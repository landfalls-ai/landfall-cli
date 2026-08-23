// Package session holds the in-memory state of one running `landfall serve`
// process — a Go port of `createBridgeSession` in `src/tools.mjs`. Nothing
// here is persisted; it exists for the process lifetime only.
//
// The (possibly not-yet-joined) client, the durable update cursor, the queue
// of live room events waiting to reach the agent, and the two coalesced
// background reads (attention, divergence) whose concurrency contract is
// documented in snapshot.go.
package session

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

// PendingMax is the upper bound on parked live events. The queue exists to be
// flushed onto the next tool result, and a tool result is not a place to dump
// a timeline.
const PendingMax = 50

// SettleBudget is the longest the hook socket's `peek` will wait for an
// in-flight attention read.
//
// The bound that matters is this CLI's own 250ms socket round-trip budget, not
// a host-imposed one. Waiting less than the round trip we already give
// ourselves leaves room for the rest of it and keeps the hook fast regardless
// of what the host would tolerate.
const SettleBudget = 150 * time.Millisecond

// ErrNotConnected is what the tool wrapper turns into the caller-visible
// "not connected — call join_war_room …" error.
var ErrNotConnected = errors.New("not connected")

// EdgeClient is the outbound surface the session drives. *client.Client
// satisfies it; tests substitute their own.
type EdgeClient interface {
	Join(ctx context.Context) (*client.JoinResult, error)
	Leave(ctx context.Context) error
	Heartbeat(ctx context.Context, doing string) error
	Contribute(ctx context.Context, kind string, body map[string]any) error
	UploadArtifact(ctx context.Context, filename, contentType, dataBase64 string) (*client.ArtifactResult, error)
	FlagContext(ctx context.Context, targetSeq int64, reason, targetKind string) error
	PositionClaim(ctx context.Context, claimSeq int64, position, reason string) error
	StageClaim(ctx context.Context, body map[string]any) error
	GetBrief(ctx context.Context) ([]client.Event, error)
	GetUpdates(ctx context.Context, sinceSeq int64) ([]client.Event, error)
	GetContextFrame(ctx context.Context) (*client.ContextFrame, error)
	GetContextDelta(ctx context.Context, sinceVersion int64) (*client.FrameDelta, error)
	SearchContext(ctx context.Context, query string) (*client.SearchResult, error)
	GetAttention(ctx context.Context) (*client.Attention, error)
	GetDivergence(ctx context.Context) (*client.Divergence, error)
	AgentInstanceID() string
	Config() client.Config
}

// RedeemFunc turns a share link into an incident-scoped bridge config.
type RedeemFunc func(ctx context.Context, shareURL string) (client.Config, error)

// ClientFactory builds the client for a freshly redeemed config.
type ClientFactory func(cfg client.Config) EdgeClient

// Options configures a new Session.
type Options struct {
	// Client is an already-joined client (feature 012's legacy entry point).
	Client EdgeClient
	// AgentLabel is the name this agent narrates under.
	AgentLabel string
	// Redeem turns a share link into a bridge config. Defaults to the real
	// HTTP redemption.
	Redeem RedeemFunc
	// ClientFactory builds a client from a redeemed config. Defaults to the
	// real HTTP client.
	ClientFactory ClientFactory
	// OnJoined lets the host process start presence/live-watch once a join
	// lands.
	OnJoined func(s *Session, cfg client.Config) error
	// Doer is the transport handed to the default ClientFactory/Redeem.
	Doer client.Doer
}

// Session is one bridge session.
type Session struct {
	mu             sync.Mutex
	client         EdgeClient
	cursor         int64
	pending        []client.Event
	pendingDropped int
	agentLabel     string

	// Vote requests / divergence nudges already announced in-band, so the same
	// claim is not re-appended to every tool result for the rest of the
	// session. The server has no notion of "already told this terminal", so
	// de-duplication is entirely this session's responsibility.
	attentionNotified  map[string]bool
	divergenceNotified map[string]bool

	attention  snapshot[client.Attention]
	divergence snapshot[client.Divergence]

	redeem        RedeemFunc
	clientFactory ClientFactory
	onJoined      func(s *Session, cfg client.Config) error
	doer          client.Doer
}

