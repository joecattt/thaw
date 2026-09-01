package process

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/joecattt/thaw/pkg/models"
)

// Discovery finds running terminal sessions and their process trees.
type Discovery interface {
	// ListShells returns all interactive shell processes with TTYs.
	ListShells() ([]ShellInfo, error)
	// Children returns child processes of a given PID.
	Children(pid int) ([]models.Process, error)
	// CWD returns the current working directory of a process.
	CWD(pid int) (string, error)
	// Environ returns the environment variables of a process.
	Environ(pid int) (map[string]string, error)
}

// ShellInfo holds basic info about a running shell.
type ShellInfo struct {
	PID     int
	TTY     string
	Shell   string // basename: zsh, bash, fish
	Command string // full command line
}

// LabelMatcher maps regex patterns to human-readable labels.
type LabelMatcher struct {
	patterns map[string]*regexp.Regexp
}

// NewLabelMatcher builds a matcher from config label map.
func NewLabelMatcher(labels map[string]string) *LabelMatcher {
	m := &LabelMatcher{patterns: make(map[string]*regexp.Regexp)}
	for pattern, label := range labels {
		re, err := regexp.Compile("(?i)(" + pattern + ")")
		if err == nil {
			m.patterns[label] = re
		}
	}
	return m
}

// Match returns the best label for a command string.
func (m *LabelMatcher) Match(command string) string {
	for label, re := range m.patterns {
		if re.MatchString(command) {
			return label
		}
	}
	return ""
}

// ForegroundCommand determines the active foreground command for a shell.
func ForegroundCommand(children []models.Process, shellName string) string {
	if len(children) == 0 {
		return shellName
	}
	deepest := children[len(children)-1]
	cmd := deepest.Command
	if cmd == "" {
		cmd = shellName
	}
	return cmd
}

// EnvDiff computes the difference between a process's environment and a baseline.
// Returns only vars that were added or changed relative to baseline.
// blocklist contains patterns (case-insensitive substrings) to exclude — prevents credential leaks.
func EnvDiff(processEnv, baselineEnv map[string]string, blocklist []string) models.EnvDelta {
	delta := models.EnvDelta{
		Set: make(map[string]string),
	}

	// Vars to always exclude — noise or ephemeral
	skip := map[string]bool{
		"_": true, "SHLVL": true, "OLDPWD": true, "PWD": true,
		"TERM_SESSION_ID": true, "TERM_PROGRAM": true, "TERM_PROGRAM_VERSION": true,
		"SHELL_SESSION_DIR": true, "SHELL_SESSION_FILE": true, "SHELL_SESSION_HISTFILE": true,
		"SHELL_SESSION_HISTFILE_NEW": true, "SHELL_SESSION_DID_INIT": true,
		"WINDOWID": true, "COLUMNS": true, "LINES": true,
		"SSH_AUTH_SOCK": true, "SSH_AGENT_PID": true, "GPG_AGENT_INFO": true,
		"DISPLAY": true, "SECURITYSESSIONID": true,
		"HISTFILE": true, "HISTSIZE": true, "SAVEHIST": true,
		"TMPDIR": true, "XPC_FLAGS": true, "XPC_SERVICE_NAME": true,
		"COLORTERM": true, "ITERM_SESSION_ID": true, "ITERM_PROFILE": true,
		"TERM": true, "TERMCAP": true, "LC_TERMINAL": true, "LC_TERMINAL_VERSION": true,
	}

	// Default credential patterns — always blocked
	defaultBlocklist := []string{
		"SECRET", "TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL",
		"API_KEY", "APIKEY", "PRIVATE_KEY", "PRIVATEKEY",
		"ACCESS_KEY", "ACCESSKEY", "SESSION_KEY",
		"JWT", "BEARER", "OAUTH",
		"DATABASE_URL", "REDIS_URL", "MONGO_URI", "AMQP_URL",
		"DB_URL", "DB_PASSWORD", "DB_PASS",
		"MYSQL_PASSWORD", "MYSQL_ROOT_PASSWORD",
		"POSTGRES_PASSWORD", "PGPASSWORD",
		"MONGO_PASSWORD", "MONGO_INITDB_ROOT_PASSWORD",
		"STRIPE_", "TWILIO_", "SENDGRID_", "AWS_SECRET",
		"GITHUB_TOKEN", "GITLAB_TOKEN", "NPM_TOKEN",
		"ENCRYPTION", "SIGNING_KEY", "MASTER_KEY",
		"COOKIE_SECRET", "SESSION_SECRET",
		"SMTP_PASSWORD", "MAIL_PASSWORD",
	}
	allPatterns := append(defaultBlocklist, blocklist...)

	for k, v := range processEnv {
		if skip[k] {
			continue
		}
		if matchesBlocklist(k, allPatterns) {
			continue
		}
		// Also scan values for embedded credentials (e.g. JSON config with passwords)
		if valueContainsCredential(v) {
			continue
		}
		if baseVal, exists := baselineEnv[k]; !exists || baseVal != v {
			delta.Set[k] = v
		}
	}

	for k := range baselineEnv {
		if skip[k] || matchesBlocklist(k, allPatterns) {
			continue
		}
		if _, exists := processEnv[k]; !exists {
			delta.Unset = append(delta.Unset, k)
		}
	}

	return delta
}

