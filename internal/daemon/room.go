package daemon

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/realtime"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

var isPlumbing = realtime.IsPlumbing

// RingMax bounds the per-room event ring, the same bound the per-process queue
// used (session.PendingMax). Older events fall off the front; a reader whose
// cursor is behind the ring's oldest event is served from the server's delta.
const RingMax = session.PendingMax

// FrameTTL is how long a cached ranked frame is served before a fresh fetch.
const FrameTTL = 30 * time.Second

// Connection is the room's realtime state, shown by every surface (FR-014).
type Connection string

const (
	Connecting   Connection = "connecting"
	Live         Connection = "live"
	Disconnected Connection = "disconnected"
)

// RoomKey is the identity a second join attaches to: same Landfall, same
// organization, same incident.
func RoomKey(cfg client.Config) string {
	return strings.TrimSuffix(cfg.BaseURL, "/") + "/o/" + cfg.Slug + "/" + cfg.IncidentID
}

// Deps are the room's injectable edges: the production wiring is the real
// HTTP client, the real Socket.IO watcher and the real clock; tests supply
// stubs.
type Deps struct {
	NewClient func(client.Config) session.EdgeClient
	// Watch subscribes to the room's live events; it returns a stop func. Nil
	// means pull-only (no live push), which the daemon then reports as
	// Disconnected rather than pretending.
	Watch     func(ctx context.Context, cfg client.Config, ownInstanceID func() string, onEvent func(client.Event)) (stop func())
	Heartbeat time.Duration
	Log       func(string)
	// OnEvent is called for every event that enters the ring, after it is
	// stored: the daemon's passive surfaces (notification, doorbell) hang here.
	OnEvent func(room *Room, evt client.Event)
	Now     func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Room is one incident this machine has joined, owned by the daemon.
type Room struct {
	mu sync.Mutex

	Key             string
	Config          client.Config
	AgentInstanceID string
	Connection      Connection
	CwdAllowed      bool

	client  session.EdgeClient
	events  []client.Event
	frame   *client.ContextFrame
	frameAt time.Time
	readers map[string]*Reader

	lastReaderLeftAt time.Time
	openedAt         time.Time
	deps             Deps
	cancel           context.CancelFunc
	stopWatch        func()
	subscribers      map[chan client.Event]struct{}
}

// OpenRoom joins the incident once for the whole machine and starts presence
// and live watch. Readers attach afterwards.
func OpenRoom(ctx context.Context, cfg client.Config, deps Deps) (*Room, error) {
	if deps.NewClient == nil {
		return nil, errors.New("daemon: Deps.NewClient is required")
	}
	cl := deps.NewClient(cfg)
	res, err := cl.Join(ctx)
	if err != nil {
		return nil, err
	}
	r := &Room{
		Key:        RoomKey(cfg),
		Config:     cfg,
		Connection: Connecting,
		client:     cl,
		readers:    map[string]*Reader{},
		deps:       deps,
		openedAt:   deps.now(),
	}
	if res != nil {
		r.AgentInstanceID = res.AgentInstanceID
	}
	r.start(ctx)
	return r, nil
}

// RestoreRoom rejoins a room from persisted state (the daemon restarted). The
// readers come back with their cursors; the token is the stored one.
func RestoreRoom(ctx context.Context, st RoomState, deps Deps) (*Room, error) {
	r, err := OpenRoom(ctx, st.Config, deps)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.CwdAllowed = st.CwdAllowed
	for name, rd := range st.Readers {
		cp := *rd
		cp.Connected = false
		r.readers[name] = &cp
	}
	r.mu.Unlock()
	return r, nil
}

func (r *Room) start(ctx context.Context) {
	roomCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	interval := r.deps.Heartbeat
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-roomCtx.Done():
				return
			case <-t.C:
				bctx, c := context.WithTimeout(context.Background(), interval)
				_ = r.client.Heartbeat(bctx, "investigating")
				c()
			}
		}
	}()
	if r.deps.Watch != nil {
		r.stopWatch = r.deps.Watch(roomCtx, r.Config, func() string { return r.AgentInstanceID }, r.enqueue)
		r.mu.Lock()
		r.Connection = Live
		r.mu.Unlock()
	} else {
		r.mu.Lock()
		r.Connection = Disconnected
		r.mu.Unlock()
	}
}

