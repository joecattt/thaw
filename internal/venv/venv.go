package venv

import (
	"os"
	"path/filepath"
	"strings"
)

// Activation represents a virtual environment that needs to be re-activated on restore.
type Activation struct {
	Type    string // python-venv | nvm | rbenv | pyenv | asdf | conda
	Command string // the shell command to activate it
}

// Detect checks a session's CWD and env vars for active virtual environments.
// Returns activation commands that should be run on restore instead of raw env injection.
func Detect(cwd string, envDelta map[string]string) []Activation {
	var activations []Activation

	// Python virtualenv — VIRTUAL_ENV is set
	if venvPath, ok := envDelta["VIRTUAL_ENV"]; ok {
		activateScript := filepath.Join(venvPath, "bin", "activate")
		if fileExists(activateScript) {
			activations = append(activations, Activation{
				Type:    "python-venv",
				Command: "source " + activateScript,
			})
		}
	}

	// Conda — CONDA_DEFAULT_ENV or CONDA_PREFIX set
	if condaEnv, ok := envDelta["CONDA_DEFAULT_ENV"]; ok {
		activations = append(activations, Activation{
			Type:    "conda",
			Command: "conda activate " + condaEnv,
		})
	} else if _, ok := envDelta["CONDA_PREFIX"]; ok {
		// CONDA_PREFIX without CONDA_DEFAULT_ENV — use prefix directly
		activations = append(activations, Activation{
			Type:    "conda",
			Command: "conda activate",
		})
	}

	// nvm — check .nvmrc or .node-version in CWD
	if fileExists(filepath.Join(cwd, ".nvmrc")) || fileExists(filepath.Join(cwd, ".node-version")) {
		if commandExists("nvm") || commandExists("fnm") {
			activations = append(activations, Activation{
				Type:    "nvm",
				Command: "nvm use 2>/dev/null || fnm use 2>/dev/null",
			})
		}
	}

	// pyenv — check .python-version
	if fileExists(filepath.Join(cwd, ".python-version")) {
		if commandExists("pyenv") {
			activations = append(activations, Activation{
				Type:    "pyenv",
				Command: "pyenv activate 2>/dev/null",
			})
		}
	}

	// rbenv — check .ruby-version
	if fileExists(filepath.Join(cwd, ".ruby-version")) {
		if commandExists("rbenv") {
			activations = append(activations, Activation{
				Type:    "rbenv",
				Command: "rbenv shell $(cat .ruby-version) 2>/dev/null",
			})
		}
	}

	// asdf — check .tool-versions
	if fileExists(filepath.Join(cwd, ".tool-versions")) {
		if commandExists("asdf") {
			activations = append(activations, Activation{
				Type:    "asdf",
				Command: "asdf reshim 2>/dev/null",
			})
		}
	}

	// Poetry — check if poetry shell is active
	if _, ok := envDelta["POETRY_ACTIVE"]; ok {
		activations = append(activations, Activation{
			Type:    "poetry",
			Command: "poetry shell 2>/dev/null",
		})
	}

	return activations
}

// EnvKeysToSkip returns env var keys that should be excluded from raw injection
// because a venv activation command handles them instead.
func EnvKeysToSkip(activations []Activation) map[string]bool {
	skip := make(map[string]bool)
	for _, a := range activations {
		switch a.Type {
		case "python-venv":
			skip["VIRTUAL_ENV"] = true
			skip["PYTHONPATH"] = true
			// PATH changes handled by activation
		case "conda":
			skip["CONDA_DEFAULT_ENV"] = true
			skip["CONDA_PREFIX"] = true
			skip["CONDA_SHLVL"] = true
			skip["CONDA_PROMPT_MODIFIER"] = true
		case "nvm":
			skip["NVM_DIR"] = true
			skip["NVM_BIN"] = true
			skip["NVM_INC"] = true
		case "poetry":
			skip["POETRY_ACTIVE"] = true
		}
	}
	return skip
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func commandExists(name string) bool {
	// Check common locations rather than exec.LookPath (which may not find shell functions like nvm)
	paths := []string{
		"/usr/local/bin/" + name,
		"/usr/bin/" + name,
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		paths = append(paths,
			filepath.Join(home, "."+name, name),           // ~/.nvm/nvm, ~/.pyenv/pyenv
			filepath.Join(home, ".local", "bin", name),    // pip installed
			filepath.Join(home, ".asdf", "bin", name),     // asdf
			filepath.Join(home, ".rbenv", "bin", name),    // rbenv
		)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	// Also check if the function exists via env var hint
	// nvm sets NVM_DIR, rbenv sets RBENV_ROOT, etc.
	envHints := map[string]string{
		"nvm":   "NVM_DIR",
		"pyenv": "PYENV_ROOT",
		"rbenv": "RBENV_ROOT",
		"asdf":  "ASDF_DIR",
	}
	if envKey, ok := envHints[name]; ok {
		if os.Getenv(envKey) != "" {
			return true
		}
	}
	return false
}

// FilterEnvDelta removes env keys that are handled by venv activation commands.
func FilterEnvDelta(envSet map[string]string, activations []Activation) map[string]string {
	skip := EnvKeysToSkip(activations)
	filtered := make(map[string]string)
	for k, v := range envSet {
		// Also skip PATH modifications — venv activation handles PATH
		if skip[k] || (k == "PATH" && hasPathVenvPrefix(v, activations)) {
			continue
		}
		filtered[k] = v
	}
	return filtered
}

func hasPathVenvPrefix(pathVal string, activations []Activation) bool {
	for _, a := range activations {
		if a.Type == "python-venv" || a.Type == "conda" || a.Type == "nvm" {
			return true // PATH is managed by these tools
		}
	}
	return false
}

// DescribeActivations returns a human-readable summary.
func DescribeActivations(activations []Activation) string {
	if len(activations) == 0 {
		return ""
	}
	var types []string
	for _, a := range activations {
		types = append(types, a.Type)
	}
	return strings.Join(types, ", ")
}
