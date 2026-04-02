package ordering

import (
	"sort"
	"strings"

	"github.com/joecattt/thaw/pkg/models"
)

// Priority tiers — lower number = start first.
const (
	TierInfra    = 10 // databases, message queues, docker compose
	TierBackend  = 20 // backend servers, APIs
	TierFrontend = 30 // frontend dev servers, UI
	TierTest     = 40 // test runners, watchers
	TierMonitor  = 50 // log tails, monitoring
	TierEditor   = 60 // vim, emacs, code editors
	TierRemote   = 70 // ssh sessions
	TierIdle     = 80 // idle shells
)

// Assign sets RestoreOrder on each session based on command analysis.
func Assign(sessions []models.Session) []models.Session {
	result := make([]models.Session, len(sessions))
	copy(result, sessions)

	for i := range result {
		result[i].RestoreOrder = classify(result[i])
	}

	return result
}

// Sort returns sessions ordered by RestoreOrder (lowest first).
// Also marks sessions that are subsumed by docker-compose/k8s orchestrators.
func Sort(sessions []models.Session) []models.Session {
	sorted := make([]models.Session, len(sessions))
	copy(sorted, sessions)

	// Detect orchestrator meta-commands and mark subsumed sessions
	markSubsumed(sorted)

	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].RestoreOrder < sorted[j].RestoreOrder
	})

	return sorted
}

// markSubsumed detects docker-compose/k8s orchestrators and suppresses individual
// service commands that the orchestrator already manages.
// A session running `docker compose up` makes individual `node server.js` or
// `postgres` sessions in the same project redundant.
func markSubsumed(sessions []models.Session) {
	// Find orchestrator sessions
	type orchestrator struct {
		idx     int
		cwd     string
		repoRoot string
	}
	var orchestrators []orchestrator

	for i, s := range sessions {
		cmd := strings.ToLower(s.Command)
		if matchesAny(cmd, []string{
			"docker compose up", "docker-compose up",
			"docker compose start", "docker-compose start",
		}) {
			root := ""
			if s.Git != nil {
				root = s.Git.RepoRoot
			}
			orchestrators = append(orchestrators, orchestrator{idx: i, cwd: s.CWD, repoRoot: root})
		}
	}

	if len(orchestrators) == 0 {
		return
	}

	// Commands that compose typically manages
	composeManagedPatterns := []string{
		"node ", "npm run", "npm start", "yarn ",
		"python manage.py runserver", "uvicorn", "gunicorn",
		"rails server", "rails s",
		"postgres", "mysqld", "mongod", "redis-server",
		"nginx", "caddy",
	}

	for i, s := range sessions {
		if s.IsIdle() {
			continue
		}
		cmd := strings.ToLower(s.Command)

		// Check if this session is in the same project as an orchestrator
		for _, orch := range orchestrators {
			if i == orch.idx {
				continue // don't suppress the orchestrator itself
			}
			sameProject := false
			if orch.repoRoot != "" && s.Git != nil && s.Git.RepoRoot == orch.repoRoot {
				sameProject = true
			} else if orch.cwd != "" && s.CWD != "" && (strings.HasPrefix(s.CWD, orch.cwd) || strings.HasPrefix(orch.cwd, s.CWD)) {
				sameProject = true
			}

			if sameProject && matchesAny(cmd, composeManagedPatterns) {
				// Mark as subsumed — bump to idle tier so it doesn't auto-execute
				sessions[i].RestoreOrder = TierIdle - 1
				sessions[i].Intent = "(subsumed by docker compose) " + s.Intent
				break
			}
		}
	}
}

// classify determines the startup priority tier for a session.
func classify(s models.Session) int {
	if s.IsIdle() {
		return TierIdle
	}

	cmd := strings.ToLower(s.Command)

	// Databases / data store clients — check BEFORE infra servers
	// because commands like "psql -U postgres mydb" contain "postgres" as an argument
	if matchesAny(cmd, []string{
		"psql", "mysql ", "redis-cli", "mongo ",
		"sqlite3", "pgcli", "mycli",
	}) {
		return TierInfra + 5
	}

	// Infrastructure servers — must start first
	if matchesAny(cmd, []string{
		"docker compose up", "docker-compose up",
		"postgres ", "postgresq", "mysqld", "mongod", "redis-server",
		"rabbitmq", "kafka", "zookeeper", "consul",
		"docker run", "podman run",
		"minikube", "kind create",
	}) {
		return TierInfra
	}

	// Backend servers
	if matchesAny(cmd, []string{
		"npm run dev", "npm run start", "npm start",
		"yarn dev", "yarn start",
		"node server", "node index", "node app",
		"go run", "cargo run",
		"python manage.py runserver", "python -m flask",
		"uvicorn", "gunicorn", "hypercorn",
		"rails server", "rails s",
		"php artisan serve",
		"mix phx.server",
		"bundle exec",
	}) {
		return TierBackend
	}

	// Frontend dev servers
	if matchesAny(cmd, []string{
		"next dev", "vite", "webpack",
		"ng serve", "ng s",
		"vue-cli-service serve",
		"gatsby develop",
		"nuxt dev",
		"npm run client", "yarn client",
	}) {
		return TierFrontend
	}

	// Test runners
	if matchesAny(cmd, []string{
		"npm test", "yarn test", "jest", "vitest",
		"pytest", "go test", "cargo test",
		"rspec", "minitest",
		"npm run test", "yarn run test",
	}) {
		return TierTest
	}

	// Monitoring / logs
	if matchesAny(cmd, []string{
		"tail -f", "tail -F", "less +F",
		"journalctl -f", "dmesg -w",
		"kubectl logs", "docker logs",
		"htop", "top", "btop",
		"watch ",
	}) {
		return TierMonitor
	}

	// Editors
	if matchesAny(cmd, []string{
		"vim ", "nvim ", "emacs ", "nano ",
		"vi ", "code ",
	}) {
		return TierEditor
	}

	// Remote sessions
	if matchesAny(cmd, []string{"ssh ", "mosh "}) {
		return TierRemote
	}

	// K8s operations
	if matchesAny(cmd, []string{"kubectl", "helm", "k9s"}) {
		return TierMonitor // treat as monitoring
	}

	// Default: treat as backend-ish
	return TierBackend + 5
}

func matchesAny(cmd string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(cmd, p) {
			return true
		}
	}
	return false
}

// ExplainOrder returns a human-readable explanation of why sessions are ordered this way.
func ExplainOrder(sessions []models.Session) []string {
	var explanations []string
	for _, s := range sessions {
		tier := TierName(s.RestoreOrder)
		explanations = append(explanations, tier+": "+s.Label+" ("+s.Command+")")
	}
	return explanations
}

func TierName(order int) string {
	switch {
	case order <= TierInfra+5:
		return "infra"
	case order <= TierBackend+5:
		return "backend"
	case order <= TierFrontend+5:
		return "frontend"
	case order <= TierTest+5:
		return "test"
	case order <= TierMonitor+5:
		return "monitor"
	case order <= TierEditor+5:
		return "editor"
	case order <= TierRemote+5:
		return "remote"
	default:
		return "idle"
	}
}
