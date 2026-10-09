package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/instance"
)

// fakeHandoff is a Landfall that answers the sign-in probe and the handoff
// poll: "not yet" (404) for the first `pending` polls, then the session, or
// "not yet" forever when pending is negative.
type fakeHandoff struct {
	srv     *httptest.Server
	mu      sync.Mutex
	polls   int
	pending int
}

func newFakeHandoff(t *testing.T, pending int) *fakeHandoff {
	t.Helper()
	f := &fakeHandoff{pending: pending}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/auth/cli-handoff/poll":
			f.mu.Lock()
			f.polls++
			n := f.polls
			f.mu.Unlock()
			if f.pending < 0 || n <= f.pending {
				http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
				return
			}
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accessToken": "access-1", "refreshToken": "refresh-1",
				"expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "orgSlug": "acme",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// jsonLines parses every stdout line as JSON.
func jsonLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not a JSON line: %q", line)
		}
		rows = append(rows, m)
	}
	return rows
}

func loginEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"LANDFALL_URL", "LANDFALL_WEB_URL", "LANDFALL_BASE_URL", "LANDFALL_SLUG"} {
		t.Setenv(k, "")
	}
}

func TestLoginJSONPrintsTheURLThenTheOutcome(t *testing.T) {
	loginEnv(t)
	f := newFakeHandoff(t, 2)
	var out, errOut bytes.Buffer
	var opened []string
	err := runLogin(context.Background(), &UI{Out: &out, Err: &errOut}, loginArgs{URL: f.srv.URL, JSON: true}, auth.LoginOptions{
		OpenBrowser:  func(u string) error { opened = append(opened, u); return nil },
		PollInterval: -1,
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	rows := jsonLines(t, out.String())
	if len(rows) != 2 {
		t.Fatalf("want the url line then the outcome, got %q", out.String())
	}
	url, _ := rows[0]["url"].(string)
	if rows[0]["event"] != "url" || !strings.HasPrefix(url, f.srv.URL+"/cli-auth?nonce=") || len(rows[0]) != 2 {
		t.Fatalf("first line = %v", rows[0])
	}
	// The CLI opens the browser itself, at the same URL it printed.
	if len(opened) != 1 || opened[0] != url {
		t.Fatalf("opened = %v, want [%s]", opened, url)
	}
	if rows[1]["ok"] != true || rows[1]["org"] != "acme" || rows[1]["web"] != f.srv.URL || len(rows[1]) != 3 {
		t.Fatalf("last line = %v", rows[1])
	}
	// stderr is what a plain login logs.
	for _, want := range []string{"opening your browser to sign in", url, "signed in"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("stderr %q lacks %q", errOut.String(), want)
		}
	}
	// And it signed in.
	if auth.GetCachedOrgSlug() != "acme" {
		t.Fatalf("the session was not cached")
	}
}

func TestLoginJSONTimesOut(t *testing.T) {
	loginEnv(t)
	f := newFakeHandoff(t, -1)
	var out, errOut bytes.Buffer
	err := runLogin(context.Background(), &UI{Out: &out, Err: &errOut}, loginArgs{URL: f.srv.URL, JSON: true}, auth.LoginOptions{
		OpenBrowser:  func(string) error { return nil },
		PollInterval: 5 * time.Millisecond,
		PollTimeout:  40 * time.Millisecond,
	})
	if !errors.Is(err, auth.ErrHandoffTimeout) {
		t.Fatalf("err = %v, want the handoff timeout", err)
	}
	rows := jsonLines(t, out.String())
	if len(rows) != 2 || rows[0]["event"] != "url" {
		t.Fatalf("lines = %q", out.String())
	}
	// The exact words the console turns into "Your browser did not finish
	// within 5 minutes."
	if rows[1]["ok"] != false || rows[1]["error"] != "timed out waiting for the browser to complete sign-in" {
		t.Fatalf("last line = %v", rows[1])
	}
	if auth.ReadCache() != nil {
		t.Fatalf("a timed-out sign-in wrote a credential")
	}
}

func TestLoginJSONUnreachableInstanceFailsBeforeAnyURL(t *testing.T) {
	loginEnv(t)
	f := newFakeHandoff(t, 0)
	addr := f.srv.URL
	f.srv.Close()
	var out, errOut bytes.Buffer
	opened := false
	err := runLogin(context.Background(), &UI{Out: &out, Err: &errOut}, loginArgs{URL: addr, JSON: true}, auth.LoginOptions{
		OpenBrowser:  func(string) error { opened = true; return nil },
		PollInterval: -1,
	})
	var unreachable *instance.UnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("err = %v, want unreachable", err)
	}
	rows := jsonLines(t, out.String())
	if len(rows) != 1 || rows[0]["ok"] != false || opened {
		t.Fatalf("lines = %q, opened %v", out.String(), opened)
	}
	msg, _ := rows[0]["error"].(string)
	if !strings.HasPrefix(msg, "Could not reach Landfall at "+addr) || strings.Contains(msg, "\n") {
		t.Fatalf("error = %q, want the CLI's own sentence on one line", msg)
	}
}

// TestLoginWithoutJSONPrintsNothingOnStdout: the plain command is unchanged.
func TestLoginWithoutJSONPrintsNothingOnStdout(t *testing.T) {
	loginEnv(t)
	f := newFakeHandoff(t, 0)
	var out, errOut bytes.Buffer
	if err := runLogin(context.Background(), &UI{Out: &out, Err: &errOut}, loginArgs{URL: f.srv.URL}, auth.LoginOptions{
		OpenBrowser: func(string) error { return nil }, PollInterval: -1,
	}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", out.String())
	}
	if !strings.Contains(errOut.String(), "signed in") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// TestLoginJSONFlagsThroughTheCommand runs `landfall login --json --host
// claude-code --url <dead address>` through the real argument handling: the
// flags parse, stdout is one JSON failure line, and the exit code and stderr
// are the plain command's for an unreachable instance. Nothing is opened.
func TestLoginJSONFlagsThroughTheCommand(t *testing.T) {
	loginEnv(t)
	f := newFakeHandoff(t, 0)
	addr := f.srv.URL
	f.srv.Close()
	var out, errOut bytes.Buffer
	code := run(&UI{Out: &out, Err: &errOut}, []string{"login", "--json", "--host", "claude-code", "--url", addr}, func(string) string { return "" })
	if code != instance.ExitUnreachable {
		t.Fatalf("exit %d, want %d", code, instance.ExitUnreachable)
	}
	rows := jsonLines(t, out.String())
	if len(rows) != 1 || rows[0]["ok"] != false {
		t.Fatalf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "Could not reach Landfall at "+addr) {
		t.Fatalf("stderr = %q", errOut.String())
	}
}
