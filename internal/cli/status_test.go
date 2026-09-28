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
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/session"
	"github.com/landfalls-ai/landfall-cli/internal/spool"
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
	return hooks.SocketResponse{OK: true, IncidentID: incidentID, IncidentDisplayID: "Acme 42", Pending: pending, VotesAwaited: votes}
}

// -------------------------------------------------------------- FormatStatusLine

func TestFormatStatusLineRendersIncidentAndCountsOmittingZeroSegments(t *testing.T) {
	s := status("inc-1", 3, 1)
	if got := FormatStatusLine(&s); got != "🔴 Acme 42 · 3 new · 1 vote awaited" {
		t.Fatalf("got %q", got)
	}
	z := status("inc-1", 0, 0)
	if got := FormatStatusLine(&z); got != "🔴 Acme 42" {
		t.Fatalf("got %q", got)
	}
	p := status("inc-1", 1, 2)
	if got := FormatStatusLine(&p); got != "🔴 Acme 42 · 1 new · 2 votes awaited" {
		t.Fatalf("got %q", got)
	}
}

// TestFormatStatusLineNeverShowsTheIncidentUUID: the room is named by its
// display id, else its title, else not at all; the UUID is nobody's name for it.
func TestFormatStatusLineNeverShowsTheIncidentUUID(t *testing.T) {
	const uuid = "8a0c2f4e-1b7d-4c55-9f3a-2d6e8b1c0a77"
	for _, tc := range []struct {
		displayID, title, want string
	}{
		{"Landfall 163", "Checkout 5xx spike", "🔴 Landfall 163 · 2 new"},
		{"", "Checkout 5xx spike", "🔴 landfall: Checkout 5xx spike · 2 new"},
		{"", "", "🔴 landfall · 2 new"},
		{"", "A title long enough that it would push everything else off the status line", "🔴 landfall: A title long enough that it would push… · 2 new"},
	} {
		s := hooks.SocketResponse{OK: true, IncidentID: uuid, IncidentDisplayID: tc.displayID, IncidentTitle: tc.title, Pending: 2}
		got := FormatStatusLine(&s)
		if got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
		if strings.Contains(got, uuid) {
			t.Errorf("the UUID is on the status line: %q", got)
		}
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
	if got := FormatStatusLine(&s); got != "🔴 Acme 42" {
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
	if out.String() != "🔴 Acme 42" {
		t.Fatalf("got %q", out.String())
	}
	if strings.Contains(out.String(), "\n") {
		t.Fatalf("a statusline command must write one line with no trailing newline, got %q", out.String())
	}
}

// TestRunStatusCountsSharesTheRoomRefused: the person sees, where they are
// working, that a share their agent was told went out did not reach the room.
func TestRunStatusCountsSharesTheRoomRefused(t *testing.T) {
	ws := withWorkspace(t)
	ws.Env["XDG_STATE_HOME"] = ws.Cwd
	sp, err := spool.Open(ws.Getenv, hooks.WorkspaceKey(ws.Dir()))
	if err != nil {
		t.Fatalf("spool.Open: %v", err)
	}
	for _, text := range []string{"the root cause is the rollback", "chart: p99"} {
		e, aerr := sp.Accept("inc-1", "a-1", text, nil)
		if aerr != nil {
			t.Fatalf("Accept: %v", aerr)
		}
		if rerr := sp.Refuse("inc-1", e.ID, 400, "statement required"); rerr != nil {
			t.Fatalf("Refuse: %v", rerr)
		}
	}
	// Another room's refusal is not this line's business.
	other, _ := sp.Accept("inc-2", "a-1", "elsewhere", nil)
	_ = sp.Refuse("inc-2", other.ID, 400, "no")

	WriteStatusCache(status("inc-1", 0, 0), ws)
	plantDeadSocket(t, ws)

	var out bytes.Buffer
	if code := RunStatus(&UI{Out: &out, Err: io.Discard}, ws); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.HasSuffix(out.String(), " · 2 shares refused") {
		t.Fatalf("got %q, want the two refusals counted", out.String())
	}

	if got := spoolSuffix(ws, []string{"inc-1"}, time.Now().Add(spool.RefusedWindow+time.Minute)); got != "" {
		t.Fatalf("an old refusal still shows: %q", got)
	}
	if got := spoolSuffix(ws, []string{"inc-2"}, time.Now()); got != " · 1 share refused" {
		t.Fatalf("one refusal = %q", got)
	}
}

// TestRunStatusSaysTheRoomSessionExpired: after 8 hours the room answers 401
// to every share, and only the person can fix that, by rejoining with a new
// link. The line says so, with how many shares are waiting on it.
func TestRunStatusSaysTheRoomSessionExpired(t *testing.T) {
	ws := withWorkspace(t)
	ws.Env["XDG_STATE_HOME"] = ws.Cwd
	sp, err := spool.Open(ws.Getenv, hooks.WorkspaceKey(ws.Dir()))
	if err != nil {
		t.Fatalf("spool.Open: %v", err)
	}
	first, _ := sp.Accept("inc-1", "a-1", "origin returned 502", nil)
	_, _ = sp.Accept("inc-1", "a-1", "latency spiked at 14:02", nil)
	if got := spoolSuffix(ws, []string{"inc-1"}, time.Now()); got != "" {
		t.Fatalf("shares merely queued already read as a problem: %q", got)
	}
	_ = sp.Claim("inc-1", first.ID)
	if xerr := sp.Expire("inc-1", first.ID, "HTTP 401"); xerr != nil {
		t.Fatalf("Expire: %v", xerr)
	}

	WriteStatusCache(status("inc-1", 0, 0), ws)
	plantDeadSocket(t, ws)

	var out bytes.Buffer
	if code := RunStatus(&UI{Out: &out, Err: io.Discard}, ws); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.HasSuffix(out.String(), " · session expired, 2 shares waiting: rejoin with a new link") {
		t.Fatalf("got %q", out.String())
	}
	if strings.Contains(out.String(), "refused") {
		t.Fatalf("an expired session reads as a refusal: %q", out.String())
	}
}

// TestTheStatusLineNamesTheRoomOverARealSocket: end to end through serve's
// own socket, the name a frame read gave the session is what the line shows.
func TestTheStatusLineNamesTheRoomOverARealSocket(t *testing.T) {
	ws := withWorkspace(t)
	s := session.New(session.Options{
		Client: client.New(client.Config{IncidentID: "8a0c2f4e-1b7d-4c55-9f3a-2d6e8b1c0a77", Slug: "acme"}, nil),
	})
	s.NoteFrame(&client.ContextFrame{Incident: client.Incident{DisplayID: "Acme 7", Title: "Checkout 5xx"}})
	bound := hooks.StartHookSocket(context.Background(), s, hooks.StartOptions{Workspace: ws, PID: 2})
	if bound == nil {
		t.Fatal("socket bind is expected to succeed in this environment")
	}
	defer func() { _ = bound.Close() }()

	if got := FormatStatusLine(QueryStatus(ws)); got != "🔴 Acme 7" {
		t.Fatalf("got %q", got)
	}
}
