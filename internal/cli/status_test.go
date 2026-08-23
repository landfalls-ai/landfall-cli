// status_test.go — a port of `test/status.test.mjs` (11 cases), feature
// 20260812-010632 (US5/T048, T051).
//
// A genuine StartHookSocket+SendToSocket round trip works standalone here, so
// the success path binds a real socket. The fallback/absent-session paths
// deliberately do NOT need one: "no socket at all" needs only a directory read,
// and "a socket exists but nothing answers" is reproduced with a plain file at
// the expected socket path, which fails to dial fast without anything needing to
// be listening — smaller and more deterministic than racing a real server's
// shutdown timing.
package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// withWorkspace builds an isolated workspace under /tmp — short enough that a
// real socket path stays inside the 104-byte sockaddr_un.sun_path limit, which
// the default macOS $TMPDIR does not.
func withWorkspace(t *testing.T) hooks.Workspace {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "lf-status-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return hooks.Workspace{Cwd: root, Env: map[string]string{"XDG_RUNTIME_DIR": root}}
}

// plantDeadSocket puts a file at a socket's expected path that answers no dial
// at all — enough for ListHookSockets to see it, not enough for anything to
// answer.
func plantDeadSocket(t *testing.T, ws hooks.Workspace) string {
	t.Helper()
	loc := hooks.SocketLocationFor(ws)
	if err := os.MkdirAll(loc.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := loc.PathFor(loc.NameFor("999999"))
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func status(incidentID string, pending, votes int) hooks.SocketResponse {
	return hooks.SocketResponse{OK: true, IncidentID: incidentID, Pending: pending, VotesAwaited: votes}
}

// -------------------------------------------------------------- FormatStatusLine

func TestFormatStatusLineRendersIncidentAndCountsOmittingZeroSegments(t *testing.T) {
	s := status("inc-1", 3, 1)
	if got := FormatStatusLine(&s); got != "🔴 landfall #inc-1 · 3 new · 1 vote awaited" {
		t.Fatalf("got %q", got)
	}
	z := status("inc-1", 0, 0)
	if got := FormatStatusLine(&z); got != "🔴 landfall #inc-1" {
		t.Fatalf("got %q", got)
	}
	p := status("inc-1", 1, 2)
	if got := FormatStatusLine(&p); got != "🔴 landfall #inc-1 · 1 new · 2 votes awaited" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatStatusLineRendersTheDivergenceSegmentWhenPresent(t *testing.T) {
	s := status("inc-1", 0, 0)
	s.Divergence = &client.Divergence{
		Diverging:          true,
		EstablishedSubject: "cli-handoff/redeem",
		ObservedSubject:    "cloudfront/5xxerrorrate",
	}
	line := FormatStatusLine(&s)
	if !strings.Contains(line, "diverging from established root cause") || !strings.Contains(line, "cli-handoff/redeem") {
		t.Fatalf("got %q", line)
	}
}

func TestFormatStatusLineIgnoresANonDivergingAnswer(t *testing.T) {
	s := status("inc-1", 0, 0)
	s.Divergence = &client.Divergence{Diverging: false}
	if got := FormatStatusLine(&s); got != "🔴 landfall #inc-1" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatStatusLineReturnsEmptyForNilSoTheCallerPrintsNothing(t *testing.T) {
	if got := FormatStatusLine(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

// -------------------------------------------------------------- QueryStatus

func TestQueryStatusAnswersNilWhenServeIsNotRunning(t *testing.T) {
	if got := QueryStatus(withWorkspace(t)); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

func TestQueryStatusNeverReadsAStaleCacheWhenNothingIsRunning(t *testing.T) {
	ws := withWorkspace(t)
	// A cache exists from an earlier, now-ended session...
	WriteStatusCache(status("inc-old", 5, 0), ws)
	// ...but there is no socket directory at all — serve is not running now, and
	// a closed terminal's incident must not bleed into a blank statusline.
	if got := QueryStatus(ws); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

func TestQueryStatusFallsBackToTheCacheWhenASocketExistsButNothingAnswers(t *testing.T) {
	ws := withWorkspace(t)
	WriteStatusCache(status("inc-1", 2, 1), ws)
	plantDeadSocket(t, ws)

	got := QueryStatus(ws)
	if got == nil || got.IncidentID != "inc-1" || got.Pending != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestQueryStatusAnswersNilWhenNothingAnswersAndThereIsNoCacheEither(t *testing.T) {
	ws := withWorkspace(t)
	plantDeadSocket(t, ws)
	if got := QueryStatus(ws); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

func TestQueryStatusAnswersARealSocketAndCachesWhatItGot(t *testing.T) {
	ws := withWorkspace(t)
	s := session.New(session.Options{
		Client: client.New(client.Config{IncidentID: "inc-real", Slug: "acme"}, nil),
	})
	s.SetAttention(&client.Attention{VotesAwaited: []client.VoteAwaited{{}}})
	s.SetAttentionDirty(false)
	one, two := int64(1), int64(2)
	s.EnqueueEvent(context.Background(), client.Event{Seq: &one, Type: "edge.finding"})
	s.EnqueueEvent(context.Background(), client.Event{Seq: &two, Type: "edge.finding"})

	// A short pid, deliberately: the sockaddr_un.sun_path limit makes a long
	// socket filename the difference between a bound socket and a silent
	// failure.
	bound := hooks.StartHookSocket(context.Background(), s, hooks.StartOptions{Workspace: ws, PID: 1})
	if bound == nil {
		t.Fatal("socket bind is expected to succeed in this environment")
	}
	defer func() { _ = bound.Close() }()

	got := QueryStatus(ws)
	if got == nil || got.IncidentID != "inc-real" || got.Pending != 2 || got.VotesAwaited != 1 {
		t.Fatalf("got %+v", got)
	}
	// The real answer was CACHED, not just returned.
	cached := ReadStatusCache(ws)
	if cached == nil || cached.IncidentID != "inc-real" {
		t.Fatalf("cached %+v", cached)
	}
}

func TestStatusCacheRoundTripsAndIsUnreadableSafe(t *testing.T) {
	ws := withWorkspace(t)
	if got := ReadStatusCache(ws); got != nil {
		t.Fatalf("nothing written yet, got %+v", got)
	}
	WriteStatusCache(status("inc-9", 0, 0), ws)
	got := ReadStatusCache(ws)
	if got == nil || got.IncidentID != "inc-9" {
		t.Fatalf("got %+v", got)
	}
}

func TestReadStatusCacheAnswersNilForGarbageRatherThanFailing(t *testing.T) {
	ws := withWorkspace(t)
	WriteStatusCache(status("inc-9", 0, 0), ws)
	if err := os.WriteFile(statusCachePath(ws), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadStatusCache(ws); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

// -------------------------------------------------- RunStatus (the command itself)

func TestRunStatusWritesNothingAndSucceedsWhenServeIsNotRunning(t *testing.T) {
	var out bytes.Buffer
	ui := &UI{Out: &out, Err: io.Discard}
	if code := RunStatus(ui, withWorkspace(t)); code != 0 {
		t.Fatalf("exit code %d, want 0 — a statusline must never surface an error", code)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %q, want silence", out.String())
	}
}

func TestRunStatusWritesExactlyOneLineWhenThereIsSomethingToSay(t *testing.T) {
	ws := withWorkspace(t)
	WriteStatusCache(status("inc-1", 0, 0), ws)
	plantDeadSocket(t, ws) // exists but unreachable — the fallback path, end to end

	var out bytes.Buffer
	ui := &UI{Out: &out, Err: io.Discard}
	if code := RunStatus(ui, ws); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if !strings.Contains(out.String(), "inc-1") {
		t.Fatalf("got %q", out.String())
	}
	if strings.Contains(out.String(), "\n") {
		t.Fatalf("a statusline command must write one line with no trailing newline, got %q", out.String())
	}
}
