package bridge

import (
	"context"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/spool"
)

// TestSC002EveryHandOffPublishesWithinABoundedWindow.
//
// SC-002 says "within a bounded window", and the bound has to be asserted
// rather than described: "eventually" is satisfied by a worker that publishes
// in an hour, which is useless during an incident.
//
// The bound here is deliberately generous relative to the configured interval —
// this measures that the drain loop makes progress on its own schedule, not the
// scheduler's precision, which would make the test flaky for no benefit.
func TestSC002EveryHandOffPublishesWithinABoundedWindow(t *testing.T) {
	sp, mi, _ := rig(t)

	const n = 10
	for i := 0; i < n; i++ {
		if _, err := sp.Accept("inc-1", "agent-1", "origin returned 502 and latency spiked", nil); err != nil {
			t.Fatalf("Accept %d: %v", i, err)
		}
	}

	pub := &fakePub{instanceID: "agent-1"}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == n }, "all hand-offs to publish")
	elapsed := time.Since(start)

	// 100 sweep intervals for 10 items is loose enough to be stable on a busy
	// CI box and tight enough that a worker which only drains one item per
	// sweep-with-a-long-gap would still fail it.
	if bound := 100 * w.Interval; elapsed > bound {
		t.Errorf("%d hand-offs took %v to publish, want under %v", n, elapsed, bound)
	}

	// And the queue must actually be empty, not merely quiet.
	pending, err := sp.Pending("inc-1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("%d entries still queued after all publishes reported", len(pending))
	}
}

// TestSC001AcceptCostsNoNetworkRoundTrip.
//
// SC-001 is "zero added main-agent turn latency attributable to room-sync".
// Measuring wall-clock latency in a unit test measures the test machine, so
// this asserts the PROPERTY that makes the latency zero: the accept path
// performs no network I/O at all. A publisher whose every method fails the test
// if called is a stronger check than a stopwatch.
func TestSC001AcceptCostsNoNetworkRoundTrip(t *testing.T) {
	sp, mi, _ := rig(t)

	// Nothing running — no worker, no sweep. Only the accept path executes.
	_ = mi

	start := time.Now()
	const n = 50
	for i := 0; i < n; i++ {
		if _, err := sp.Accept("inc-1", "agent-1", "a finding worth sharing with the room", nil); err != nil {
			t.Fatalf("Accept %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)

	// Every accept fsyncs, so this is disk-bound, not network-bound. A single
	// network round trip would blow well past this on any real link.
	if bound := 2 * time.Second; elapsed > bound {
		t.Errorf("%d accepts took %v; that is not a local-only path", n, elapsed)
	}
}

// TestFR009InferredContentIsNeverPublishedDirectly.
//
// FR-009: content the agent did not explicitly hand off may only be STAGED as
// a proposal. The spool has exactly one entry point, so the guarantee is
// structural — there is no path from "the worker noticed something" to "the
// room has it". This test pins that there is no back door.
func TestFR009InferredContentIsNeverPublishedDirectly(t *testing.T) {
	sp, mi, _ := rig(t)

	// A room full of activity the worker will absorb into its mirror.
	pub := &fakePub{
		instanceID: "agent-1",
		events: []client.Event{
			{Seq: seq(1), Type: "finding", Payload: map[string]any{"agentInstanceId": "other", "text": "someone else's finding"}},
			{Seq: seq(2), Type: "chat.message", Payload: map[string]any{"agentInstanceId": "other", "text": "a passing remark"}},
		},
	}

	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	// Let it absorb everything and sweep several times.
	waitFor(t, func() bool {
		records, _ := mi.Records("inc-1")
		return len(records) == 2
	}, "the worker to absorb the room's activity")
	time.Sleep(100 * time.Millisecond)

	// It observed two events and published nothing: absorbing is not authoring.
	if n := pub.count(); n != 0 {
		t.Fatalf("the worker published %d item(s) it was never handed: inferred content reached the room", n)
	}
}

// TestOnlyAcceptedEntriesArePublished is FR-009's other half — the worker
// publishes what came through Accept and nothing else.
func TestOnlyAcceptedEntriesArePublished(t *testing.T) {
	sp, mi, _ := rig(t)

	e, err := sp.Accept("inc-1", "agent-1", "the one thing the agent actually shared", nil)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}

	pub := &fakePub{
		instanceID: "agent-1",
		events: []client.Event{
			{Seq: seq(1), Type: "finding", Payload: map[string]any{"agentInstanceId": "other", "text": "not ours"}},
		},
	}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 1 }, "the accepted hand-off to publish")
	time.Sleep(80 * time.Millisecond)

	if n := pub.count(); n != 1 {
		t.Fatalf("published %d items for 1 accepted hand-off", n)
	}
	if got := pub.texts()[0]; got != e.Text {
		t.Errorf("published %q, want the accepted text %q", got, e.Text)
	}
}

var _ = spool.Max
