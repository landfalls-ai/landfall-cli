package spool

import (
	"errors"
	"testing"
	"time"
)

// TestRefuseIsTerminal: a refused entry leaves the queue for good: never
// Next, never Pending, never counted against the bound, and kept (with the
// room's reason) rather than dropped.
func TestRefuseIsTerminal(t *testing.T) {
	s := open(t)
	e, err := s.Accept("inc-1", "a-1", "the root cause is the rollback", nil)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := s.Claim("inc-1", e.ID); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := s.Refuse("inc-1", e.ID, 400, "statement required"); err != nil {
		t.Fatalf("Refuse: %v", err)
	}

	if next, _ := s.Next("inc-1"); next != nil {
		t.Fatalf("Next = %+v, want nothing: a refused entry must never be retried", next)
	}
	if pending, _ := s.Pending("inc-1"); len(pending) != 0 {
		t.Fatalf("Pending = %d, want 0", len(pending))
	}
	refused, err := s.Refusals("inc-1")
	if err != nil || len(refused) != 1 {
		t.Fatalf("Refusals = %v, %v", refused, err)
	}
	got := refused[0]
	if got.State != Refused || got.Refusal != "statement required" || got.Attempts != 1 || got.RefusedAt == nil {
		t.Fatalf("refused entry = %+v", got)
	}
	if got.Text != "the root cause is the rollback" {
		t.Fatalf("text = %q: the hand-off itself must be kept", got.Text)
	}
}

// TestRefusedDoesNotCountAgainstTheBound: the bound is on work still owed to
// the room; a refused entry is not owed.
func TestRefusedDoesNotCountAgainstTheBound(t *testing.T) {
	s := open(t)
	for i := 0; i < Max; i++ {
		e, err := s.Accept("inc-1", "a-1", "x", nil)
		if err != nil {
			t.Fatalf("Accept %d: %v", i, err)
		}
		if err := s.Refuse("inc-1", e.ID, 400, "no"); err != nil {
			t.Fatalf("Refuse: %v", err)
		}
	}
	if _, err := s.Accept("inc-1", "a-1", "still room for this", nil); err != nil {
		t.Fatalf("Accept after %d refusals: %v", Max, err)
	}
}

// TestTakeUnreportedIsOnceAndSurvivesReopen: each refusal is told once, and
// "told" is durable: a restarted serve does not tell the agent again.
func TestTakeUnreportedIsOnceAndSurvivesReopen(t *testing.T) {
	getenv := env(t)
	s, err := Open(getenv, "wskey")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	a, _ := s.Accept("inc-1", "a-1", "first", nil)
	b, _ := s.Accept("inc-1", "a-1", "second", nil)
	_ = s.Refuse("inc-1", a.ID, 400, "reason a")

	took, err := s.TakeUnreported("inc-1")
	if err != nil || len(took) != 1 || took[0].Text != "first" {
		t.Fatalf("first take = %v, %v", took, err)
	}
	if again, _ := s.TakeUnreported("inc-1"); len(again) != 0 {
		t.Fatalf("second take = %v, want none", again)
	}

	_ = s.Refuse("inc-1", b.ID, 400, "reason b")
	reopened, err := Open(getenv, "wskey")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	took, _ = reopened.TakeUnreported("inc-1")
	if len(took) != 1 || took[0].Text != "second" {
		t.Fatalf("after reopen = %v, want only the untold one", took)
	}
}

// TestRecentRefusalsAgeOut: the status line counts a refusal for
// RefusedWindow, then lets it go.
func TestRecentRefusalsAgeOut(t *testing.T) {
	s := open(t)
	e, _ := s.Accept("inc-1", "a-1", "x", nil)
	_ = s.Refuse("inc-1", e.ID, 400, "no")

	now := time.Now()
	if n, err := s.RecentRefusals("inc-1", now); err != nil || n != 1 {
		t.Fatalf("RecentRefusals now = %d, %v; want 1", n, err)
	}
	// Told to the agent or not, it still counts inside the window.
	_, _ = s.TakeUnreported("inc-1")
	if n, _ := s.RecentRefusals("inc-1", now); n != 1 {
		t.Fatalf("RecentRefusals after telling the agent = %d, want 1", n)
	}
	if n, _ := s.RecentRefusals("inc-1", now.Add(RefusedWindow+time.Minute)); n != 0 {
		t.Fatalf("RecentRefusals past the window = %d, want 0", n)
	}
}

