package session

// Ported from `test/attention.test.mjs` (the coalescing/abandon/room-switch
// half) and `test/edge-bridge.test.mjs` (the queue half). The adversarial
// cases are the point: a quiet room costs nothing, a superseded read touches
// nothing, and a read still in flight against the OLD room can never answer
// for the new one.
//
// Every test here runs under `go test -race`; the concurrency ones use real
// goroutines and real blocking, not a sequential stand-in.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// --- fakes ------------------------------------------------------------------

// stubClient implements EdgeClient with overridable behavior; every method is
// counted so a test can assert how many reads a scenario actually cost.
type stubClient struct {
	mu sync.Mutex

	id    string
	calls map[string]int

	attentionFn  func(ctx context.Context) (*client.Attention, error)
	divergenceFn func(ctx context.Context) (*client.Divergence, error)
	deltaFn      func(ctx context.Context, since int64) (*client.FrameDelta, error)
	joinErr      error
}

func newStub(id string) *stubClient {
	return &stubClient{id: id, calls: map[string]int{}}
}

func (c *stubClient) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[name]++
	return c.calls[name]
}

func (c *stubClient) countOf(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func (c *stubClient) Join(context.Context) (*client.JoinResult, error) {
	c.count("join")
	if c.joinErr != nil {
		return nil, c.joinErr
	}
	return &client.JoinResult{AgentInstanceID: c.id}, nil
}
func (c *stubClient) Leave(context.Context) error             { c.count("leave"); return nil }
func (c *stubClient) Heartbeat(context.Context, string) error { c.count("heartbeat"); return nil }
func (c *stubClient) Contribute(context.Context, string, map[string]any) error {
	c.count("contribute")
	return nil
}
func (c *stubClient) UploadArtifact(context.Context, string, string, string) (*client.ArtifactResult, error) {
	c.count("upload")
	return &client.ArtifactResult{}, nil
}
func (c *stubClient) FlagContext(context.Context, int64, string, string) error {
	c.count("flag")
	return nil
}
func (c *stubClient) PositionClaim(context.Context, int64, string, string) error {
	c.count("position")
	return nil
}
func (c *stubClient) StageClaim(context.Context, map[string]any) error {
	c.count("stage")
	return nil
}
func (c *stubClient) GetBrief(context.Context) ([]client.Event, error) {
	c.count("brief")
	return nil, nil
}
func (c *stubClient) GetUpdates(context.Context, int64) ([]client.Event, error) {
	c.count("updates")
	return nil, nil
}
func (c *stubClient) GetContextFrame(context.Context) (*client.ContextFrame, error) {
	c.count("frame")
	return &client.ContextFrame{}, nil
}
func (c *stubClient) GetContextDelta(ctx context.Context, since int64) (*client.FrameDelta, error) {
	c.count("delta")
	if c.deltaFn != nil {
		return c.deltaFn(ctx, since)
	}
	return &client.FrameDelta{SinceVersion: since, ToVersion: &since}, nil
}
func (c *stubClient) SearchContext(context.Context, string) (*client.SearchResult, error) {
	c.count("search")
	return &client.SearchResult{}, nil
}
func (c *stubClient) GetAttention(ctx context.Context) (*client.Attention, error) {
	c.count("attention")
	if c.attentionFn != nil {
		return c.attentionFn(ctx)
	}
	return &client.Attention{}, nil
}
func (c *stubClient) GetDivergence(ctx context.Context) (*client.Divergence, error) {
	c.count("divergence")
	if c.divergenceFn != nil {
		return c.divergenceFn(ctx)
	}
	return &client.Divergence{}, nil
}
func (c *stubClient) GetSignalCatalog(context.Context) ([]client.SignalCatalogEntry, error) {
	c.count("signal_catalog")
	return nil, nil
}
func (c *stubClient) QuerySignals(context.Context, string, string, map[string]any, string, string) (client.SignalsQueryResult, error) {
	c.count("query_signals")
	return nil, nil
}
func (c *stubClient) AgentInstanceID() string { return c.id }
func (c *stubClient) Config() client.Config   { return client.Config{Slug: "acme", IncidentID: "inc-1"} }

// deferredReads hands each in-flight read its own channel, so a test can land
// them out of START order — a retry, a pool wait or a GC pause is enough to
// produce exactly this in production.
type deferredReads struct {
	mu    sync.Mutex
	chans []chan attentionAnswer
}

type attentionAnswer struct {
	value *client.Attention
	err   error
}

func (d *deferredReads) fetch(ctx context.Context) (*client.Attention, error) {
	ch := make(chan attentionAnswer, 1)
	d.mu.Lock()
	d.chans = append(d.chans, ch)
	d.mu.Unlock()
	select {
	case a := <-ch:
		return a.value, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *deferredReads) len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.chans)
}

