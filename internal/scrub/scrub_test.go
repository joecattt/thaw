package scrub

import (
	"strings"
	"testing"
)

// TestText_ProvenEscapes uses the audit's confirmed escape cases as fixtures.
// Each secret here provably survived the old scrubber; all must die now.
func TestText_ProvenEscapes(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string // the substring that must NOT survive
	}{
		{
			// Space-separated key-value; the '/' in the value broke the old
			// charset and truncated the match below the 16-char floor.
			"aws secret access key, credentials-file form",
			"aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
		{
			// The '+' at char 10 truncated the old generic match to 9 chars.
			"base64 secret with + in api_key",
			"api_key=abc123XYZ+Secret9876base64chunkA==",
			"abc123XYZ+Secret9876base64chunkA==",
		},
		{
			"bare GitHub PAT",
			"remote: token ghp_16C7e42F292c6912E7710c838347Ae178B4a in use",
			"ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		},
		{
			"bare GitLab PAT",
			"using glpat-xQzR7wYv2mKp9sDf3aBc for CI",
			"glpat-xQzR7wYv2mKp9sDf3aBc",
		},
		{
			"bare Stripe live key",
			"charge failed for sk_live_4eC39HqLyjWDarjtT1zdp7dc",
			"sk_live_4eC39HqLyjWDarjtT1zdp7dc",
		},
		{
			"bare OpenAI-style key",
			"OPENAI: sk-proj-Ab12Cd34Ef56Gh78Ij90",
			"sk-proj-Ab12Cd34Ef56Gh78Ij90",
		},
		{
			"Slack bot token",
			"slack: xoxb-2444333222111-9998887776665-AbCdEfGhIjKl",
			"xoxb-2444333222111",
		},
		{
			// Old code only knew "bearer eyJ" — Google-style ya29. escaped.
			"non-JWT Bearer token",
			"Authorization: Bearer ya29.a0AfH6SMBx3qKvNpR8sT",
			"ya29.a0AfH6SMBx3qKvNpR8sT",
		},
		{
			".netrc password line",
			"machine api.example.com login joe password hunter2",
			"hunter2",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Text(c.input)
			if strings.Contains(got, c.secret) {
				t.Errorf("secret survived: Text(%q) = %q", c.input, got)
			}
			if !strings.Contains(got, "REDACTED") {
				t.Errorf("no redaction marker in output: %q", got)
			}
		})
	}
}

// TestText_KeepsExistingCoverage — the patterns the old code already caught.
func TestText_KeepsExistingCoverage(t *testing.T) {
	if got := Text("key AKIAIOSFODNN7EXAMPLE"); strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS access key survived: %q", got)
	}
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.abcDEF123456"
	if got := Text("auth " + jwt); strings.Contains(got, jwt) {
		t.Errorf("JWT survived: %q", got)
	}
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----"
	if got := Text(pem); strings.Contains(got, "MIIEow") {
		t.Errorf("private key survived: %q", got)
	}
	if got := Text("api_key=abcdef0123456789abcdef"); strings.Contains(got, "abcdef0123456789") {
		t.Errorf("generic secret survived: %q", got)
	}
}

// TestText_NegativeCases — ordinary output must pass through untouched.
func TestText_NegativeCases(t *testing.T) {
	cases := []string{
		"$ ls -la /tmp",
		"total 42 files copied successfully",
		"npm install completed in 4.2s",
		"git commit -m 'fix parser bug'",
		"make -j8 build/thaw",
		"curl https://example.com/path",
		"keyboard=qwerty",
		"export NODE_ENV=production",
		"the token: was rejected upstream",
	}
	for _, in := range cases {
		if got := Text(in); got != in {
			t.Errorf("false positive: Text(%q) = %q", in, got)
		}
	}
}

func TestCommand_FlagStyleSecrets(t *testing.T) {
	cases := []struct {
		input  string
		secret string
	}{
		{"mysql -u root -pSecret123", "Secret123"},
		{"mysql -u root -p secretpass mydb", "secretpass"},
		{"psql --password=hunter22 mydb", "hunter22"},
		{"curl -H 'Authorization: Bearer abc.def.ghi' http://api.com", "abc.def.ghi"},
		{"export API_KEY=sk-1234", "sk-1234"},
	}
	for _, c := range cases {
		got := Command(c.input)
		if strings.Contains(got, c.secret) {
			t.Errorf("secret survived: Command(%q) = %q", c.input, got)
		}
	}

	// -pXXX attached form must stay scoped to mysql-like commands: find's
	// -print flag is not a password.
	if got := Command("find . -print"); got != "find . -print" {
		t.Errorf("false positive on find -print: %q", got)
	}
	if got := Command("git status"); got != "git status" {
		t.Errorf("false positive: Command(git status) = %q", got)
	}
}

func TestMap_RedactsSecretKeys(t *testing.T) {
	got := Map(map[string]string{
		"MY_TOKEN": "supersecretvalue",
		"EDITOR":   "vim",
	})
	if got["MY_TOKEN"] != "[REDACTED]" {
		t.Errorf("MY_TOKEN = %q, want [REDACTED]", got["MY_TOKEN"])
	}
	if got["EDITOR"] != "vim" {
		t.Errorf("EDITOR = %q, want vim", got["EDITOR"])
	}
}

func TestSlice_ScrubsEachLine(t *testing.T) {
	got := Slice([]string{"plain line", "pat: ghp_16C7e42F292c6912E7710c838347Ae178B4a"})
	if got[0] != "plain line" {
		t.Errorf("plain line altered: %q", got[0])
	}
	if strings.Contains(got[1], "16C7e42F") {
		t.Errorf("token survived in slice: %q", got[1])
	}
}
