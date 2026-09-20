package spool

import (
	"testing"
	"time"
)

// Held is a state the WORKER never touches: a held entry is not Queued, so a
// publish pass skips it, and only a person's command moves it.
func TestHeldEntriesAreNotPublishableAndLeaveOnlyByThePersonsHand(t *testing.T) {
	sp := open(t)
	e, err := sp.Accept("inc-1", "agent-1", "TTL_MS in src/cache.js is 60s", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.Hold("inc-1", e.ID, []string{"src/cache.js", "TTL_MS"}); err != nil {
		t.Fatalf("hold: %v", err)
	}
	held, _ := sp.Held("inc-1")
	if len(held) != 1 || held[0].State != Held || held[0].Held == nil || len(held[0].Held.Matched) != 2 {
		t.Fatalf("held listing = %+v", held)
	}
	if held[0].Held.HeldAt.IsZero() || time.Since(held[0].Held.HeldAt) > time.Minute {
		t.Fatalf("HeldAt not stamped: %v", held[0].Held.HeldAt)
	}
	// A publish pass must not see it as work.
	queued, _ := sp.Pending("inc-1")
	for _, q := range queued {
		if q.ID == e.ID {
			t.Fatal("a held entry showed up as pending work for the worker")
		}
	}
	// Release: the person allowed working-directory content for the room.
	n, err := sp.ReleaseHeld("inc-1")
	if err != nil || n != 1 {
		t.Fatalf("release = %d, %v", n, err)
	}
	queued, _ = sp.Pending("inc-1")
	if len(queued) != 1 || queued[0].ID != e.ID || queued[0].State != Queued || queued[0].Held != nil {
		t.Fatalf("after release the entry must be Queued with no hold reason: %+v", queued)
	}
}

func TestDroppingAHeldEntryAbandonsIt(t *testing.T) {
	sp := open(t)
	e, _ := sp.Accept("inc-1", "agent-1", "commit 82400b8 touched retry.js", nil)
	if err := sp.Hold("inc-1", e.ID, []string{"82400b8"}); err != nil {
		t.Fatal(err)
	}
	if err := sp.DropHeld("inc-1", e.ID); err != nil {
		t.Fatalf("drop: %v", err)
	}
	all, _ := sp.load("inc-1")
	if len(all) != 1 || all[0].State != Abandoned {
		t.Fatalf("dropped entry should be Abandoned: %+v", all)
	}
	if err := sp.DropHeld("inc-1", e.ID); err == nil {
		t.Fatal("dropping an entry that is not held must be refused")
	}
}
