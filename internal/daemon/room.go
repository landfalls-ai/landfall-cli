package daemon

import (
	"context"
	"errors"
	"fmt"
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

func (d Deps) log(msg string) {
	if d.Log != nil {
		d.Log(msg)
	}
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// DefaultSeatLabel is the label an agent session joins under when nothing
// better is known (no LANDFALL_AGENT_LABEL, no MCP clientInfo).
const DefaultSeatLabel = "edge-agent"

// SeatLabel is the seat a config joins: its AgentLabel, or the default.
func SeatLabel(cfg client.Config) string {
	if cfg.AgentLabel != "" {
		return cfg.AgentLabel
	}
	return DefaultSeatLabel
}

// Seat is one agent session this machine holds in a room: one /edge/join, one
// server-issued instance id, one label, one presence. A room has one seat per
// harness (2026-09-28): Claude Code and Codex in the same checkout used to
// write under ONE session labelled "edge-agent", each dropped the other's
// events as its own echo, and the room could not tell them apart.
type Seat struct {
	Label      string
	InstanceID string
	Config     client.Config

	client session.EdgeClient
	// attention is what the room is waiting on from THIS seat's agent identity
	// (quarantined citations, awaited positions): the Stop hook's second reason
	// to refuse (#252). Refreshed on demand by peek, marked stale by any event
	// narrate.TouchesAttention names. Nil until fetched.
	attention      *client.Attention
	attentionStale bool
	// idleSince is when this seat's last agent reader left (or when it was
	// opened, if none ever attached); zero while any is attached.
	idleSince time.Time
}

// Room is one incident this machine has joined, owned by the daemon.
type Room struct {
	mu sync.Mutex

	Key    string
	Config client.Config
	// AgentInstanceID is the PRIMARY seat's instance id (the first harness to
	// join); every seat's id is in seats.
	AgentInstanceID string
	Connection      Connection
	CwdAllowed      bool

	// client is the primary seat's client, used for the room's shared reads
	// (the frame, the backfill). Per-agent reads go through the seat's own.
	client  session.EdgeClient
	seats   map[string]*Seat
	primary string
	events  []client.Event
	frame   *client.ContextFrame
	frameAt time.Time
	readers map[string]*Reader
	// links are the share links (by LinkHash) that opened or joined this room
	// on this machine. A share link is single-use; a second harness handed the
	// same link joins through the room the daemon already holds instead of
	// redeeming it again (which the server refuses).
	links map[string]struct{}

	lastReaderLeftAt time.Time
	openedAt         time.Time
	deps             Deps
	cancel           context.CancelFunc
	stopWatch        func()
	subscribers      map[chan client.Event]struct{}
}

// OpenRoom joins the incident for the first harness on this machine and
// starts presence and live watch. Readers attach afterwards; a second harness
// gets its own seat through EnsureSeat.
func OpenRoom(ctx context.Context, cfg client.Config, deps Deps) (*Room, error) {
	if deps.NewClient == nil {
		return nil, errors.New("daemon: Deps.NewClient is required")
	}
	seat, err := joinSeat(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	seat.idleSince = deps.now()
	r := &Room{
		Key:             RoomKey(cfg),
		Config:          cfg,
		Connection:      Connecting,
		client:          seat.client,
		AgentInstanceID: seat.InstanceID,
		seats:           map[string]*Seat{seat.Label: seat},
		primary:         seat.Label,
		readers:         map[string]*Reader{},
		deps:            deps,
		openedAt:        deps.now(),
	}
	r.start(ctx)
	return r, nil
}

// joinSeat is one /edge/join under the config's label.
func joinSeat(ctx context.Context, cfg client.Config, deps Deps) (*Seat, error) {
	cfg.AgentLabel = SeatLabel(cfg)
	cl := deps.NewClient(cfg)
	res, err := cl.Join(ctx)
	if err != nil {
		return nil, err
	}
	seat := &Seat{Label: cfg.AgentLabel, Config: cfg, client: cl}
	if res != nil {
		seat.InstanceID = res.AgentInstanceID
	}
	return seat, nil
}

// EnsureSeat returns the seat for cfg's label, joining the room under that
// label first if this machine has no such seat yet. The join uses cfg's own
// credential; a config that carries none (the front end could not resolve
// one) borrows the room's, which is the same person's session.
func (r *Room) EnsureSeat(ctx context.Context, cfg client.Config) (*Seat, error) {
	label := SeatLabel(cfg)
	r.mu.Lock()
	if s, ok := r.seats[label]; ok {
		r.mu.Unlock()
		return s, nil
	}
	if cfg.Token == "" {
		cfg.Token = r.Config.Token
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = r.Config.BaseURL
	}
	if cfg.HumanActorID == "" {
		cfg.HumanActorID = r.Config.HumanActorID
	}
	cfg.Slug, cfg.IncidentID = r.Config.Slug, r.Config.IncidentID
	deps := r.deps
	r.mu.Unlock()

	seat, err := joinSeat(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	seat.idleSince = deps.now()
	r.mu.Lock()
	if existing, ok := r.seats[label]; ok { // lost a race; keep the first
		r.mu.Unlock()
		_ = seat.client.Leave(ctx)
		return existing, nil
	}
	r.seats[label] = seat
	r.mu.Unlock()
	deps.log(fmt.Sprintf("joined %s as %q (instance %s)", cfg.IncidentID, label, seat.InstanceID))
	return seat, nil
}

func (r *Room) addLinkLocked(hash string) {
	if hash == "" {
		return
	}
	if r.links == nil {
		r.links = map[string]struct{}{}
	}
	r.links[hash] = struct{}{}
}

// AddLink records that a share link (by LinkHash) belongs to this room.
func (r *Room) AddLink(hash string) {
	r.mu.Lock()
	r.addLinkLocked(hash)
	r.mu.Unlock()
}

// HasLink reports whether a share link (by LinkHash) belongs to this room.
func (r *Room) HasLink(hash string) bool {
	if hash == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.links[hash]
	return ok
}

// Seats is a copy of the room's seats, primary first.
func (r *Room) Seats() []Seat {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seatsLocked()
}

func (r *Room) seatsLocked() []Seat {
	out := make([]Seat, 0, len(r.seats))
	if p, ok := r.seats[r.primary]; ok {
		out = append(out, *p)
	}
	labels := make([]string, 0, len(r.seats))
	for l := range r.seats {
		if l != r.primary {
			labels = append(labels, l)
		}
	}
	sort.Strings(labels)
	for _, l := range labels {
		out = append(out, *r.seats[l])
	}
	return out
}

// seatForLocked is the seat a reader speaks for: its own, or the primary when
// it names none (a panel, or a reader persisted before seats existed).
func (r *Room) seatForLocked(rd *Reader) *Seat {
	if rd != nil && rd.Seat != "" {
		if s, ok := r.seats[rd.Seat]; ok {
			return s
		}
	}
	return r.seats[r.primary]
}

// ownIDsLocked are the instance ids whose writes are a reader's own echo: its
// seat's for an agent or terminal reader, and every seat's on this machine for
// a panel or push reader (which never saw the machine's own writes).
func (r *Room) ownIDsLocked(rd *Reader) []string {
	if rd != nil && (rd.Kind == KindAgent || rd.Kind == KindTerminal) {
		if s := r.seatForLocked(rd); s != nil {
			return []string{s.InstanceID}
		}
		return nil
	}
	ids := make([]string, 0, len(r.seats))
	for _, s := range r.seats {
		ids = append(ids, s.InstanceID)
	}
	return ids
}

// FromThisMachine reports whether an event was written by any of this
// machine's seats in the room.
func (r *Room) FromThisMachine(evt client.Event) bool {
	id := realtime.EventInstanceID(evt)
	if id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.seats {
		if s.InstanceID == id {
			return true
		}
	}
	return false
}

// ReapSeats leaves every seat whose agent readers have all been gone for
// longer than grace, as long as another seat in the room is still in use: a
// harness that closed should not stay present in the room on the strength of
// its sibling. When every seat is idle the room itself idles out (the daemon's
// own rule), so nothing is reaped here. Returns the labels left.
func (r *Room) ReapSeats(ctx context.Context, grace time.Duration) []string {
	now := r.deps.now()
	r.mu.Lock()
	inUse := map[string]bool{}
	for _, rd := range r.readers {
		if rd.Connected && rd.Kind == KindAgent {
			if s := r.seatForLocked(rd); s != nil {
				inUse[s.Label] = true
			}
		}
	}
	if len(inUse) == 0 {
		r.mu.Unlock()
		return nil
	}
	var gone []*Seat
	for label, s := range r.seats {
		if inUse[label] {
			s.idleSince = time.Time{}
			continue
		}
		if s.idleSince.IsZero() {
			s.idleSince = now
			continue
		}
		if now.Sub(s.idleSince) >= grace {
			gone = append(gone, s)
			delete(r.seats, label)
		}
	}
	if _, ok := r.seats[r.primary]; !ok {
		r.promoteLocked()
	}
	r.mu.Unlock()
	labels := make([]string, 0, len(gone))
	for _, s := range gone {
		_ = s.client.Leave(ctx)
		labels = append(labels, s.Label)
	}
	sort.Strings(labels)
	return labels
}

// promoteLocked makes the first remaining seat (by label) the primary.
func (r *Room) promoteLocked() {
	labels := make([]string, 0, len(r.seats))
	for l := range r.seats {
		labels = append(labels, l)
	}
	if len(labels) == 0 {
		return
	}
	sort.Strings(labels)
	p := r.seats[labels[0]]
	// r.Config stays as opened (the room's identity and the watch's
	// credential); Snapshot persists the primary seat's own config.
	r.primary, r.client, r.AgentInstanceID = p.Label, p.client, p.InstanceID
}

// RestoreRoom rejoins a room from persisted state (the daemon restarted). The
// readers come back with their cursors; the token is the stored one.
//
// THE RING IS BACKFILLED FROM THE SERVER. Cursors persist; the event ring does
// not, and a reader's untold set is derived from the ring. Without this, an
// item that arrived seconds before a stop, or during the gap before the next
// daemon came up, vanished from the person's view for good (measured
// 2026-09-21: a teammate's message posted one second before `landfall daemon
// stop` never reached the person, while the same run re-attached within the
// same second). `GetUpdates(since)` returns raw events, exactly what the ring
// holds, so the untold sets come back as if the daemon had never stopped. The
// backfill is quiet: no doorbell, no notification for what is being restored.
func RestoreRoom(ctx context.Context, st RoomState, deps Deps) (*Room, error) {
	r, err := OpenRoom(ctx, st.Config, deps)
	if err != nil {
		return nil, err
	}
	for _, sc := range st.Seats {
		if _, serr := r.EnsureSeat(ctx, sc); serr != nil {
			deps.log("could not rejoin " + st.Config.IncidentID + " as " + SeatLabel(sc) + ": " + serr.Error())
		}
	}
	r.mu.Lock()
	r.CwdAllowed = st.CwdAllowed
	for _, h := range st.Links {
		r.addLinkLocked(h)
	}
	lowest := int64(-1)
	first := true
	for name, rd := range st.Readers {
		cp := *rd
		cp.Connected = false
		r.readers[name] = &cp
		if first || cp.Cursor < lowest {
			lowest = cp.Cursor
			first = false
		}
	}
	r.mu.Unlock()
	if len(st.Readers) > 0 {
		since := lowest
		if since < 0 {
			since = 0
		}
		if events, uerr := r.client.GetUpdates(ctx, since); uerr == nil {
			for _, e := range events {
				r.enqueueQuiet(e)
			}
		} else {
			deps.log("could not backfill " + st.Config.IncidentID + " since " + fmt.Sprint(since) + ": " + uerr.Error())
		}
	}
	return r, nil
}

// enqueueQuiet stores an event in the ring without ringing anything: for the
// backfill after a restart, where the events are not news to the surfaces,
// only to the untold sets.
func (r *Room) enqueueQuiet(evt client.Event) {
	if evt.Seq == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.Seq != nil && *e.Seq == *evt.Seq {
			return
		}
	}
	r.events = append(r.events, evt)
	sort.SliceStable(r.events, func(i, j int) bool { return r.events[i].SeqOr(0) < r.events[j].SeqOr(0) })
	for len(r.events) > RingMax {
		r.events = r.events[1:]
	}
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
				// One beat per seat: each harness's agent session is present in
				// the room on its own account.
				r.mu.Lock()
				clients := make([]session.EdgeClient, 0, len(r.seats))
				for _, s := range r.seats {
					clients = append(clients, s.client)
				}
				r.mu.Unlock()
				for _, cl := range clients {
					bctx, c := context.WithTimeout(context.Background(), interval)
					_ = cl.Heartbeat(bctx, "investigating")
					c()
				}
			}
		}
	}()
	if r.deps.Watch != nil {
		// No echo suppression at the socket: with a seat per harness, what is
		// one harness's own write is a sibling harness's news. Each reader's
		// untold set leaves out its own seat's writes instead (Untold).
		r.stopWatch = r.deps.Watch(roomCtx, r.Config, func() string { return "" }, r.enqueue)
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
	if !isPlumbing(evt.Type) && !isOwn(evt, r.ownIDsLocked(nil)) {
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
	if narrate.TouchesAttention(evt.Type) {
		r.mu.Lock()
		for _, st := range r.seats {
			st.attentionStale = true
		}
		r.mu.Unlock()
	}
	if r.deps.OnEvent != nil {
		r.deps.OnEvent(r, evt)
	}
}

// Attention is the room's attention projection for this machine's agent
// identity, as current as one read within `budget` can make it. A read that
// fails or times out returns the last snapshot (nil if there never was one):
// a hook must never block on it, and "unknown" reads as "nothing owed" there,
// which is the recoverable direction.
func (r *Room) Attention(ctx context.Context, budget time.Duration) *client.Attention {
	r.mu.Lock()
	label := r.primary
	r.mu.Unlock()
	return r.AttentionFor(ctx, label, budget)
}

// AttentionFor is Attention for one seat's agent identity: what the room waits
// on from Claude Code is not what it waits on from Codex.
func (r *Room) AttentionFor(ctx context.Context, label string, budget time.Duration) *client.Attention {
	r.mu.Lock()
	seat, ok := r.seats[label]
	if !ok {
		seat = r.seats[r.primary]
	}
	if seat == nil {
		r.mu.Unlock()
		return nil
	}
	stale, have, cl := seat.attentionStale, seat.attention, seat.client
	r.mu.Unlock()
	if have != nil && !stale {
		return have
	}
	if budget <= 0 {
		budget = time.Second
	}
	actx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	got, err := cl.GetAttention(actx)
	if err != nil {
		return have
	}
	r.mu.Lock()
	seat.attention, seat.attentionStale = got, false
	r.mu.Unlock()
	return got
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
	if rd.Kind == KindAgent {
		if s := r.seatForLocked(&rd); s != nil {
			s.idleSince = time.Time{}
		}
	}
	if existing, ok := r.readers[rd.Name]; ok {
		existing.Connected = true
		existing.LastSeenAt = now
		if rd.Host != "" {
			existing.Host = rd.Host
		}
		if rd.Seat != "" {
			existing.Seat = rd.Seat
		}
		if rd.Harness != "" {
			existing.Harness = rd.Harness
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
	rd, ok := r.readers[name]
	if ok {
		rd.Connected = false
		rd.LastSeenAt = r.deps.now()
	}
	if ok && rd.Kind == KindAgent {
		if s := r.seatForLocked(rd); s != nil {
			still := false
			for _, other := range r.readers {
				if other.Connected && other.Kind == KindAgent && r.seatForLocked(other) == s {
					still = true
					break
				}
			}
			if !still {
				s.idleSince = r.deps.now()
			}
		}
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
	return Untold(r.events, rd.Cursor, r.ownIDsLocked(rd)...)
}

// untoldForAll is the union of several readers' untold sets (seq-deduped,
// addressed first), the lowest of their cursors, and the seat label whose
// attention speaks for them (the first reader's).
func (r *Room) untoldForAll(names []string) ([]client.Event, int64, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cursor := int64(-1)
	seat := r.primary
	seen := map[int64]bool{}
	var merged []client.Event
	for i, name := range names {
		rd, ok := r.readers[name]
		if !ok {
			continue
		}
		if i == 0 {
			cursor = rd.Cursor
			if s := r.seatForLocked(rd); s != nil {
				seat = s.Label
			}
		} else if rd.Cursor < cursor {
			cursor = rd.Cursor
		}
		for _, e := range Untold(r.events, rd.Cursor, r.ownIDsLocked(rd)...) {
			if !seen[*e.Seq] {
				seen[*e.Seq] = true
				merged = append(merged, e)
			}
		}
	}
	if len(names) > 1 {
		merged = Untold(merged, -1)
	}
	return merged, cursor, seat
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
// this reader's cursor (or since `from`, when the agent asked for an explicit
// sinceSeq), and the cursor moved to what was delivered. This is what a front
// end renders onto a tool result in place of FlushPending, and what
// get_updates reads in daemon mode, so the two share one cursor per harness
// session. The read goes out under the reader's own seat, so the server
// classifies it for that agent (its own writes are routine to it, a sibling
// harness's are not). Returns the since the read used.
func (r *Room) Delta(ctx context.Context, name string, from *int64) (*client.FrameDelta, int64, error) {
	r.mu.Lock()
	rd, ok := r.readers[name]
	if !ok {
		r.mu.Unlock()
		return nil, -1, errors.New("no reader " + name)
	}
	since := rd.Cursor
	if from != nil {
		since = *from
	}
	cl := r.client
	if s := r.seatForLocked(rd); s != nil {
		cl = s.client
	}
	r.mu.Unlock()
	delta, err := cl.GetContextDelta(ctx, since)
	if err != nil {
		return nil, since, err
	}
	if delta != nil && delta.ToVersion != nil {
		_, _ = r.Advance(name, KindAgent, *delta.ToVersion)
	}
	return delta, since, nil
}

// Client is the primary seat's HTTP client (each front end's tools reuse its
// own seat's instance id through SetAgentInstanceID on their own client).
func (r *Room) Client() session.EdgeClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client
}

// Close stops presence and live watch and leaves the room.
func (r *Room) Close(ctx context.Context) {
	if r.stopWatch != nil {
		r.stopWatch()
	}
	if r.cancel != nil {
		r.cancel()
	}
	for _, s := range r.Seats() {
		_ = s.client.Leave(ctx)
	}
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
	var seats []client.Config
	for _, s := range r.seatsLocked() {
		if s.Label != r.primary {
			seats = append(seats, s.Config)
		}
	}
	var links []string
	for h := range r.links {
		links = append(links, h)
	}
	sort.Strings(links)
	cfg := r.Config
	if p, ok := r.seats[r.primary]; ok {
		cfg = p.Config
	}
	return RoomState{Config: cfg, CwdAllowed: r.CwdAllowed, Readers: readers, Seats: seats, Links: links, SavedAt: r.deps.now()}
}

// frameCursor is the frame's as-of seq as a cursor, -1 when absent — the same
// reading the front end already makes (narrate.FrameCursor).
func frameCursor(f *client.ContextFrame) int64 { return narrate.FrameCursor(f) }
