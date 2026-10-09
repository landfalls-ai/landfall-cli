package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/instance"
)

var whoamiNow = time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)

func whoamiStr(s string) *string { return &s }
func whoamiI64(n int64) *int64   { return &n }

// whoamiDeps is a machine with this credential (nil: none) and this
// environment, pointed at the hosted Landfall unless the env says otherwise.
func whoamiDeps(t *testing.T, cache *auth.Credentials, env map[string]string) WhoamiDeps {
	t.Helper()
	// No saved nomination on this machine.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return WhoamiDeps{
		Env:       func(k string) string { return env[k] },
		ReadCache: func() *auth.Credentials { return cache },
		Now:       func() time.Time { return whoamiNow },
	}
}

func TestWhoamiIsOfflineAndFollowsTheSignInRule(t *testing.T) {
	hosted := instance.Hosted()
	hostedInst := &instance.Instance{Name: hosted.Name, Web: hosted.Web, API: hosted.API, Docs: hosted.Docs}
	in := func(d time.Duration) *int64 { return whoamiI64(whoamiNow.Add(d).UnixMilli()) }

	cases := []struct {
		name        string
		cache       *auth.Credentials
		env         map[string]string
		signedIn    bool
		reason      string
		org         string
		refreshable any
		expiresAt   any
	}{
		{name: "no credential", cache: nil, reason: "none"},
		{name: "an empty file", cache: &auth.Credentials{}, reason: "none"},
		{
			name:     "a fresh token",
			cache:    &auth.Credentials{AccessToken: "a", Instance: hostedInst, ExpiresAt: in(30 * time.Minute), OrgSlug: whoamiStr("acme")},
			signedIn: true, org: "acme", refreshable: false, expiresAt: "2026-10-08T16:30:00Z",
		},
		{
			name:     "expired but refreshable",
			cache:    &auth.Credentials{AccessToken: "a", Instance: hostedInst, ExpiresAt: in(-2 * time.Hour), RefreshToken: whoamiStr("r"), OrgSlug: whoamiStr("acme")},
			signedIn: true, org: "acme", refreshable: true, expiresAt: "2026-10-08T14:00:00Z",
		},
		{
			name:   "expired and not refreshable",
			cache:  &auth.Credentials{AccessToken: "a", Instance: hostedInst, ExpiresAt: in(-2 * time.Hour), OrgSlug: whoamiStr("acme")},
			reason: "expired", org: "acme", refreshable: false, expiresAt: "2026-10-08T14:00:00Z",
		},
		{
			name:   "inside the refresh skew with nothing to refresh with",
			cache:  &auth.Credentials{AccessToken: "a", Instance: hostedInst, ExpiresAt: in(30 * time.Second), OrgSlug: whoamiStr("acme")},
			reason: "expired", org: "acme", refreshable: false, expiresAt: "2026-10-08T16:00:30Z",
		},
		{
			name:   "no expiry recorded and nothing to refresh with",
			cache:  &auth.Credentials{AccessToken: "a", Instance: hostedInst, OrgSlug: whoamiStr("acme")},
			reason: "expired", org: "acme", refreshable: false,
		},
		{
			name:   "minted by another Landfall",
			cache:  &auth.Credentials{AccessToken: "a", Instance: &instance.Instance{Name: "custom", Web: "https://lf.example", API: "https://lf.example"}, ExpiresAt: in(time.Hour), RefreshToken: whoamiStr("r"), OrgSlug: whoamiStr("acme")},
			reason: "other-instance", org: "acme", refreshable: true, expiresAt: "2026-10-08T17:00:00Z",
		},
		{
			name:   "predates instance tracking",
			cache:  &auth.Credentials{AccessToken: "a", ExpiresAt: in(time.Hour), OrgSlug: whoamiStr("acme")},
			reason: "other-instance", org: "acme", refreshable: false, expiresAt: "2026-10-08T17:00:00Z",
		},
		{
			name:     "LANDFALL_SLUG wins, as for every person-read",
			cache:    &auth.Credentials{AccessToken: "a", Instance: hostedInst, ExpiresAt: in(time.Hour), OrgSlug: whoamiStr("acme")},
			env:      map[string]string{"LANDFALL_SLUG": "beta"},
			signedIn: true, org: "beta", refreshable: false, expiresAt: "2026-10-08T17:00:00Z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ans := roundTrip(t, RunWhoami(whoamiDeps(t, tc.cache, tc.env)))
			if ans["ok"] != true || ans["signedIn"] != tc.signedIn {
				t.Fatalf("answer = %v, want signedIn %v", ans, tc.signedIn)
			}
			inst := ans["instance"].(map[string]any)
			if inst["name"] != "hosted" || inst["web"] != hosted.Web || inst["api"] != hosted.API {
				t.Fatalf("instance = %v", inst)
			}
			if tc.signedIn {
				if _, has := ans["reason"]; has {
					t.Fatalf("a sign-in carries no reason: %v", ans)
				}
			} else if ans["reason"] != tc.reason {
				t.Fatalf("reason = %v, want %q", ans["reason"], tc.reason)
			}
			if got, _ := ans["org"].(string); got != tc.org {
				t.Fatalf("org = %q, want %q", got, tc.org)
			}
			if ans["refreshable"] != tc.refreshable {
				t.Fatalf("refreshable = %v, want %v", ans["refreshable"], tc.refreshable)
			}
			if ans["expiresAt"] != tc.expiresAt {
				t.Fatalf("expiresAt = %v, want %v", ans["expiresAt"], tc.expiresAt)
			}
		})
	}
}