func (d *deferredReads) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.len() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d read(s) to start; got %d", n, d.len())
}

func (d *deferredReads) resolve(i int, a *client.Attention) {
	d.mu.Lock()
	ch := d.chans[i]
	d.mu.Unlock()
	ch <- attentionAnswer{value: a}
}

func (d *deferredReads) reject(i int, err error) {
	d.mu.Lock()
	ch := d.chans[i]
	d.mu.Unlock()
	ch <- attentionAnswer{err: err}
}

func seq(n int64) *int64 { return &n }

func quarantined() *client.Attention {
	return &client.Attention{FlaggedOwnContext: []client.FlaggedContext{{TargetSeq: seq(21), State: "quarantined"}}}
}

// --- the queue ---------------------------------------------------------------

func TestEnqueueEventParksDedupesAndDropsAtOrBelowCursor(t *testing.T) {
	s := New(Options{Client: newStub("a-1")})
	s.SetCursor(4)
	ctx := context.Background()

	if s.EnqueueEvent(ctx, client.Event{Seq: seq(4), Type: "edge.finding"}) {
		t.Error("seq 4 is at the cursor — already delivered, must not queue")
	}
	if s.EnqueueEvent(ctx, client.Event{Seq: seq(2), Type: "edge.finding"}) {
		t.Error("seq 2 is below the cursor — must not queue")
	}
	if !s.EnqueueEvent(ctx, client.Event{Seq: seq(6), Type: "edge.finding"}) {
		t.Error("seq 6 must queue")
	}
	if s.EnqueueEvent(ctx, client.Event{Seq: seq(6), Type: "edge.finding"}) {
		t.Error("duplicate seq must not queue twice")
	}
	if !s.EnqueueEvent(ctx, client.Event{Seq: seq(5), Type: "edge.hypothesis"}) {
		t.Error("seq 5 must queue")
	}
	if s.EnqueueEvent(ctx, client.Event{Type: "edge.finding"}) {
		t.Error("an event with no seq has no dedupe key and must be refused")
	}

	got := s.Pending()
	if len(got) != 2 || *got[0].Seq != 5 || *got[1].Seq != 6 {
		t.Fatalf("pending = %v, want [5 6] in seq order", got)
	}
}

func TestPendingQueueIsBoundedAndCountsWhatItDropped(t *testing.T) {
	s := New(Options{Client: newStub("a-1")})
	ctx := context.Background()
	for i := int64(0); i < 60; i++ {
		s.EnqueueEvent(ctx, client.Event{Seq: seq(i), Type: "edge.finding"})
	}
	pending := s.Pending()
	if len(pending) != PendingMax {
		t.Fatalf("pending length = %d, want %d", len(pending), PendingMax)
	}
	if *pending[0].Seq != 10 || *pending[len(pending)-1].Seq != 59 {
		t.Errorf("kept the wrong window: %d..%d", *pending[0].Seq, *pending[len(pending)-1].Seq)
	}
	if s.PendingDropped() != 10 {
		t.Errorf("pendingDropped = %d, want 10", s.PendingDropped())
	}
}