// New builds a bridge session. `attentionDirty`/`divergenceDirty` start true
// so the first tool call asks once.
func New(opts Options) *Session {
	s := &Session{
		client:             opts.Client,
		cursor:             -1,
		agentLabel:         opts.AgentLabel,
		attentionNotified:  map[string]bool{},
		divergenceNotified: map[string]bool{},
		redeem:             opts.Redeem,
		clientFactory:      opts.ClientFactory,
		onJoined:           opts.OnJoined,
		doer:               opts.Doer,
	}
	if s.agentLabel == "" {
		s.agentLabel = "edge-agent"
	}
	s.attention.dirty = true
	s.divergence.dirty = true
	if s.redeem == nil {
		s.redeem = func(ctx context.Context, shareURL string) (client.Config, error) {
			return client.RedeemShareLink(ctx, shareURL, client.RedeemOptions{Doer: s.doer})
		}
	}
	if s.clientFactory == nil {
		s.clientFactory = func(cfg client.Config) EdgeClient {
			return client.New(cfg, s.doer)
		}
	}
	return s
}

// AgentLabel is the name this agent narrates under.
func (s *Session) AgentLabel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentLabel
}

// Client is the current client, or nil before a join.
func (s *Session) Client() EdgeClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// SetClient replaces the client outright (feature 012's legacy path, and
// tests).
func (s *Session) SetClient(c EdgeClient) {
	s.mu.Lock()
	s.client = c
	s.mu.Unlock()
}

// Cursor is the durable delivery cursor; it starts at -1.
func (s *Session) Cursor() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

// SetCursor forces the cursor (tests, and a caller restoring state).
func (s *Session) SetCursor(v int64) {
	s.mu.Lock()
	s.cursor = v
	s.mu.Unlock()
}

// AdvanceCursorTo moves the cursor forward to `v`, never backwards.
func (s *Session) AdvanceCursorTo(v int64) {
	s.mu.Lock()
	if v > s.cursor {
		s.cursor = v
	}
	s.mu.Unlock()
}

// Pending is a copy of the parked live events, in seq order.
func (s *Session) Pending() []client.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]client.Event, len(s.pending))
	copy(out, s.pending)
	return out
}

// PendingDropped counts events discarded because the queue was full. The
// cursor is never advanced for them, so `get_updates` can still fetch them;
// the count is what keeps the overflow visible instead of silent.
func (s *Session) PendingDropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingDropped
}

// EnqueueEvent parks a live room event for delivery, returning true when it
// was queued. A numeric `seq` is required: it is both the dedupe key and how
// an event the agent has already seen is recognised.
func (s *Session) EnqueueEvent(ctx context.Context, evt client.Event) bool {
	if evt.Seq == nil {
		return false
	}
	seq := *evt.Seq

	s.mu.Lock()
	if seq <= s.cursor {
		s.mu.Unlock()
		return false
	}
	for _, e := range s.pending {
		if e.Seq != nil && *e.Seq == seq {
			s.mu.Unlock()
			return false
		}
	}
	s.pending = append(s.pending, evt)
	sort.SliceStable(s.pending, func(i, j int) bool {
		return s.pending[i].SeqOr(0) < s.pending[j].SeqOr(0)
	})
	for len(s.pending) > PendingMax {
		s.pending = s.pending[1:]
		s.pendingDropped++
	}
	s.mu.Unlock()

	// A claim/vetting event is the only thing that can change what awaits this
	// agent, so ARRIVAL is the refresh trigger — no timer anywhere. The read
	// STARTS here rather than only marking the snapshot dirty: the flag alone
	// was acted on by exactly one code path (the tool-call flush), and the Stop
	// hook makes no tool call — so a quarantine landing after an agent's last
	// tool call never reached the Stop path at all. Fire-and-forget and
	// coalesced: a burst of claim events costs one read, and a failed one
	// leaves the flag set for the next caller.
	//
	// The predicate itself is narrate.TouchesAttention — attention.mjs's own
	// ATTENTION_PREFIXES, in the one package that owns them. A second copy here
	// would be a second answer to "can this event change what awaits me", and
	// the two would drift the first time a prefix is added.
	if narrate.TouchesAttention(evt.Type) {
		// The same arrival that can change what awaits this agent's VOTE can
		// also change the room's ESTABLISHED direction — both dirty together,
		// one background read each.
		s.divergence.markDirty()
		s.RefreshDivergenceAsync(ctx)
		s.attention.markDirty()
		s.RefreshAttentionAsync(ctx)
	}
	return true
}

// AdvanceCursor moves the cursor past every event in `events`, and drops
// anything the queue was still holding at or below the new cursor — a pull the
// agent made itself has already delivered those.
func (s *Session) AdvanceCursor(events []client.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range events {
		if e.Seq != nil && *e.Seq > s.cursor {
			s.cursor = *e.Seq
		}
	}
	s.prunePendingLocked(false)
}