// enqueue stores a pushed event in the ring: deduped by seq, kept in order,
// bounded. Machinery is stored too (a reader's delta may still want it); the
// untold set is what filters it out.
func (r *Room) enqueue(evt client.Event) {
	if evt.Seq == nil {
		return
	}
	r.mu.Lock()
	for _, e := range r.events {
		if e.Seq != nil && *e.Seq == *evt.Seq {
			r.mu.Unlock()
			return
		}
	}
	r.events = append(r.events, evt)
	sort.SliceStable(r.events, func(i, j int) bool { return r.events[i].SeqOr(0) < r.events[j].SeqOr(0) })
	for len(r.events) > RingMax {
		r.events = r.events[1:]
	}
	r.Connection = Live
	subs := make([]chan client.Event, 0, len(r.subscribers))
	if !isPlumbing(evt.Type) {
		for ch := range r.subscribers {
			subs = append(subs, ch)
		}
	}
	r.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- evt:
		default: // a slow subscriber drops the event; it can `delta` for the rest
		}
	}
	if r.deps.OnEvent != nil {
		r.deps.OnEvent(r, evt)
	}
}

// Subscribe registers a live stream of the room's substantive events.
func (r *Room) Subscribe() (<-chan client.Event, func()) {
	ch := make(chan client.Event, 64)
	r.mu.Lock()
	if r.subscribers == nil {
		r.subscribers = map[chan client.Event]struct{}{}
	}
	r.subscribers[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subscribers, ch)
		r.mu.Unlock()
	}
}

// MaxSeq is the newest seq the room has seen (from the ring or the frame).
func (r *Room) MaxSeq() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxSeqLocked()
}

func (r *Room) maxSeqLocked() int64 {
	max := int64(-1)
	if n := len(r.events); n > 0 {
		max = r.events[n-1].SeqOr(-1)
	}
	if r.frame != nil {
		if c := frameCursor(r.frame); c > max {
			max = c
		}
	}
	return max
}

// Attach registers (or reconnects) a reader. A new reader starts at the room's
// current position: what happened before it arrived is the brief's business,
// not a backlog of "new" items. A returning reader (same name, within the TTL)
// keeps its cursor.
func (r *Room) Attach(rd Reader) *Reader {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.deps.now()
	if existing, ok := r.readers[rd.Name]; ok {
		existing.Connected = true
		existing.LastSeenAt = now
		if rd.Host != "" {
			existing.Host = rd.Host
		}
		return existing
	}
	rd.Cursor = r.maxSeqLocked()
	rd.AttachedAt = now
	rd.LastSeenAt = now
	rd.Connected = true
	cp := rd
	r.readers[rd.Name] = &cp
	return &cp
}

// Detach marks a reader gone. Its cursor is kept for SilentReaderTTL.
func (r *Room) Detach(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rd, ok := r.readers[name]; ok {
		rd.Connected = false
		rd.LastSeenAt = r.deps.now()
	}
	if r.connectedLocked() == 0 {
		r.lastReaderLeftAt = r.deps.now()
	}
}

// connectedLocked counts the readers that hold the room open. A terminal
// reader never does: it is the person's passive view, created alongside their
// agent and read by short-lived hook processes, so it is "connected" only in
// the sense of existing. Agents, panels and push consumers keep the room alive.
func (r *Room) connectedLocked() int {
	n := 0
	for _, rd := range r.readers {
		if rd.Connected && rd.Kind != KindTerminal {
			n++
		}
	}
	return n
}

// ConnectedReaders is how many readers currently hold a live attachment.
func (r *Room) ConnectedReaders() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connectedLocked()
}

