package spool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// env builds a getenv that points the spool at a temp dir, so no test ever
// touches the developer's real ~/.local/state.
func env(t *testing.T) func(string) string {
	t.Helper()
	base := t.TempDir()
	return func(k string) string {
		if k == "XDG_STATE_HOME" {
			return base
		}
		return ""
	}
}

func open(t *testing.T) *Spool {
	t.Helper()
	s, err := Open(env(t), "wskey")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// TestSpoolIsNotUnderRuntimeDir is the regression guard for the review finding
// that motivated this package's directory choice.
//
// internal/hooks.runtimeDir uses XDG_RUNTIME_DIR, which is tmpfs on Linux and
// wiped on reboot. A durable queue there would satisfy every restart test in
// this file and still lose the responder's findings on the first reboot — the
// exact failure FR-008 exists to prevent, invisible to a process-level test.
func TestSpoolIsNotUnderRuntimeDir(t *testing.T) {
	// Both set, as on a real Linux box: XDG_RUNTIME_DIR is tmpfs, XDG_STATE_HOME
	// is not. Resolution must pick the durable one.
	getenv := func(k string) string {
		switch k {
		case "XDG_RUNTIME_DIR":
			return "/run/user/1000"
		case "XDG_STATE_HOME":
			return "/home/u/.local/state"
		case "HOME":
			return "/home/u"
		}
		return ""
	}

	got := stateDir(getenv)
	if strings.Contains(got, "/run/") {
		t.Fatalf("stateDir = %q — resolved under a runtime dir, which is tmpfs on Linux and wiped on reboot", got)
	}
	if want := filepath.Join("/home/u/.local/state", "landfall"); got != want {
		t.Fatalf("stateDir = %q, want %q", got, want)
	}
}

func TestStateDirFallsBackToLocalState(t *testing.T) {
	getenv := func(k string) string {
		if k == "HOME" {
			return "/home/u"
		}
		return ""
	}
	want := filepath.Join("/home/u", ".local", "state", "landfall")
	if got := stateDir(getenv); got != want {
		t.Fatalf("stateDir = %q, want %q", got, want)
	}
}

func TestAcceptIsDurableImmediately(t *testing.T) {
	s := open(t)
	e, err := s.Accept("inc-1", "agent-1", "origin returned 502", nil)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}

	// Read the file directly rather than through the Spool: the point is that
	// the bytes are on disk the moment Accept returns, not that an in-memory
	// structure remembers them.
	b, err := os.ReadFile(s.path("inc-1"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(b), e.ID) || !strings.Contains(string(b), "502") {
		t.Fatalf("entry not on disk after Accept: %s", b)
	}
}

// TestSurvivesRestartAndReportsUnknownOutcome covers the window D9's
// reconciliation exists for: the process died AFTER claiming an entry, so the
// publish may or may not have landed.
//
// The spool must NOT resolve that itself. Resetting to Queued would republish
// something the room already has; acking would lose it. Returning "unknown" is
// the only honest answer, and internal/bridge decides using the local mirror.
func TestSurvivesRestartAndReportsUnknownOutcome(t *testing.T) {
	getenv := env(t)

	s1, err := Open(getenv, "wskey")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	e, err := s1.Accept("inc-1", "agent-1", "finding A", nil)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := s1.Claim("inc-1", e.ID); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// <- process dies here, mid-publish. No Ack, no Fail.

	s2, err := Open(getenv, "wskey") // a fresh process
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	unknown, err := s2.Recover("inc-1")
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(unknown) != 1 || unknown[0].ID != e.ID {
		t.Fatalf("Recover returned %d entries, want the one claimed entry", len(unknown))
	}

	// And it must not have been silently requeued behind our back.
	next, err := s2.Next("inc-1")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next != nil {
		t.Fatal("a claimed entry was requeued on restart: that republishes what the room may already have")
	}
}

func TestQueuedEntrySurvivesRestart(t *testing.T) {
	getenv := env(t)

	s1, _ := Open(getenv, "wskey")
	if _, err := s1.Accept("inc-1", "agent-1", "finding B", nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	s2, _ := Open(getenv, "wskey")
	next, err := s2.Next("inc-1")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next == nil || next.Text != "finding B" {
		t.Fatal("a queued entry did not survive restart")
	}
}

// TestFullQueueRefusesRatherThanDropsOldest pins the direction reversal the
// senior review caught.
//
// session.PendingMax drops the OLDEST inbound event, which is right: the oldest
// incoming event is the stalest. Copying that here would delete the
// responder's EARLIEST finding — something a human wrote, that no retry
// recovers. Refusing the newest is recoverable; the caller can say so.
func TestFullQueueRefusesRatherThanDropsOldest(t *testing.T) {
	s := open(t)

	first, err := s.Accept("inc-1", "a", "the first finding", nil)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	for i := 1; i < Max; i++ {
		if _, err := s.Accept("inc-1", "a", "filler", nil); err != nil {
			t.Fatalf("Accept %d: %v", i, err)
		}
	}

	if _, err := s.Accept("inc-1", "a", "one too many", nil); !errors.Is(err, ErrFull) {
		t.Fatalf("Accept past Max returned %v, want ErrFull", err)
	}

	pending, err := s.Pending("inc-1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != Max {
		t.Fatalf("pending = %d, want %d", len(pending), Max)
	}
	if pending[0].ID != first.ID {
		t.Fatal("the oldest finding was evicted: outbound overflow must never destroy the responder's earliest work")
	}
}

func TestAckThenCompactRemovesPublished(t *testing.T) {
	s := open(t)
	e, _ := s.Accept("inc-1", "a", "done", nil)
	if err := s.Ack("inc-1", e.ID); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if err := s.Compact("inc-1"); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	pending, _ := s.Pending("inc-1")
	if len(pending) != 0 {
		t.Fatalf("pending = %d after ack+compact, want 0", len(pending))
	}
}

func TestFailRequeuesAndCountsAttempts(t *testing.T) {
	s := open(t)
	e, _ := s.Accept("inc-1", "a", "retry me", nil)
	_ = s.Claim("inc-1", e.ID)
	if err := s.Fail("inc-1", e.ID, errors.New("connection refused")); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	next, _ := s.Next("inc-1")
	if next == nil {
		t.Fatal("a failed entry was not requeued: a transient error must not lose a finding")
	}
	if next.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", next.Attempts)
	}
	if next.LastError == "" {
		t.Error("LastError not recorded; the operator needs to see why it is stuck")
	}
}

// TestAbandonReportsCount — FR-012's visibility principle applied to the
// room-change case. Silently discarding findings because the responder
// switched rooms is exactly the quiet failure this design forbids.
func TestAbandonReportsCount(t *testing.T) {
	s := open(t)
	for i := 0; i < 3; i++ {
		if _, err := s.Accept("inc-1", "a", "stranded", nil); err != nil {
			t.Fatalf("Accept: %v", err)
		}
	}
	n, err := s.Abandon("inc-1")
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if n != 3 {
		t.Fatalf("Abandon reported %d, want 3 — the caller cannot surface what it is not told", n)
	}
	pending, _ := s.Pending("inc-1")
	if len(pending) != 0 {
		t.Fatalf("pending = %d after abandon, want 0", len(pending))
	}
}

func TestEntriesAreScopedPerIncident(t *testing.T) {
	s := open(t)
	if _, err := s.Accept("room-A", "a", "for A", nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	next, err := s.Next("room-B")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next != nil {
		t.Fatal("an entry accepted for room A surfaced in room B")
	}
}

// TestTornTrailingLineIsDiscarded — a half-written line is a write that never
// completed, so it was never an accepted entry. It must not make the whole
// queue unreadable.
func TestTornTrailingLineIsDiscarded(t *testing.T) {
	s := open(t)
	good, _ := s.Accept("inc-1", "a", "intact", nil)

	f, err := os.OpenFile(s.path("inc-1"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.WriteString(`{"id":"torn","text":"half`)
	_ = f.Close()

	pending, err := s.Pending("inc-1")
	if err != nil {
		t.Fatalf("Pending after torn write: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != good.ID {
		t.Fatalf("torn line corrupted the queue: got %d entries", len(pending))
	}
}

func TestConcurrentAcceptsAreAllDurable(t *testing.T) {
	s := open(t)
	const n = 20

	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := s.Accept("inc-1", "a", "concurrent", nil)
			done <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent Accept: %v", err)
		}
	}

	pending, _ := s.Pending("inc-1")
	if len(pending) != n {
		t.Fatalf("pending = %d after %d concurrent accepts, want %d", len(pending), n, n)
	}
}
