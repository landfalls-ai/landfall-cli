package bridge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/mirror"
	"github.com/landfalls-ai/landfall-cli/internal/spool"
)

// fakePub is the room, without a server.
type fakePub struct {
	mu sync.Mutex

	instanceID string
	published  []map[string]any
	failWith   error
	events     []client.Event
	updateErr  error
	updateN    int
	beats      []string
	beatErr    error
	staged     []map[string]any
}

// serverContributionKinds mirrors the real server's allow-list. The fake
// REJECTS anything else, deliberately.
//
// An earlier version accepted any string, and that permissiveness is exactly
// why a live run was the first thing to notice the worker sending kind="claim"
// — which the server answers with HTTP 400. A fake looser than the thing it
// stands in for does not test the contract, it tests itself.
var serverContributionKinds = map[string]bool{
	"finding": true, "note": true, "query": true,
	"hypothesis": true, "action": true, "widget": true,
}

func (f *fakePub) Contribute(_ context.Context, kind string, body map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	if !serverContributionKinds[kind] {
		return fmt.Errorf("/edge/contributions -> HTTP 400: unknown contribution kind %q", kind)
	}
	f.published = append(f.published, body)
	return nil
}

// StageClaim is the claims endpoint — separate from contributions.
func (f *fakePub) StageClaim(_ context.Context, body map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	f.staged = append(f.staged, body)
	return nil
}

func (f *fakePub) stagedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.staged)
}

func (f *fakePub) GetUpdates(_ context.Context, sinceSeq int64) ([]client.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateN++
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	var out []client.Event
	for _, e := range f.events {
		if e.Seq != nil && *e.Seq > sinceSeq {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakePub) Heartbeat(_ context.Context, doing string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beats = append(f.beats, doing)
	return f.beatErr
}

// beatsSaid returns every activity line narrated so far.
func (f *fakePub) beatsSaid() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.beats...)
}

func (f *fakePub) AgentInstanceID() string { return f.instanceID }

func (f *fakePub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

func (f *fakePub) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.published {
		if s, ok := p["text"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func seq(n int64) *int64 { return &n }

func rig(t *testing.T) (*spool.Spool, *mirror.Mirror, string) {
	t.Helper()
	base := t.TempDir()
	getenv := func(k string) string {
		if k == "XDG_STATE_HOME" {
			return base
		}
		return ""
	}
	sp, err := spool.Open(getenv, "ws")
	if err != nil {
		t.Fatalf("spool.Open: %v", err)
	}
	mi, err := mirror.Open(filepath.Join(base, "mirror"))
	if err != nil {
		t.Fatalf("mirror.Open: %v", err)
	}
	return sp, mi, base
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPublishesQueuedHandOffs(t *testing.T) {
	sp, mi, _ := rig(t)
	if _, err := sp.Accept("inc-1", "agent-1", "origin returned 502", nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	pub := &fakePub{instanceID: "agent-1"}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 1 }, "the hand-off to reach the room")
	if got := pub.texts()[0]; got != "origin returned 502" {
		t.Fatalf("published %q", got)
	}
}

// TestReconcileAcksWhatAlreadyLanded is the SC-004 case that matters: the
// process died AFTER the server accepted the publish but BEFORE the local ack.
// Republishing would duplicate a finding in the room.
func TestReconcileAcksWhatAlreadyLanded(t *testing.T) {
	sp, mi, _ := rig(t)

	e, _ := sp.Accept("inc-1", "agent-1", "the finding that landed", nil)
	_ = sp.Claim("inc-1", e.ID) // claimed, then the process died

	// The room already has it — the mirror will see it on the next pull.
	pub := &fakePub{
		instanceID: "agent-1",
		events: []client.Event{{
			Seq:     seq(9),
			Type:    "finding",
			Payload: map[string]any{"agentInstanceId": "agent-1", "text": "the finding that landed"},
		}},
	}

	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool {
		pending, _ := sp.Pending("inc-1")
		return len(pending) == 0
	}, "the entry to be reconciled")

	if n := pub.count(); n != 0 {
		t.Fatalf("republished %d time(s) something the room already had — that is the duplicate SC-004 forbids", n)
	}
}

// TestReconcileRepublishesWhatNeverLanded is the same crash, opposite outcome.
// Acking here would silently lose the responder's finding.
func TestReconcileRepublishesWhatNeverLanded(t *testing.T) {
	sp, mi, _ := rig(t)

	e, _ := sp.Accept("inc-1", "agent-1", "never made it out", nil)
	_ = sp.Claim("inc-1", e.ID)

	pub := &fakePub{instanceID: "agent-1"} // room has nothing

	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 1 }, "the lost hand-off to be republished")
	if got := pub.texts()[0]; got != "never made it out" {
		t.Fatalf("republished %q", got)
	}
}