func TestConsumeUpToAdvancesAndClearsTheOverflowCounter(t *testing.T) {
	s := New(Options{Client: newStub("a-1")})
	ctx := context.Background()
	for i := int64(0); i < 60; i++ {
		s.EnqueueEvent(ctx, client.Event{Seq: seq(i), Type: "edge.finding"})
	}
	if got := s.ConsumeUpTo(59); got != 59 {
		t.Fatalf("cursor = %d, want 59", got)
	}
	if len(s.Pending()) != 0 || s.PendingDropped() != 0 {
		t.Errorf("queue = %d, dropped = %d, want both 0", len(s.Pending()), s.PendingDropped())
	}
}

func TestFlushPendingRendersTheServerDeltaAndPrunes(t *testing.T) {
	stub := newStub("a-1")
	to := int64(4)
	stub.deltaFn = func(_ context.Context, since int64) (*client.FrameDelta, error) {
		return &client.FrameDelta{SinceVersion: since, ToVersion: &to,
			Items: []client.DeltaItem{{Seq: 4, Type: "edge.finding", Summary: "origin pool unhealthy"}}}, nil
	}
	s := New(Options{Client: stub})
	ctx := context.Background()

	if delta := s.FlushPending(ctx); delta != nil {
		t.Fatal("an empty queue must not trigger a delta fetch at all")
	}
	s.EnqueueEvent(ctx, client.Event{Seq: seq(4), Type: "edge.finding"})
	delta := s.FlushPending(ctx)
	if delta == nil || len(delta.Items) != 1 {
		t.Fatalf("delta = %v, want the server's one classified item", delta)
	}
	if s.Cursor() != 4 {
		t.Errorf("cursor = %d, want 4", s.Cursor())
	}
	if len(s.Pending()) != 0 {
		t.Errorf("the flush must prune what it delivered, got %v", s.Pending())
	}
}

func TestFlushPendingLeavesTheQueueInPlaceWhenTheFetchFails(t *testing.T) {
	stub := newStub("a-1")
	stub.deltaFn = func(context.Context, int64) (*client.FrameDelta, error) {
		return nil, errors.New("network down")
	}
	s := New(Options{Client: stub})
	ctx := context.Background()
	s.EnqueueEvent(ctx, client.Event{Seq: seq(4), Type: "edge.finding"})

	if delta := s.FlushPending(ctx); delta != nil {
		t.Fatal("a failed fetch must render nothing")
	}
	if len(s.Pending()) != 1 {
		t.Error("a failed flush must leave what it was owed for the next attempt")
	}
}

// --- the coalesced read: the four properties --------------------------------

func TestQuietRoomCostsExactlyOneAttentionReadForTheWholeSession(t *testing.T) {
	stub := newStub("a-1")
	s := New(Options{Client: stub})
	ctx := context.Background()

	s.RefreshAttentionIfStale(ctx)
	s.RefreshAttentionIfStale(ctx)
	s.RefreshAttentionIfStale(ctx)

	if got := stub.countOf("attention"); got != 1 {
		t.Fatalf("attention reads = %d, want 1 — the first primes it, nothing since said otherwise", got)
	}
}

func TestOnlyAClaimOrContextEventTriggersARead(t *testing.T) {
	stub := newStub("a-1")
	s := New(Options{Client: stub})
	ctx := context.Background()
	s.SetAttentionDirty(false)
	s.SetAttention(&client.Attention{})

	s.EnqueueEvent(ctx, client.Event{Seq: seq(1), Type: "edge.finding"})
	s.WaitForReads()
	if got := stub.countOf("attention"); got != 0 {
		t.Fatalf("an ordinary finding must trigger no attention read, got %d", got)
	}

	s.EnqueueEvent(ctx, client.Event{Seq: seq(2), Type: "claim.staged"})
	s.WaitForReads()
	if got := stub.countOf("attention"); got != 1 {
		t.Fatalf("a claim event must trigger exactly one read, got %d", got)
	}
	if got := stub.countOf("divergence"); got != 1 {
		t.Fatalf("the same arrival must dirty divergence too, got %d read(s)", got)
	}
}

