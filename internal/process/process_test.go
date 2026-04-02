package process

import (
	"testing"
)

func TestEnvDiff_BasicDiff(t *testing.T) {
	baseline := map[string]string{"HOME": "/home/user", "PATH": "/usr/bin"}
	process := map[string]string{"HOME": "/home/user", "PATH": "/usr/bin", "NODE_ENV": "development"}

	delta := EnvDiff(process, baseline, nil)

	if _, ok := delta.Set["NODE_ENV"]; !ok {
		t.Error("expected NODE_ENV in delta")
	}
	if _, ok := delta.Set["HOME"]; ok {
		t.Error("HOME should not be in delta (unchanged)")
	}
}

func TestEnvDiff_SkipsNoiseVars(t *testing.T) {
	baseline := map[string]string{}
	process := map[string]string{"SHLVL": "2", "OLDPWD": "/tmp", "PWD": "/home", "NODE_ENV": "test"}

	delta := EnvDiff(process, baseline, nil)

	if _, ok := delta.Set["SHLVL"]; ok {
		t.Error("SHLVL should be skipped")
	}
	if _, ok := delta.Set["OLDPWD"]; ok {
		t.Error("OLDPWD should be skipped")
	}
	if _, ok := delta.Set["NODE_ENV"]; !ok {
		t.Error("NODE_ENV should be included")
	}
}

func TestEnvDiff_BlocksCredentials(t *testing.T) {
	baseline := map[string]string{}
	process := map[string]string{
		"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI",
		"DATABASE_URL":         "postgres://user:pass@host/db",
		"API_KEY":              "sk-1234567890",
		"GITHUB_TOKEN":         "ghp_xxxxxxxxxxxx",
		"NODE_ENV":             "production",
		"MY_APP_SECRET":        "supersecret",
		"STRIPE_SECRET_KEY":    "sk_test_xxx",
		"SAFE_VAR":             "hello",
	}

	delta := EnvDiff(process, baseline, nil)

	blocked := []string{"AWS_SECRET_ACCESS_KEY", "DATABASE_URL", "API_KEY", "GITHUB_TOKEN", "MY_APP_SECRET", "STRIPE_SECRET_KEY"}
	for _, key := range blocked {
		if _, ok := delta.Set[key]; ok {
			t.Errorf("%s should be blocked by credential blocklist", key)
		}
	}

	if _, ok := delta.Set["NODE_ENV"]; !ok {
		t.Error("NODE_ENV should pass through")
	}
	if _, ok := delta.Set["SAFE_VAR"]; !ok {
		t.Error("SAFE_VAR should pass through")
	}
}

func TestEnvDiff_CustomBlocklist(t *testing.T) {
	baseline := map[string]string{}
	process := map[string]string{
		"INTERNAL_SERVICE_URL": "http://internal:8080",
		"PUBLIC_URL":           "http://example.com",
	}

	delta := EnvDiff(process, baseline, []string{"INTERNAL_"})

	if _, ok := delta.Set["INTERNAL_SERVICE_URL"]; ok {
		t.Error("INTERNAL_SERVICE_URL should be blocked by custom blocklist")
	}
	if _, ok := delta.Set["PUBLIC_URL"]; !ok {
		t.Error("PUBLIC_URL should pass through")
	}
}

func TestEnvDiff_BlocksEmbeddedCredentialsInValues(t *testing.T) {
	baseline := map[string]string{}
	process := map[string]string{
		"CONFIG":    `{"password":"hunter2","host":"localhost"}`,
		"DB_CONN":   "postgres://admin:secretpass@db.internal:5432/myapp",
		"SAFE_JSON": `{"host":"localhost","port":3000}`,
	}

	delta := EnvDiff(process, baseline, nil)

	if _, ok := delta.Set["CONFIG"]; ok {
		t.Error("CONFIG with embedded password should be blocked")
	}
	if _, ok := delta.Set["DB_CONN"]; ok {
		t.Error("DB_CONN should be blocked by key pattern")
	}
	if _, ok := delta.Set["SAFE_JSON"]; !ok {
		t.Error("SAFE_JSON should pass through")
	}
}

func TestMatchesBlocklist(t *testing.T) {
	patterns := []string{"SECRET", "TOKEN", "PASSWORD"}

	tests := []struct {
		key    string
		expect bool
	}{
		{"AWS_SECRET_KEY", true},
		{"MY_TOKEN", true},
		{"DB_PASSWORD", true},
		{"NODE_ENV", false},
		{"HOME", false},
		{"secret_thing", true}, // case insensitive
	}

	for _, tt := range tests {
		got := matchesBlocklist(tt.key, patterns)
		if got != tt.expect {
			t.Errorf("matchesBlocklist(%q) = %v, want %v", tt.key, got, tt.expect)
		}
	}
}

func TestValueContainsCredential(t *testing.T) {
	tests := []struct {
		value  string
		expect bool
	}{
		{`postgres://user:password@host/db`, true},
		{`redis://default:mypass@redis:6379`, true},
		{`http://example.com/api`, false},
		{`{"password":"secret"}`, true},
		{`{"api_key":"sk-xxx"}`, true},
		{`{"host":"localhost","port":3000}`, false},
		{`simple string`, false},
		{``, false},
	}

	for _, tt := range tests {
		got := valueContainsCredential(tt.value)
		if got != tt.expect {
			t.Errorf("valueContainsCredential(%q) = %v, want %v", tt.value, got, tt.expect)
		}
	}
}