// IdleSince is when the last reader left, or zero while any is attached. A
// room that has had no attached reader since it was opened (one restored from
// state after a stop, whose readers came back detached) counts as idle since it
// was opened: otherwise a daemon restored with nobody reading would live for
// ever, holding a phantom presence in the room (found live, 2026-09-21).
func (r *Room) IdleSince() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.connectedLocked() > 0 {
		return time.Time{}
	}
	if r.lastReaderLeftAt.IsZero() {
		return r.openedAt
	}
	return r.lastReaderLeftAt
}

// Reader looks a reader up by name.
func (r *Room) Reader(name string) (*Reader, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rd, ok := r.readers[name]
	return rd, ok
}

// Readers is a copy of every reader, forgetting silent ones past the TTL.
func (r *Room) Readers() []*Reader {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.deps.now()
	out := make([]*Reader, 0, len(r.readers))
	for name, rd := range r.readers {
		if !rd.Connected && now.Sub(rd.LastSeenAt) > SilentReaderTTL {
			delete(r.readers, name)
			continue
		}
		cp := *rd
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// UntoldFor is a reader's untold set (see Untold).
func (r *Room) UntoldFor(name string) []client.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	rd, ok := r.readers[name]
	if !ok {
		return nil
	}
	return Untold(r.events, rd.Cursor)
}

// Advance moves a reader's cursor under the kind rule.
func (r *Room) Advance(name string, caller Kind, upTo int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rd, ok := r.readers[name]
	if !ok {
		return -1, errors.New("no reader " + name)
	}
	return rd.Advance(caller, upTo)
}

// Frame is the ranked context frame, cached for FrameTTL.
func (r *Room) Frame(ctx context.Context) (*client.ContextFrame, error) {
	r.mu.Lock()
	if r.frame != nil && r.deps.now().Sub(r.frameAt) < FrameTTL {
		f := r.frame
		r.mu.Unlock()
		return f, nil
	}
	cl := r.client
	r.mu.Unlock()
	f, err := cl.GetContextFrame(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.frame = f
	r.frameAt = r.deps.now()
	r.mu.Unlock()
	return f, nil
}

// Delta is an agent reader's own pull: the server's classified delta since
// this reader's cursor, and the cursor moved to what was delivered. This is
// what a front end renders onto a tool result in place of FlushPending.
func (r *Room) Delta(ctx context.Context, name string) (*client.FrameDelta, error) {
	r.mu.Lock()
	rd, ok := r.readers[name]
	if !ok {
		r.mu.Unlock()
		return nil, errors.New("no reader " + name)
	}
	since := rd.Cursor
	cl := r.client
	r.mu.Unlock()
	delta, err := cl.GetContextDelta(ctx, since)
	if err != nil {
		return nil, err
	}
	if delta != nil && delta.ToVersion != nil {
		_, _ = r.Advance(name, KindAgent, *delta.ToVersion)
	}
	return delta, nil
}

// Client is the room's one HTTP client (the front end's tools reuse its
// instance id through SetAgentInstanceID on their own client).
func (r *Room) Client() session.EdgeClient { return r.client }

// Close stops presence and live watch and leaves the room.
func (r *Room) Close(ctx context.Context) {
	if r.stopWatch != nil {
		r.stopWatch()
	}
	if r.cancel != nil {
		r.cancel()
	}
	_ = r.client.Leave(ctx)
	r.mu.Lock()
	r.Connection = Disconnected
	r.mu.Unlock()
}

// Snapshot is the room as persisted.
func (r *Room) Snapshot() RoomState {
	r.mu.Lock()
	defer r.mu.Unlock()
	readers := make(map[string]*Reader, len(r.readers))
	for name, rd := range r.readers {
		cp := *rd
		readers[name] = &cp
	}
	return RoomState{Config: r.Config, CwdAllowed: r.CwdAllowed, Readers: readers, SavedAt: r.deps.now()}
}

// frameCursor is the frame's as-of seq as a cursor, -1 when absent — the same
// reading the front end already makes (narrate.FrameCursor).
func frameCursor(f *client.ContextFrame) int64 { return narrate.FrameCursor(f) }