// Property 2 — coalescing, exercised with genuinely overlapping callers.
func TestConcurrentCallersJoinTheInFlightReadInsteadOfIssuingAnother(t *testing.T) {
	d := &deferredReads{}
	stub := newStub("a-1")
	stub.attentionFn = d.fetch
	s := New(Options{Client: stub})
	ctx := context.Background()

	const callers = 8
	var started, done sync.WaitGroup
	started.Add(callers)
	done.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			started.Done()
			s.RefreshAttention(ctx)
			done.Done()
		}()
	}
	started.Wait()
	d.waitFor(t, 1)
	// Give any second caller that was going to issue its own read every chance
	// to do so before asserting it did not.
	time.Sleep(20 * time.Millisecond)
	if got := d.len(); got != 1 {
		t.Fatalf("%d overlapping callers issued %d reads, want 1", callers, got)
	}

	d.resolve(0, quarantined())
	done.Wait()

	if s.Attention() == nil || len(s.Attention().FlaggedOwnContext) != 1 {
		t.Fatalf("every joined caller must see the one read's answer, got %v", s.Attention())
	}
	if stub.countOf("attention") != 1 {
		t.Fatalf("client-side read count = %d, want 1", stub.countOf("attention"))
	}
}

// A burst of claim events costs ONE read, not one per event (the same property
// from the arrival side, which is where it actually bites).
func TestABurstOfClaimEventsCostsOneRead(t *testing.T) {
	d := &deferredReads{}
	stub := newStub("a-1")
	stub.attentionFn = d.fetch
	s := New(Options{Client: stub})
	ctx := context.Background()
	s.SetAttentionDirty(false)

	for i := int64(1); i <= 5; i++ {
		s.EnqueueEvent(ctx, client.Event{Seq: seq(i), Type: "claim.staged"})
	}
	d.waitFor(t, 1)
	time.Sleep(20 * time.Millisecond)
	if got := d.len(); got != 1 {
		t.Fatalf("5 claim events cost %d reads, want 1", got)
	}
	d.resolve(0, &client.Attention{})
	s.WaitForReads()
}

// Property 3 — the dirty flag is cleared on ENTRY, not on success.
func TestDirtyIsClearedOnEntrySoAnEventDuringAReadReDirties(t *testing.T) {
	d := &deferredReads{}
	stub := newStub("a-1")
	stub.attentionFn = d.fetch
	s := New(Options{Client: stub})
	ctx := context.Background()

	go s.RefreshAttention(ctx)
	d.waitFor(t, 1)
	if s.AttentionDirty() {
		t.Fatal("entering a read must clear the flag")
	}

	// An event lands DURING the read.
	s.MarkAttentionDirty()
	d.resolve(0, &client.Attention{})
	s.WaitForReads()

	if !s.AttentionDirty() {
		t.Fatal("an event arriving during a read must leave the snapshot dirty for the next caller")
	}
}

// Properties 1 + 4 — the abandoned read.
func TestAnAbandonedSlowReadCanNeverClobberTheFresherSnapshotThatReplacedIt(t *testing.T) {
	d := &deferredReads{}
	stub := newStub("a-1")
	stub.attentionFn = d.fetch
	s := New(Options{Client: stub})
	ctx := context.Background()
	s.SetAttention(&client.Attention{}) // clean, from earlier in the session
	s.SetAttentionDirty(true)

	// Read #1 starts, misses its budget, and the caller answers with what it has.
	start := time.Now()
	answered := s.SettleAttention(ctx, 30*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("settle took %v — a bounded wait must not become a stuck agent", elapsed)
	}
	if answered == nil || len(answered.FlaggedOwnContext) != 0 {
		t.Fatalf("settle must answer with the previous snapshot, got %v", answered)
	}
	if d.len() != 1 {
		t.Fatalf("read count = %d, want 1", d.len())
	}
	if !s.AttentionDirty() {
		t.Fatal("abandoning re-dirties AT ABANDON TIME, so the next caller starts fresh")
	}

	// Read #2 — fresh, and this one sees the quarantine.
	go s.RefreshAttention(ctx)
	d.waitFor(t, 2)
	d.resolve(1, quarantined())
	// Wait for #2 specifically to have landed.
	waitUntil(t, func() bool {
		a := s.Attention()
		return a != nil && len(a.FlaggedOwnContext) == 1
	}, "read #2 to land")

	// Read #1 lands at last, carrying the world as it was before the quarantine.
	d.resolve(0, &client.Attention{})
	s.WaitForReads()

	if a := s.Attention(); a == nil || len(a.FlaggedOwnContext) != 1 {
		t.Fatalf("the abandoned read overwrote the newer answer: %v", a)
	}
}

