package mirror

import (
	"path/filepath"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func open(t *testing.T) *Mirror {
	t.Helper()
	m, err := Open(filepath.Join(t.TempDir(), "mirror"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m
}

func seq(n int64) *int64 { return &n }

func ev(n int64, typ, agent, text string) client.Event {
	return client.Event{
		Seq:  seq(n),
		Type: typ,
		Payload: map[string]any{
			"agentInstanceId": agent,
			"text":            text,
		},
	}
}

// TestReconcilesAfterCrashWindow is the whole reason this package exists.
//
// The window: the server accepted the publish, then the process died before
// the spool ack was written. On restart the entry's outcome is unknown.
// Republishing duplicates it in the room; acking loses it. The mirror is what
// turns "unknown" into an answer.
func TestReconcilesAfterCrashWindow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mirror")

	m1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// The publish landed and we observed it, THEN the process died.
	if err := m1.Record("inc-1", []client.Event{
		ev(7, "finding", "agent-1", "origin returned 502"),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	m2, err := Open(dir) // fresh process
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	landed, err := m2.FindOwn("inc-1", "agent-1", "origin returned 502", 0)
	if err != nil {
		t.Fatalf("FindOwn: %v", err)
	}
	if !landed {
		t.Fatal("reconciliation missed an event that DID land: the worker would republish it and duplicate the finding")
	}
}

func TestDoesNotClaimSomethingNeverPublished(t *testing.T) {
	m := open(t)
	if err := m.Record("inc-1", []client.Event{
		ev(7, "finding", "agent-1", "a different finding"),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	landed, err := m.FindOwn("inc-1", "agent-1", "the one that never sent", 0)
	if err != nil {
		t.Fatalf("FindOwn: %v", err)
	}
	if landed {
		t.Fatal("reconciliation claimed an unpublished entry had landed: the finding would be acked and silently lost")
	}
}

// TestDoesNotMatchAnotherAgentsEvent — two investigators can post the same
// text. Matching on content alone would let one responder's publish ack
// another's queued entry.
func TestDoesNotMatchAnotherAgentsEvent(t *testing.T) {
	m := open(t)
	if err := m.Record("inc-1", []client.Event{
		ev(7, "finding", "agent-OTHER", "origin returned 502"),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	landed, _ := m.FindOwn("inc-1", "agent-1", "origin returned 502", 0)
	if landed {
		t.Fatal("matched another agent's event: our finding would be dropped as already-sent")
	}
}

// TestRefusesToGuessOnEmptyKey — an empty agent id or text must never match.
// Before POST /edge/join returns, the instance id is empty; a match on that
// would ack an arbitrary entry.
func TestRefusesToGuessOnEmptyKey(t *testing.T) {
	m := open(t)

	// Record events that WOULD match an empty key, so the guard is what stops
	// it rather than the absence of a candidate. (An earlier version of this
	// test recorded a fully-empty event and passed even with the guard
	// removed — it was asserting nothing. Mutation testing caught that.)
	_ = m.Record("inc-1", []client.Event{
		ev(7, "finding", "", "a human posted this, no agent id"),
		ev(8, "finding", "agent-1", ""),
	})

	// Before POST /edge/join returns, our own instance id is empty. Matching on
	// it would ack an arbitrary human-authored event.
	if landed, _ := m.FindOwn("inc-1", "", "a human posted this, no agent id", 0); landed {
		t.Error("matched on an empty agent instance id: an un-joined session would ack someone else's event")
	}
	// An entry with no text cannot be identified by content.
	if landed, _ := m.FindOwn("inc-1", "agent-1", "", 0); landed {
		t.Error("matched on empty text: any event by us would satisfy any entry")
	}
}

// TestMinSeqBoundsTheSearch — an entry queued now must not be satisfied by an
// identical event from before it was ever accepted.
func TestMinSeqBoundsTheSearch(t *testing.T) {
	m := open(t)
	if err := m.Record("inc-1", []client.Event{
		ev(3, "finding", "agent-1", "recurring observation"),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	landed, _ := m.FindOwn("inc-1", "agent-1", "recurring observation", 10)
	if landed {
		t.Fatal("an event older than the entry satisfied reconciliation: a genuinely new finding would never be published")
	}
}

func TestRecordIsIdempotentBySeq(t *testing.T) {
	m := open(t)
	batch := []client.Event{ev(1, "finding", "agent-1", "once")}

	for i := 0; i < 3; i++ {
		if err := m.Record("inc-1", batch); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}
	records, err := m.Records("inc-1")
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("recorded %d copies, want 1 — a replayed GetUpdates window must not duplicate the mirror", len(records))
	}
}

func TestLastSeqDrivesTheCursor(t *testing.T) {
	m := open(t)
	if got, _ := m.LastSeq("inc-1"); got != 0 {
		t.Fatalf("empty mirror LastSeq = %d, want 0", got)
	}
	_ = m.Record("inc-1", []client.Event{
		ev(4, "finding", "a", "x"),
		ev(9, "finding", "a", "y"),
		ev(6, "finding", "a", "z"),
	})
	if got, _ := m.LastSeq("inc-1"); got != 9 {
		t.Fatalf("LastSeq = %d, want 9", got)
	}
}

func TestEventsWithoutSeqAreIgnored(t *testing.T) {
	m := open(t)
	if err := m.Record("inc-1", []client.Event{
		{Type: "finding", Payload: map[string]any{"agentInstanceId": "a", "text": "no seq"}},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	records, _ := m.Records("inc-1")
	if len(records) != 0 {
		t.Fatal("recorded an event with no seq: it has no timeline position to reconcile against")
	}
}

func TestPerIncidentIsolation(t *testing.T) {
	m := open(t)
	_ = m.Record("room-A", []client.Event{ev(1, "finding", "agent-1", "for A")})

	landed, _ := m.FindOwn("room-B", "agent-1", "for A", 0)
	if landed {
		t.Fatal("an event from room A satisfied reconciliation in room B")
	}
}

func TestForgetDropsStaleRoom(t *testing.T) {
	m := open(t)
	_ = m.Record("inc-1", []client.Event{ev(1, "finding", "agent-1", "old")})
	if err := m.Forget("inc-1"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if landed, _ := m.FindOwn("inc-1", "agent-1", "old", 0); landed {
		t.Fatal("a forgotten room still satisfied reconciliation")
	}
	// Forget on an absent mirror must not error — shutdown paths call it blind.
	if err := m.Forget("never-existed"); err != nil {
		t.Fatalf("Forget on missing incident: %v", err)
	}
}

func TestConcurrentRecordsAreAllDurable(t *testing.T) {
	m := open(t)
	const n = 20

	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			done <- m.Record("inc-1", []client.Event{ev(int64(i+1), "finding", "a", "concurrent")})
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent Record: %v", err)
		}
	}
	records, _ := m.Records("inc-1")
	if len(records) != n {
		t.Fatalf("recorded %d of %d concurrent events", len(records), n)
	}
}