// ConsumeUpTo advances the cursor straight to `upTo` and drops everything the
// queue was holding at or below it — what the hook socket's `consume` verb
// calls: a Stop hook that has already spelled the events out to the agent has
// delivered them, exactly as a flushed tool result would have.
func (s *Session) ConsumeUpTo(upTo int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if upTo > s.cursor {
		s.cursor = upTo
	}
	// Same rule as the flush: the overflow counter has been reported once the
	// queue it belonged to is gone. The cursor was never advanced for the
	// dropped events themselves, so `get_updates` can still fetch them.
	s.prunePendingLocked(true)
	return s.cursor
}

// prunePendingLocked drops queued events at or below the cursor. With
// `clearDropped`, an emptied queue also clears the overflow counter.
func (s *Session) prunePendingLocked(clearDropped bool) {
	kept := s.pending[:0]
	for _, e := range s.pending {
		if e.Seq != nil && *e.Seq > s.cursor {
			kept = append(kept, e)
		}
	}
	s.pending = kept
	if clearDropped && len(s.pending) == 0 {
		s.pendingDropped = 0
	}
}

// FlushPending drains what is owed onto a tool result.
//
// The LOCAL queue is used only as the TRIGGER — "did the live socket say
// something arrived since we last checked" — so a quiet room still costs zero
// extra requests per tool call. Once triggered, the CONTENT is always the
// server's own classification (`GET …/edge/context/delta`), never the raw
// locally-queued events: the server is the one place that knows what is
// addressed to this viewer versus routine plumbing.
//
// Returns nil when there is nothing owed, so an ordinary result is untouched.
// A failed fetch leaves the queue in place for the next flush attempt rather
// than silently discarding what it was owed.
func (s *Session) FlushPending(ctx context.Context) *client.FrameDelta {
	s.mu.Lock()
	owed := len(s.pending) > 0 || s.pendingDropped > 0
	cl := s.client
	since := s.cursor
	s.mu.Unlock()

	if !owed || cl == nil {
		return nil
	}
	delta, err := cl.GetContextDelta(ctx, since)
	if err != nil || delta == nil {
		return nil
	}
	s.mu.Lock()
	if delta.ToVersion != nil && *delta.ToVersion > s.cursor {
		s.cursor = *delta.ToVersion
	}
	s.prunePendingLocked(true)
	s.mu.Unlock()
	return delta
}

// --- the two coalesced reads ------------------------------------------------

func (s *Session) attentionFetch(cl EdgeClient) fetchFunc[client.Attention] {
	return func(ctx context.Context) (*client.Attention, error) {
		return cl.GetAttention(ctx)
	}
}

func (s *Session) divergenceFetch(cl EdgeClient) fetchFunc[client.Divergence] {
	return func(ctx context.Context) (*client.Divergence, error) {
		return cl.GetDivergence(ctx)
	}
}

// RefreshAttention re-reads the attention projection, best-effort. Never
// returns an error and never blocks a tool call: an unreachable server means
// "nothing known to be waiting", exactly as it does for the live socket.
func (s *Session) RefreshAttention(ctx context.Context) *client.Attention {
	cl := s.Client()
	if cl == nil {
		return s.attention.get()
	}
	return s.attention.refresh(ctx, s.attentionFetch(cl))
}

// RefreshAttentionAsync starts (or joins) an attention read without waiting.
func (s *Session) RefreshAttentionAsync(ctx context.Context) {
	cl := s.Client()
	if cl == nil {
		return
	}
	s.attention.refreshAsync(ctx, s.attentionFetch(cl))
}

// RefreshAttentionIfStale is the tool-result path's forced read: it fetches
// only when the snapshot is dirty or has never landed.
func (s *Session) RefreshAttentionIfStale(ctx context.Context) *client.Attention {
	cl := s.Client()
	if cl == nil {
		return s.attention.get()
	}
	return s.attention.refreshIfStale(ctx, s.attentionFetch(cl))
}

// SettleAttention makes the snapshot as current as it can be made within
// `timeout`, then returns whatever there is — the Stop hook's entry point. A
// zero timeout uses SettleBudget.
func (s *Session) SettleAttention(ctx context.Context, timeout time.Duration) *client.Attention {
	if timeout <= 0 {
		timeout = SettleBudget
	}
	cl := s.Client()
	if cl == nil {
		return s.attention.get()
	}
	return s.attention.settle(ctx, s.attentionFetch(cl), timeout)
}

// Attention is the last landed attention answer, or nil if none ever landed.
func (s *Session) Attention() *client.Attention { return s.attention.get() }

// SetAttention overwrites the snapshot (tests, and a caller restoring state).
func (s *Session) SetAttention(a *client.Attention) { s.attention.set(a) }

// AttentionDirty reports whether the next caller should re-read.
func (s *Session) AttentionDirty() bool { return s.attention.isDirty() }

// SetAttentionDirty forces the flag.
func (s *Session) SetAttentionDirty(v bool) { s.attention.setDirty(v) }