// Property 1 on the failure path: a corpse's error says nothing about the
// snapshot its successor owns.
func TestASupersededReadThatFailsDoesNotReDirtyTheSnapshotItsSuccessorOwns(t *testing.T) {
	d := &deferredReads{}
	stub := newStub("a-1")
	stub.attentionFn = d.fetch
	s := New(Options{Client: stub})
	ctx := context.Background()
	s.SetAttention(&client.Attention{})
	s.SetAttentionDirty(true)

	s.SettleAttention(ctx, 30*time.Millisecond) // abandons read #1
	go s.RefreshAttention(ctx)
	d.waitFor(t, 2)
	d.resolve(1, quarantined())
	waitUntil(t, func() bool { return !s.AttentionDirty() }, "read #2 to land clean")

	d.reject(0, errors.New("server unreachable"))
	s.WaitForReads()

	if a := s.Attention(); a == nil || len(a.FlaggedOwnContext) != 1 {
		t.Fatalf("the corpse touched the snapshot: %v", a)
	}
	if s.AttentionDirty() {
		t.Fatal("a superseded read's failure must not re-dirty — that buys a pointless extra round trip")
	}
}

// The two conditional entry points differ in whether they JOIN a read already
// running, and the difference is the whole point of the fast path.
func TestACleanSnapshotIsNotBlockedOnABackgroundReadButSettleStillJoinsIt(t *testing.T) {
	d := &deferredReads{}
	stub := newStub("a-1")
	stub.attentionFn = d.fetch
	s := New(Options{Client: stub})
	s.SetAttention(&client.Attention{})
	s.SetAttentionDirty(false)
	ctx := context.Background()

	// A background read is running (an arriving event started one).
	s.RefreshAttentionAsync(ctx)
	d.waitFor(t, 1)

	// The tool-result path must answer from the clean snapshot immediately —
	// `if (dirty || attention === null)` is false, so it never even asks.
	start := time.Now()
	got := s.RefreshAttentionIfStale(ctx)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("the fast path blocked for %v on a background read", elapsed)
	}
	if got == nil || len(got.FlaggedOwnContext) != 0 {
		t.Fatalf("it must answer with the clean snapshot, got %v", got)
	}

	// The Stop path, by contrast, waits on whatever is already in flight.
	settled := make(chan *client.Attention, 1)
	go func() { settled <- s.SettleAttention(ctx, 2*time.Second) }()
	time.Sleep(20 * time.Millisecond)
	d.resolve(0, quarantined())

	answer := <-settled
	if answer == nil || len(answer.FlaggedOwnContext) != 1 {
		t.Fatalf("settle must join the running read and see its answer, got %v", answer)
	}
	if d.len() != 1 {
		t.Fatalf("settle issued a second read (%d total) instead of joining", d.len())
	}
}

func TestAFailedReadLeavesThePreviousSnapshotAndStaysDirtyForARetry(t *testing.T) {
	stub := newStub("a-1")
	stub.attentionFn = func(context.Context) (*client.Attention, error) {
		return nil, errors.New("server unreachable")
	}
	s := New(Options{Client: stub})
	s.SetAttention(quarantined())
	s.SetAttentionDirty(true)

	got := s.SettleAttention(context.Background(), SettleBudget)
	if got == nil || len(got.FlaggedOwnContext) != 1 {
		t.Fatalf("a failed read must leave the previous answer in place, got %v", got)
	}
	if !s.AttentionDirty() {
		t.Fatal("a failed read must stay dirty so the next caller retries")
	}
}