// matchesBlocklist returns true if the key matches any blocklist pattern (case-insensitive).
func matchesBlocklist(key string, patterns []string) bool {
	upper := strings.ToUpper(key)
	for _, p := range patterns {
		if strings.Contains(upper, strings.ToUpper(p)) {
			return true
		}
	}
	return false
}

// valueContainsCredential checks if an env var value contains embedded credentials.
// Catches JSON configs with password fields, connection strings with credentials, etc.
func valueContainsCredential(value string) bool {
	lower := strings.ToLower(value)

	// Connection strings with credentials: scheme://user:pass@host
	if strings.Contains(value, "://") && strings.Contains(value, "@") {
		// Check if there's a password component
		schemeEnd := strings.Index(value, "://")
		if schemeEnd >= 0 {
			afterScheme := value[schemeEnd+3:]
			if strings.Contains(afterScheme, ":") && strings.Contains(afterScheme, "@") {
				atPos := strings.Index(afterScheme, "@")
				colonPos := strings.Index(afterScheme, ":")
				if colonPos < atPos { // user:pass@host pattern
					return true
				}
			}
		}
	}

	// JSON with credential-looking keys
	credKeys := []string{`"password"`, `"secret"`, `"token"`, `"api_key"`, `"private_key"`, `"apikey"`}
	for _, k := range credKeys {
		if strings.Contains(lower, k) {
			return true
		}
	}

	return false
}

// parsePSLine parses a single line of `ps` output.
func parsePSLine(line string) (pid, ppid int, tty, command string, err error) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return 0, 0, "", "", fmt.Errorf("too few fields: %q", line)
	}

	pid, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, "", "", fmt.Errorf("bad PID: %w", err)
	}

	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, "", "", fmt.Errorf("bad PPID: %w", err)
	}

	tty = fields[2]
	command = strings.Join(fields[3:], " ")
	return
}

func runPS(args ...string) (string, error) {
	cmd := exec.Command("ps", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ps %v: %w", args, err)
	}
	return string(out), nil
}

func isShell(cmd string) bool {
	base := strings.TrimPrefix(cmd, "-")
	shells := []string{"zsh", "bash", "fish", "sh", "dash", "ksh", "tcsh", "csh"}
	for _, s := range shells {
		if base == s || strings.HasSuffix(base, "/"+s) {
			return true
		}
	}
	return false
}

func shellBasename(cmd string) string {
	cmd = strings.TrimPrefix(cmd, "-")
	parts := strings.Split(cmd, "/")
	return parts[len(parts)-1]
}

// IsSelfProcess returns true if the given PID is the current process or an ancestor
// of the current process (i.e., the shell thaw was launched from).
// This prevents thaw from capturing itself in snapshots.
func IsSelfProcess(pid int) bool {
	self := os.Getpid()
	if pid == self {
		return true
	}
	// Walk up the parent chain from self to check if pid is an ancestor
	current := self
	for i := 0; i < 10; i++ { // max depth to prevent infinite loop
		ppid := getParentPID(current)
		if ppid <= 1 {
			break
		}
		if ppid == pid {
			return true
		}
		current = ppid
	}
	return false
}

func getParentPID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PPid:") {
				ppid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
				if err == nil {
					return ppid
				}
			}
		}
	}
	// macOS fallback — use ps
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=").Output()
	if err == nil {
		ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err == nil {
			return ppid
		}
	}
	return 0
}
