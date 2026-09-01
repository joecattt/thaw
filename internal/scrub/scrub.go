package scrub

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// High entropy / secret patterns
	// AWS access key ID: AKIA... (20 chars)
	awsRegex = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)
	// AWS secret access key bound to its canonical variable name — any separator,
	// including the credentials-file "aws_secret_access_key = X" space-padded form.
	awsSecretRegex = regexp.MustCompile(`(?i)(aws_secret_access_key["']?[\s:=]+["']?)[A-Za-z0-9+/]{40}`)
	// Generic hex/b64 secret like: api_key=... — value charset includes +/= so a
	// base64 secret can't truncate the match below the 16-char floor and escape.
	genericSecretRegex = regexp.MustCompile(`(?i)((?:key|secret|token|pass|auth|pwd)[-_\s]*[:=][\s]*)[a-zA-Z0-9\-_.~+/=]{16,}`)
	// JWT
	jwtRegex = regexp.MustCompile(`eyJ[a-zA-Z0-9\-_]+\.eyJ[a-zA-Z0-9\-_]+\.[a-zA-Z0-9\-_]+`)
	// Private Key
	privateKeyRegex = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]+ PRIVATE KEY-----.*?-----END [A-Z ]+ PRIVATE KEY-----`)
	// Vendor token prefixes: GitHub PATs, GitLab PATs, Slack tokens, and
	// Stripe/OpenAI-style sk-/pk- keys (sk_live_..., sk-proj-..., etc.).
	githubTokenRegex = regexp.MustCompile(`\b(ghp_|gho_|ghu_|ghs_|ghr_)[A-Za-z0-9]{20,}`)
	gitlabTokenRegex = regexp.MustCompile(`\bglpat-[A-Za-z0-9\-_]{20,}`)
	slackTokenRegex  = regexp.MustCompile(`\b(xox[a-z]-)[A-Za-z0-9\-]{10,}`)
	skpkTokenRegex   = regexp.MustCompile(`\b([sp]k[-_])[A-Za-z0-9\-_]{16,}`)
	// Bare Bearer/Basic auth values — any token shape, not just "bearer eyJ".
	bearerRegex = regexp.MustCompile(`(?i)\b((?:bearer|basic)\s+)[A-Za-z0-9\-_.~+/=]{8,}`)
	// .netrc-style "password <value>" with no = or : separator.
	netrcPassRegex = regexp.MustCompile(`(?i)\b(password\s+)[^\s'"]{6,}`)
)

// Text redacts sensitive patterns from a string.
func Text(s string) string {
	if s == "" {
		return ""
	}

	s = privateKeyRegex.ReplaceAllString(s, "[PRIVATE-KEY-REDACTED]")
	s = jwtRegex.ReplaceAllString(s, "[JWT-REDACTED]")
	s = awsRegex.ReplaceAllString(s, "AKIA[REDACTED]")
	s = awsSecretRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = githubTokenRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = gitlabTokenRegex.ReplaceAllString(s, "glpat-[REDACTED]")
	s = slackTokenRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = skpkTokenRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = bearerRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = netrcPassRegex.ReplaceAllString(s, "${1}[REDACTED]")
	// Generic secrets last: keep the key/separator, hide the value.
	s = genericSecretRegex.ReplaceAllString(s, "${1}[REDACTED]")

	return s
}

// secretFlags are CLI flags whose following argument (or =value) is a credential.
var secretFlags = map[string]bool{
	"-p": true, "-P": true, "--password": true, "--passwd": true, "--pass": true,
	"--token": true, "--secret": true, "--api-key": true,
	"--auth": true, "--credentials": true,
}

// mysqlLike commands accept an attached-value -pSECRET flag (no space).
var mysqlLike = map[string]bool{
	"mysql": true, "mysqldump": true, "mysqladmin": true,
	"mariadb": true, "mariadb-dump": true,
}

// Command redacts credential-bearing arguments from a command line, then runs
// the result through Text. Field-aware pass mirrors the history scrubber:
// --password X, --token=X, mysql -pSecret123, Bearer <tok>, export KEY=value.
func Command(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return Text(cmd)
	}
	base := filepath.Base(parts[0])

	var result []string
	skipNext := false
	for i, part := range parts {
		if skipNext {
			result = append(result, "[redacted]")
			skipNext = false
			continue
		}

		// --flag=value patterns
		for flag := range secretFlags {
			if strings.HasPrefix(part, flag+"=") {
				part = flag + "=[redacted]"
				break
			}
		}

		// Flag followed by value
		if secretFlags[part] {
			result = append(result, part)
			skipNext = true
			continue
		}

		// mysql-style attached password: -pSecret123
		if mysqlLike[base] && strings.HasPrefix(part, "-p") && len(part) > 2 && !strings.HasPrefix(part, "--") {
			result = append(result, "-p[redacted]")
			continue
		}

		// "Bearer <token>" (e.g. inside a curl -H argument)
		if strings.ToLower(part) == "bearer" && i+1 < len(parts) {
			result = append(result, part)
			skipNext = true
			continue
		}

		// export KEY=value where KEY looks like a secret
		if (parts[0] == "export" || parts[0] == "set") && i == 1 && strings.Contains(part, "=") {
			eqIdx := strings.Index(part, "=")
			keyName := strings.ToUpper(part[:eqIdx])
			for _, kw := range []string{"SECRET", "TOKEN", "PASSWORD", "KEY", "CREDENTIAL", "AUTH"} {
				if strings.Contains(keyName, kw) {
					part = part[:eqIdx+1] + "[redacted]"
					break
				}
			}
		}

		result = append(result, part)
	}

	return Text(strings.Join(result, " "))
}

// Map redacts sensitive values from a map (e.g. env vars).
func Map(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	res := make(map[string]string)
	for k, v := range m {
		kl := strings.ToLower(k)
		if strings.Contains(kl, "key") || strings.Contains(kl, "secret") ||
			strings.Contains(kl, "token") || strings.Contains(kl, "password") ||
			strings.Contains(kl, "pass") || strings.Contains(kl, "auth") {
			res[k] = "[REDACTED]"
		} else {
			res[k] = Text(v)
		}
	}
	return res
}

// Slice redacts sensitive patterns from each string in a slice.
func Slice(ss []string) []string {
	if ss == nil {
		return nil
	}
	res := make([]string, len(ss))
	for i, s := range ss {
		res[i] = Text(s)
	}
	return res
}