func TestAHungReadDoesNotHangTheCaller(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stub := newStub("a-1")
	stub.attentionFn = func(ctx context.Context) (*client.Attention, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, errors.New("abandoned")
	}
	s := New(Options{Client: stub})
	s.SetAttention(quarantined())
	s.SetAttentionDirty(true)

	start := time.Now()
	got := s.SettleAttention(context.Background(), SettleBudget)
	elapsed := time.Since(start)

	if elapsed > SettleBudget+200*time.Millisecond {
		t.Fatalf("settle waited %v — a hook that hangs is a hook that gets uninstalled", elapsed)
	}
	if got == nil || len(got.FlaggedOwnContext) != 1 {
		t.Fatalf("it must answer with the previous snapshot, not null: %v", got)
	}
}

// --- the room switch ---------------------------------------------------------

func TestJoiningADifferentWarRoomForgetsThePreviousRoomsQuestions(t *testing.T) {
	clientA, clientB := newStub("a-1"), newStub("a-2")
	s := New(Options{
		Client: clientA,
		Redeem: func(context.Context, string) (client.Config, error) {
			return client.Config{Slug: "acme", IncidentID: "inc-2"}, nil
		},
		ClientFactory: func(client.Config) EdgeClient { return clientB },
	})
	s.SetAttention(&client.Attention{VotesAwaited: []client.VoteAwaited{{ClaimSeq: seq(48)}}})
	s.MarkAttentionNotified([]string{"48:fresh"})
	s.SetAttentionDirty(false)
	s.EnqueueEvent(context.Background(), client.Event{Seq: seq(7), Type: "edge.finding"})

	if _, err := s.JoinWarRoom(context.Background(), "http://api.test/share/xyz"); err != nil {
		t.Fatalf("join: %v", err)
	}

	if s.Attention() != nil {
		t.Error("a different room asks different questions — the old answer must be forgotten")
	}
	if len(s.AttentionNotified()) != 0 {
		t.Error("carrying the already-told set across would silence the new room")
	}
	if !s.AttentionDirty() {
		t.Error("the new room's snapshot must start dirty")
	}
	if len(s.Pending()) != 0 || s.PendingDropped() != 0 || s.Cursor() != -1 {
		t.Errorf("a rejoin must clear the queue and cursor: pending=%v dropped=%d cursor=%d",
			s.Pending(), s.PendingDropped(), s.Cursor())
	}
	if clientA.countOf("leave") != 1 {
		t.Error("the previous room's seat must be released, best-effort")
	}
}

// The case the test above does not reach: a genuine unresolved read spanning
// the switch. `claimSeq` is a PER-INCIDENT counter, so incident A's claim #48
// rendered into a result the agent reads as incident B is not a cosmetic
// mix-up — corroborate_claim goes straight to the CURRENT client, so acting on
// it records a position on whatever #48 happens to be in B.
func TestAReadStillInFlightAgainstTheOldRoomCanNeverAnswerForTheNewOne(t *testing.T) {
	oldReads := &deferredReads{}
	clientA := newStub("a-1")
	clientA.attentionFn = oldReads.fetch

	clientB := newStub("a-2")
	clientB.attentionFn = func(context.Context) (*client.Attention, error) {
		return &client.Attention{VotesAwaited: []client.VoteAwaited{
			{ClaimSeq: seq(7), Statement: "NEW ROOM claim"},
		}}, nil
	}

	s := New(Options{
		Client: clientA,
		Redeem: func(context.Context, string) (client.Config, error) {
			return client.Config{Slug: "acme", IncidentID: "inc-2"}, nil
		},
		ClientFactory: func(client.Config) EdgeClient { return clientB },
	})
	ctx := context.Background()

	// A room event lands in A and starts a read that has not come back yet.
	s.EnqueueEvent(ctx, client.Event{Seq: seq(5), Type: "claim.staged"})
	oldReads.waitFor(t, 1)
	if s.attention.inFlightRead() == nil {
		t.Fatal("precondition: a read must be genuinely in flight")
	}

	if _, err := s.JoinWarRoom(ctx, "http://api.test/share/xyz"); err != nil {
		t.Fatalf("join: %v", err)
	}
	if s.attention.inFlightRead() != nil {
		t.Fatal("the old room's read must be abandoned, not inherited")
	}

	// The next read must go to B rather than short-circuiting onto A's read.
	fresh := s.RefreshAttention(ctx)
	if fresh == nil || len(fresh.VotesAwaited) != 1 || fresh.VotesAwaited[0].Statement != "NEW ROOM claim" {
		t.Fatalf("the fresh read answered for the wrong room: %v", fresh)
	}

	// And when A's request finally lands it must discard its own answer.
	oldReads.resolve(0, &client.Attention{VotesAwaited: []client.VoteAwaited{
		{ClaimSeq: seq(48), Statement: "OLD ROOM claim"},
	}})
	s.WaitForReads()

	got := s.Attention()
	if got == nil || len(got.VotesAwaited) != 1 {
		t.Fatalf("attention = %v", got)
	}
	if got.VotesAwaited[0].Statement != "NEW ROOM claim" || *got.VotesAwaited[0].ClaimSeq != 7 {
		t.Fatalf("incident A's claim leaked into incident B's snapshot: %+v", got.VotesAwaited[0])
	}
}