// MarkAttentionDirty flags the snapshot for a re-read without discarding it.
func (s *Session) MarkAttentionDirty() { s.attention.markDirty() }

// RefreshDivergence re-reads the divergence projection, coalesced and
// best-effort. Deliberately SIMPLER than attention's settle/abandon machinery:
// nothing hard depends on this being maximally fresh (it feeds a tier-0
// statusline nudge, not a Stop-hook block).
func (s *Session) RefreshDivergence(ctx context.Context) *client.Divergence {
	cl := s.Client()
	if cl == nil {
		return s.divergence.get()
	}
	return s.divergence.refresh(ctx, s.divergenceFetch(cl))
}

// RefreshDivergenceAsync starts (or joins) a divergence read without waiting.
func (s *Session) RefreshDivergenceAsync(ctx context.Context) {
	cl := s.Client()
	if cl == nil {
		return
	}
	s.divergence.refreshAsync(ctx, s.divergenceFetch(cl))
}

// Divergence is the last landed divergence answer, or nil if none ever landed.
func (s *Session) Divergence() *client.Divergence { return s.divergence.get() }

// SetDivergence overwrites the snapshot (tests, and a caller restoring state).
func (s *Session) SetDivergence(d *client.Divergence) { s.divergence.set(d) }

// DivergenceDirty reports whether the next trigger should re-read.
func (s *Session) DivergenceDirty() bool { return s.divergence.isDirty() }

// MarkDivergenceDirty flags the divergence snapshot for a re-read.
func (s *Session) MarkDivergenceDirty() { s.divergence.markDirty() }

// WaitForReads blocks until every background read this session spawned has
// landed. For tests and for an orderly shutdown — never on the fast path.
func (s *Session) WaitForReads() {
	s.attention.wait()
	s.divergence.wait()
}

// --- the announced-once registries -----------------------------------------

// AttentionNotified is a copy of the vote-request keys already announced.
func (s *Session) AttentionNotified() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool, len(s.attentionNotified))
	for k, v := range s.attentionNotified {
		out[k] = v
	}
	return out
}

// MarkAttentionNotified records keys as announced. Called only once the block
// is in hand, so a failed read cannot silently consume the announcement.
func (s *Session) MarkAttentionNotified(keys []string) {
	s.mu.Lock()
	for _, k := range keys {
		s.attentionNotified[k] = true
	}
	s.mu.Unlock()
}

// DivergenceNotified is a copy of the divergence-nudge keys already announced.
func (s *Session) DivergenceNotified() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool, len(s.divergenceNotified))
	for k, v := range s.divergenceNotified {
		out[k] = v
	}
	return out
}

// MarkDivergenceNotified records keys as announced.
func (s *Session) MarkDivergenceNotified(keys []string) {
	s.mu.Lock()
	for _, k := range keys {
		s.divergenceNotified[k] = true
	}
	s.mu.Unlock()
}

// --- joining ----------------------------------------------------------------

// JoinWarRoom redeems a share link, joins, resets every per-room piece of
// state, and fires OnJoined so the host process can start presence/live-watch.
func (s *Session) JoinWarRoom(ctx context.Context, shareURL string) (client.Config, error) {
	cfg, err := s.redeem(ctx, shareURL)
	if err != nil {
		return client.Config{}, err
	}
	s.mu.Lock()
	label := s.agentLabel
	s.mu.Unlock()
	if cfg.AgentLabel == "" {
		cfg.AgentLabel = label
	}

	next := s.clientFactory(cfg)
	if _, err := next.Join(ctx); err != nil {
		return client.Config{}, err
	}

	s.mu.Lock()
	prev := s.client
	s.client = next
	s.cursor = -1
	s.pending = nil
	s.pendingDropped = 0
	s.attentionNotified = map[string]bool{}
	s.divergenceNotified = map[string]bool{}
	s.mu.Unlock()

	if prev != nil && prev != next {
		_ = prev.Leave(ctx) // best-effort
	}

	// A different room asks different questions of a different participant, so
	// both snapshots are forgotten AND THE READ STILL IN FLIGHT AGAINST THE OLD
	// ROOM IS ABANDONED. `invalidate` bumps the generation, so when that read
	// finally lands its own guard is already false and it discards its answer
	// instead of writing incident A's claims into incident B's snapshot.
	// Without this the piggyback channel could render "vote requested: claim
	// #N" for a claim belonging to the room the agent just left — and
	// `claimSeq` is a per-incident counter, so acting on it in the new room
	// records a position on an unrelated claim.
	s.attention.invalidate()
	s.divergence.invalidate()

	if s.onJoined != nil {
		if err := s.onJoined(s, cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}