// TestDrainsWhileAgentIsIdle — FR-002c. The worker must not depend on the
// agent making tool calls. If it did, an idle session's queue would fill and
// the prompt-submit / Stop tiers would silently become the delivery path,
// which is the failure the whole D8 redesign exists to prevent.
func TestDrainsWhileAgentIsIdle(t *testing.T) {
	sp, mi, _ := rig(t)
	pub := &fakePub{instanceID: "agent-1"}

	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	// Accept AFTER the worker is running, and never touch it again — no nudge,
	// no tool call, nothing.
	waitFor(t, func() bool { return pub.updateCount() > 0 }, "the first sweep")
	if _, err := sp.Accept("inc-1", "agent-1", "queued while idle", nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	waitFor(t, func() bool { return pub.count() == 1 }, "the idle queue to drain on its own")
}

func TestNudgeDrainsWithoutWaitingForTheTicker(t *testing.T) {
	sp, mi, _ := rig(t)
	pub := &fakePub{instanceID: "agent-1"}

	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	_, _ = sp.Accept("inc-1", "agent-1", "nudge me", nil)
	w.Nudge()

	// DrainInterval is seconds; this must land well inside that.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if pub.count() == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Nudge did not wake the worker; a live push would wait out the full DrainInterval")
}

// TestFailedPublishIsRequeuedNotLost — a transient network error must never
// consume a responder's finding.
func TestFailedPublishIsRequeuedNotLost(t *testing.T) {
	sp, mi, _ := rig(t)
	_, _ = sp.Accept("inc-1", "agent-1", "survives a refusal", nil)

	pub := &fakePub{instanceID: "agent-1", failWith: errors.New("connection refused")}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})

	waitFor(t, func() bool {
		pending, _ := sp.Pending("inc-1")
		return len(pending) == 1 && pending[0].Attempts > 0
	}, "the failed entry to be requeued with a raised attempt count")
	w.Stop()

	pending, _ := sp.Pending("inc-1")
	if pending[0].LastError == "" {
		t.Error("no LastError recorded; the operator cannot see why it is stuck")
	}
}

// TestStartStopsThePreviousRun — join_war_room can re-target mid-process.
// A worker still running for room A would publish into a room the responder
// has left.
func TestStartStopsThePreviousRun(t *testing.T) {
	sp, mi, _ := rig(t)
	pubA := &fakePub{instanceID: "agent-1"}
	pubB := &fakePub{instanceID: "agent-1"}

	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Start(ctx, pubA, client.Config{IncidentID: "room-A"})
	waitFor(t, func() bool { return pubA.updateCount() > 0 }, "room A's worker to run")

	w.Start(ctx, pubB, client.Config{IncidentID: "room-B"})
	waitFor(t, func() bool { return pubB.updateCount() > 0 }, "room B's worker to run")
	defer w.Stop()

	before := pubA.updateCount()
	time.Sleep(200 * time.Millisecond)
	if after := pubA.updateCount(); after > before {
		t.Fatalf("room A's worker kept running after re-target (%d -> %d)", before, after)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	sp, mi, _ := rig(t)
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	w.Stop() // never started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, &fakePub{}, client.Config{IncidentID: "inc-1"})
	w.Stop()
	w.Stop()
}

// TestUnreachableRoomIsSilent — best-effort by design. A room we cannot reach
// must not surface anything to a tool caller mid-investigation.
func TestUnreachableRoomIsSilent(t *testing.T) {
	sp, mi, _ := rig(t)
	pub := &fakePub{instanceID: "agent-1", updateErr: errors.New("no route to host")}

	var logged int
	var mu sync.Mutex
	w := New(sp, mi, func(string, ...any) { mu.Lock(); logged++; mu.Unlock() })
	w.Interval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	waitFor(t, func() bool { return pub.updateCount() > 1 }, "a couple of sweeps")
	w.Stop()

	mu.Lock()
	defer mu.Unlock()
	if logged != 0 {
		t.Fatalf("an unreachable room produced %d log lines; it should change nothing", logged)
	}
}

func TestAbandonReportsWhatItDropped(t *testing.T) {
	sp, mi, _ := rig(t)
	for i := 0; i < 2; i++ {
		_, _ = sp.Accept("inc-1", "agent-1", "stranded", nil)
	}

	var lines []string
	var mu sync.Mutex
	w := New(sp, mi, func(f string, a ...any) { mu.Lock(); lines = append(lines, f); mu.Unlock() })
	w.Abandon("inc-1")

	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 {
		t.Fatal("abandoning hand-offs was silent; the responder would believe they reached the room")
	}
}

func (f *fakePub) updateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updateN
}