// TestAbandonLeavesRefusalsAlone: leaving a room drops work still owed to
// it; a refusal is already final and already told.
func TestAbandonLeavesRefusalsAlone(t *testing.T) {
	s := open(t)
	e, _ := s.Accept("inc-1", "a-1", "x", nil)
	_ = s.Refuse("inc-1", e.ID, 400, "no")
	_, _ = s.Accept("inc-1", "a-1", "queued", nil)
	n, err := s.Abandon("inc-1")
	if err != nil || n != 1 {
		t.Fatalf("Abandon = %d, %v; want 1 (the queued one only)", n, err)
	}
	if refused, _ := s.Refusals("inc-1"); len(refused) != 1 {
		t.Fatalf("Refusals after Abandon = %d, want 1", len(refused))
	}
}

// TestAnExpiredSessionLeavesTheEntryQueuedAndMarked: a 401 is about the
// session, not the entry. It stays owed to the room (Next, Pending, the
// bound), marked so the status line and the agent can say why it waits.
func TestAnExpiredSessionLeavesTheEntryQueuedAndMarked(t *testing.T) {
	s := open(t)
	a, _ := s.Accept("inc-1", "a-1", "origin returned 502", nil)
	b, _ := s.Accept("inc-1", "a-1", "latency spiked at 14:02", nil)
	if gone, waiting, err := s.AwaitingSession("inc-1"); err != nil || gone || waiting != 2 {
		t.Fatalf("before any attempt: AwaitingSession = %v, %d, %v", gone, waiting, err)
	}
	_ = s.Claim("inc-1", a.ID)
	if err := s.Expire("inc-1", a.ID, "/edge/contributions → HTTP 401: Unauthorized"); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	next, _ := s.Next("inc-1")
	if next == nil || next.ID != a.ID || next.State != Queued || !next.SessionExpired || next.Attempts != 1 {
		t.Fatalf("Next = %+v, want the expired entry still queued, marked", next)
	}
	if refused, _ := s.Refusals("inc-1"); len(refused) != 0 {
		t.Fatal("an expired session is not a refusal")
	}
	gone, waiting, err := s.AwaitingSession("inc-1")
	if err != nil || !gone || waiting != 2 {
		t.Fatalf("AwaitingSession = %v, %d, %v; want expired, both waiting", gone, waiting, err)
	}

	// The next attempt meets something else: the mark goes with it.
	_ = s.Claim("inc-1", a.ID)
	_ = s.Fail("inc-1", a.ID, errors.New("connection refused"))
	if gone, _, _ := s.AwaitingSession("inc-1"); gone {
		t.Fatal("a later transient failure left the expiry mark in place")
	}
	_ = s.Expire("inc-1", a.ID, "401")
	_ = s.Ack("inc-1", a.ID)
	if gone, waiting, _ := s.AwaitingSession("inc-1"); gone || waiting != 1 {
		t.Fatalf("after the expired entry landed: %v, %d; want not expired, one waiting", gone, waiting)
	}
	if b.ID == a.ID {
		t.Fatal("two accepts shared an id")
	}
}

// TestARefusalKeepsTheRoomsStatus: the status decides what the agent is told
// to do about it.
func TestARefusalKeepsTheRoomsStatus(t *testing.T) {
	s := open(t)
	e, _ := s.Accept("inc-1", "a-1", "the root cause is the rollback", nil)
	_ = s.Refuse("inc-1", e.ID, 409, "engagement is closed; admission is frozen")
	refused, _ := s.Refusals("inc-1")
	if len(refused) != 1 || refused[0].RefusalStatus != 409 {
		t.Fatalf("refused = %+v", refused)
	}
}