// TestWhoamiNamesTheInstanceItWouldSignInTo: a --url style nomination in the
// environment is named, not assumed to be the hosted one.
func TestWhoamiNamesTheInstanceItWouldSignInTo(t *testing.T) {
	ans := roundTrip(t, RunWhoami(whoamiDeps(t, nil, map[string]string{"LANDFALL_URL": "https://lf.example"})))
	inst := ans["instance"].(map[string]any)
	if ans["signedIn"] != false || inst["name"] != "custom" || inst["web"] != "https://lf.example" || inst["api"] != "https://lf.example" {
		t.Fatalf("answer = %v", ans)
	}
	// A credential minted by that instance is a sign-in there.
	cache := &auth.Credentials{AccessToken: "a", Instance: &instance.Instance{Name: "custom", Web: "https://lf.example", API: "https://lf.example"}, RefreshToken: whoamiStr("r")}
	if ans := RunWhoami(whoamiDeps(t, cache, map[string]string{"LANDFALL_URL": "https://lf.example"})); ans["signedIn"] != true {
		t.Fatalf("answer = %v", ans)
	}
}

func TestWhoamiText(t *testing.T) {
	for _, tc := range []struct {
		ans  map[string]any
		want string
	}{
		{map[string]any{"ok": true, "signedIn": true, "org": "acme", "instance": map[string]any{"web": "https://lf.example"}}, "Signed in to acme at https://lf.example."},
		{map[string]any{"ok": true, "signedIn": false, "reason": "none", "instance": map[string]any{"web": "https://lf.example"}}, "Not signed in to https://lf.example. Run landfall login."},
		{map[string]any{"ok": true, "signedIn": false, "reason": "expired", "instance": map[string]any{"web": "https://lf.example"}}, "Your sign-in to https://lf.example expired. Run landfall login."},
		{map[string]any{"ok": true, "signedIn": false, "reason": "other-instance", "instance": map[string]any{"web": "https://lf.example"}}, "Your sign-in is for another Landfall, not https://lf.example. Run landfall login."},
	} {
		if got := whoamiText(tc.ans); got != tc.want {
			t.Fatalf("text = %q, want %q", got, tc.want)
		}
		assertPlainSentence(t, tc.want)
	}
}

// TestWhoamiCommandAnswersOneJSONLine runs `landfall whoami --json --host
// claude-code` through the real argument handling, against a credentials
// file on disk: one JSON line, exit 0, and the file is left exactly as it
// was (no refresh, which would rotate it).
func TestWhoamiCommandAnswersOneJSONLine(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	for _, k := range []string{"LANDFALL_URL", "LANDFALL_WEB_URL", "LANDFALL_BASE_URL", "LANDFALL_SLUG"} {
		t.Setenv(k, "")
	}
	hosted := instance.Hosted()
	expired := time.Now().Add(-time.Hour).UnixMilli()
	cred, _ := json.Marshal(auth.Credentials{
		AccessToken: "a", Instance: &hosted, RefreshToken: whoamiStr("r"), ExpiresAt: &expired, OrgSlug: whoamiStr("acme"),
	})
	path := filepath.Join(cfg, "landfall", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, cred, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := run(&UI{Out: &out, Err: io.Discard}, []string{"whoami", "--json", "--host", "claude-code", "--surprise"}, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one line, got %q", out.String())
	}
	var ans map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ans); err != nil {
		t.Fatalf("not JSON: %q", lines[0])
	}
	if ans["ok"] != true || ans["signedIn"] != true || ans["org"] != "acme" || ans["refreshable"] != true {
		t.Fatalf("answer = %v", ans)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, cred) {
		t.Fatalf("whoami changed the credentials file")
	}

	// Without --json, a sentence for a person.
	out.Reset()
	if code := run(&UI{Out: &out, Err: io.Discard}, []string{"whoami"}, func(string) string { return "" }); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "Signed in to acme at "+hosted.Web+"." {
		t.Fatalf("text = %q", got)
	}
}