// --- divergence: the same guard, without the settle path --------------------

func TestRefreshDivergenceIsCoalescedAndBestEffortOnFailure(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	stub := newStub("a-1")
	stub.divergenceFn = func(context.Context) (*client.Divergence, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return &client.Divergence{Diverging: false}, nil
	}
	s := New(Options{Client: stub})
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() { defer wg.Done(); s.RefreshDivergence(ctx) }()
	}
	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls >= 1
	}, "the first divergence read to start")
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("two concurrent callers issued %d divergence reads, want 1", got)
	}
	if d := s.Divergence(); d == nil || d.Diverging {
		t.Fatalf("divergence = %v, want the landed {diverging:false}", d)
	}
}

// --- the whole thing, hammered ----------------------------------------------

// A deliberately adversarial mix: event arrivals, forced reads, settles and a
// room switch all racing. Nothing here asserts an outcome — the assertion is
// the race detector's, plus the invariant that the snapshot always belongs to
// the CURRENT room.
func TestConcurrentEventsReadsAndARoomSwitchAreRaceFree(t *testing.T) {
	clientA := newStub("a-1")
	clientA.attentionFn = func(context.Context) (*client.Attention, error) {
		time.Sleep(time.Millisecond)
		return &client.Attention{VotesAwaited: []client.VoteAwaited{{ClaimSeq: seq(48), Statement: "room A"}}}, nil
	}
	clientB := newStub("a-2")
	clientB.attentionFn = func(context.Context) (*client.Attention, error) {
		time.Sleep(time.Millisecond)
		return &client.Attention{VotesAwaited: []client.VoteAwaited{{ClaimSeq: seq(7), Statement: "room B"}}}, nil
	}

	s := New(Options{
		Client: clientA,
		Redeem: func(context.Context, string) (client.Config, error) {
			return client.Config{Slug: "acme", IncidentID: "inc-2"}, nil
		},
		ClientFactory: func(client.Config) EdgeClient { return clientB },
	})
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := int64(0); j < 40; j++ {
				s.EnqueueEvent(ctx, client.Event{Seq: seq(int64(n)*1000 + j), Type: "claim.staged"})
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				s.RefreshAttentionIfStale(ctx)
				s.SettleAttention(ctx, time.Millisecond)
				_ = s.Attention()
				_ = s.Divergence()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		if _, err := s.JoinWarRoom(ctx, "http://api.test/share/xyz"); err != nil {
			t.Errorf("join: %v", err)
		}
	}()
	wg.Wait()
	s.WaitForReads()

	// After the switch, only room B may ever have written the snapshot.
	if a := s.Attention(); a != nil {
		for _, v := range a.VotesAwaited {
			if v.Statement == "room A" {
				t.Fatalf("room A's answer survived the switch: %+v", v)
			}
		}
	}
}

// --- helpers ----------------------------------------------------------------

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
