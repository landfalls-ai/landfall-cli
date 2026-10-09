package daemon

// live.go — what the room daemon tells a watcher (the Claude Code mod, through
// `landfall watch`) beyond the untold set: the wall's newest change, how long
// since the room was last heard from, and whether this checkout's agent is in
// the room (specs/20261008-150000-edge-components/live.md, FR-L1 to FR-L5).
//
// THE WALL. Widget events are room machinery for the hooks (realtime.IsPlumbing
// keeps them out of every digest and every Stop-hook refusal, which is right:
// an agent should not be interrupted because a chart was drawn). A person
// looking at the wall is a different reader: a widget that lands, refreshes,
// fails, is pinned or unpinned, an edge widget, and the shared arrangement
// being saved all change what the wall shows. The room keeps the newest seq of
// any of those (widgetSeq) and the newest widget that LANDED (newestWidget),
// with its title taken from the event that named it: a Beacon widget's title
// rides on agent.widget.requested (and validated), never on executed, so the
// room remembers titles by widget id; an edge widget carries its own, and a
// pin names the edge widget's seq, so those are remembered by seq.
//
// WHAT "LANDED" MEANS. The first agent.widget.executed or rendered of a widget
// id, every edge.widget (each is a new reading, even under an old title; it
// lands on its author's own dashboard, not the shared wall, which the view
// says with its scope), and a pin (the card now sits on the shared wall). A refresh, a failure, an unpin
// and a saved arrangement move widgetSeq only: the wall is re-read, but nothing
// is new on it.
//
// THE ADOPTION OFFER (FR-L5). `landfall join --incident` is the person putting
// THIS folder into a room from the mod, with no agent involved: it attaches a
// terminal reader directly. That reader is marked PersonJoined, and the room
// is then on offer to any `landfall serve` of the same workspace and harness
// that is not in a room yet (ops `adoptable` and `await-room`). A terminal
// reader created beside an agent's own attach (a pasted link) is not marked,
// so a new agent session in a folder whose agent once joined by link does not
// silently join it again: that stays the person's call.

import (
	"context"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

// WidgetView is the newest widget that landed on the room's wall.
type WidgetView struct {
	Seq   int64  `json:"seq"`
	Title string `json:"title,omitempty"`
	Type  string `json:"type,omitempty"`
	By    string `json:"by,omitempty"`
	// Scope is "person" for a widget on one person's own dashboard (an
	// edge.widget: share_with_room), whose humanActorId is HumanActorID; empty
	// for the shared wall (a Beacon widget, a pin).
	Scope        string `json:"scope,omitempty"`
	HumanActorID string `json:"humanActorId,omitempty"`
}

// AgentView is this workspace's agent session in a room, as a watcher sees it:
// a `landfall serve` of this checkout (and harness) attached as an agent
// reader.
type AgentView struct {
	InRoom bool   `json:"inRoom"`
	Label  string `json:"label,omitempty"`
	// SinceMs is how long the agent has been in the room; omitted when it is
	// not.
	SinceMs *int64 `json:"sinceMs,omitempty"`
}

// widgetShapingTypes are the event types after which the wall shows something
// different (live.md FR-L2).
var widgetShapingTypes = map[string]bool{
	"agent.widget.executed":  true,
	"agent.widget.rendered":  true,
	"agent.widget.refreshed": true,
	"agent.widget.failed":    true,
	"edge.widget":            true,
	"widget.pinned":          true,
	"widget.unpinned":        true,
	"canvas.layout.saved":    true,
}

// IsWidgetShaping reports whether an event changes what the wall shows.
func IsWidgetShaping(eventType string) bool { return widgetShapingTypes[eventType] }

// widgetMemoMax bounds each of the room's widget memories.
const widgetMemoMax = 256

type widgetMeta struct {
	title, typ, by string
	landed         bool
}

// widgetMemo is a small map that forgets its oldest key past widgetMemoMax.
type widgetMemo[K comparable] struct {
	m     map[K]*widgetMeta
	order []K
}

func (w *widgetMemo[K]) get(k K) *widgetMeta {
	if w.m == nil {
		return nil
	}
	return w.m[k]
}

func (w *widgetMemo[K]) put(k K) *widgetMeta {
	if w.m == nil {
		w.m = map[K]*widgetMeta{}
	}
	if have, ok := w.m[k]; ok {
		return have
	}
	meta := &widgetMeta{}
	w.m[k] = meta
	w.order = append(w.order, k)
	for len(w.order) > widgetMemoMax {
		delete(w.m, w.order[0])
		w.order = w.order[1:]
	}
	return meta
}

// wallState is what the room remembers of its wall.
type wallState struct {
	seq    int64 // newest widget-shaping seq; 0 when none
	newest *WidgetView
	byID   widgetMemo[string] // agent.widget.* by widgetId
	bySeq  widgetMemo[int64]  // edge.widget by its own seq, for pins
}

// noteHeardLocked records that the room was heard from just now.
func (r *Room) noteHeardLocked() { r.lastHeard = r.deps.now() }

// noteWallLocked folds one stored event into the wall memory. Every event
// passes through: agent.widget.requested and validated are not widget-shaping
// themselves, but they carry the title a later executed lacks.
func (r *Room) noteWallLocked(evt client.Event) {
	if evt.Seq == nil {
		return
	}
	seq := *evt.Seq
	p := evt.Payload
	w := &r.wall
	switch {
	case strings.HasPrefix(evt.Type, "agent.widget."):
		id, _ := p["widgetId"].(string)
		if id == "" {
			break
		}
		meta := w.byID.put(id)
		if t := widgetText(p, "title"); t != "" {
			meta.title = t
		}
		if t := firstText(p, "widgetType", "type"); t != "" {
			meta.typ = t
		}
		if by := widgetBy(evt); by != "" {
			meta.by = by
		}
		if (evt.Type == "agent.widget.executed" || evt.Type == "agent.widget.rendered") && !meta.landed {
			meta.landed = true
			title := meta.title
			if title == "" {
				title = humanWidgetID(id)
			}
			r.landWidgetLocked(WidgetView{Seq: seq, Title: title, Type: meta.typ, By: meta.by})
		}
	case evt.Type == "edge.widget":
		meta := w.bySeq.put(seq)
		meta.title = widgetText(p, "title")
		meta.typ = firstText(p, "widgetType", "type")
		meta.by = widgetBy(evt)
		meta.landed = true
		who, _ := p["humanActorId"].(string)
		r.landWidgetLocked(WidgetView{Seq: seq, Title: meta.title, Type: meta.typ, By: meta.by, Scope: "person", HumanActorID: who})
	case evt.Type == "widget.pinned":
		v := WidgetView{Seq: seq, By: widgetBy(evt)}
		if src := payloadSeq(p, "sourceSeq"); src > 0 {
			if meta := w.bySeq.get(src); meta != nil {
				v.Title, v.Type = meta.title, meta.typ
			}
		}
		if v.Title == "" {
			v.Title = widgetText(p, "title")
		}
		r.landWidgetLocked(v)
	}
	if IsWidgetShaping(evt.Type) && seq > w.seq {
		w.seq = seq
	}
}

// THE WALL BEFORE THE DAEMON JOINED. A Beacon widget's title rides only on
// its agent.widget.requested, and Beacon re-executes its status and
// remediation widgets all through a run, long after the request the daemon
// never saw: those re-executions used to land as new widgets titled with
// their ids ("w-status"). seedWallAsync reads the room's timeline once when
// the room opens and remembers every widget already on the wall, with its
// title, as landed, so a later re-execution is the wall changing, not a new
// widget. Only the memory is seeded: widgetSeq and newestWidget stay as of
// the daemon's own arrival.

// seedWallAsync reads the room's timeline once, in the background, and
// remembers the widgets already on its wall.
func (r *Room) seedWallAsync(ctx context.Context) {
	r.mu.Lock()
	cl := r.client
	r.mu.Unlock()
	if cl == nil {
		return
	}
	go func() {
		rctx, cancel := context.WithTimeout(ctx, personReadBudget)
		defer cancel()
		events, err := cl.GetUpdates(rctx, 0)
		if err != nil {
			r.deps.log("could not read the wall of " + r.Config.IncidentID + ": " + err.Error())
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, e := range events {
			r.seedWallLocked(e)
		}
	}()
}

// seedWallLocked remembers one event of the wall as it stood before the
// daemon joined: titles, types and authors, and which widgets have landed.
func (r *Room) seedWallLocked(evt client.Event) {
	if evt.Seq == nil {
		return
	}
	p := evt.Payload
	w := &r.wall
	switch {
	case strings.HasPrefix(evt.Type, "agent.widget."):
		id, _ := p["widgetId"].(string)
		if id == "" {
			return
		}
		meta := w.byID.put(id)
		if t := widgetText(p, "title"); t != "" {
			meta.title = t
		}
		if t := firstText(p, "widgetType", "type"); t != "" && meta.typ == "" {
			meta.typ = t
		}
		if by := widgetBy(evt); by != "" && meta.by == "" {
			meta.by = by
		}
		if evt.Type == "agent.widget.executed" || evt.Type == "agent.widget.rendered" {
			meta.landed = true
		}
	case evt.Type == "edge.widget":
		meta := w.bySeq.put(*evt.Seq)
		if meta.title == "" {
			meta.title = widgetText(p, "title")
			meta.typ = firstText(p, "widgetType", "type")
			meta.by = widgetBy(evt)
		}
		meta.landed = true
	}
}

// humanWidgetID is a widget's id as a title, for the rare widget whose title
// no event carried: "w-status" reads "Status", "error_rate" "Error rate".
func humanWidgetID(id string) string {
	s := widgetText(map[string]any{"t": id}, "t")
	for _, prefix := range []string{"w-", "w_", "widget-", "widget_"} {
		if rest := strings.TrimPrefix(s, prefix); rest != s && rest != "" {
			s = rest
			break
		}
	}
	s = strings.Join(strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(s)), " ")
	if s == "" {
		return "Widget"
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// landWidgetLocked makes v the newest widget unless a newer one already is
// (events can arrive out of order: a backfill, a reconnect).
func (r *Room) landWidgetLocked(v WidgetView) {
	if r.wall.newest != nil && r.wall.newest.Seq >= v.Seq {
		return
	}
	cp := v
	r.wall.newest = &cp
}

// WallView is the wall's newest widget-shaping seq and its newest landed
// widget (nil when none has landed since the daemon joined).
func (r *Room) WallView() (int64, *WidgetView) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var newest *WidgetView
	if r.wall.newest != nil {
		cp := *r.wall.newest
		newest = &cp
	}
	return r.wall.seq, newest
}

// LastEventAgoMs is how long since the daemon last heard from the room (an
// event over the realtime socket, a backfill, or a frame read); nil when it
// never has.
func (r *Room) LastEventAgoMs() *int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastHeard.IsZero() {
		return nil
	}
	ms := r.deps.now().Sub(r.lastHeard).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return &ms
}

// AgentFor is this workspace's agent in the room: a connected agent reader
// of the workspace and, when one is named, the harness. The label is the
// harness key (the seat label when the reader names none); since is from the
// earliest such reader's attach.
func (r *Room) AgentFor(workspaceKey, harness string) AgentView {
	r.mu.Lock()
	defer r.mu.Unlock()
	var first *Reader
	for _, rd := range r.readers {
		if rd.Kind != KindAgent || !rd.Connected || workspaceKey == "" || rd.WorkspaceKey != workspaceKey {
			continue
		}
		if harness != "" && rd.Harness != "" && rd.Harness != harness {
			continue
		}
		if first == nil || rd.AttachedAt.Before(first.AttachedAt) {
			first = rd
		}
	}
	if first == nil {
		return AgentView{}
	}
	label := first.Harness
	if label == "" {
		label = first.Seat
	}
	since := r.deps.now().Sub(first.AttachedAt).Milliseconds()
	if since < 0 {
		since = 0
	}
	return AgentView{InRoom: true, Label: label, SinceMs: &since}
}

// widgetTitleMax bounds a widget title on the stream.
const widgetTitleMax = 120

func widgetText(p map[string]any, key string) string {
	s, _ := p[key].(string)
	s = strings.Join(strings.Fields(narrate.Printable(s)), " ")
	if r := []rune(s); len(r) > widgetTitleMax {
		s = strings.TrimSpace(string(r[:widgetTitleMax-1])) + "…"
	}
	return s
}

func firstText(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := widgetText(p, k); s != "" {
			return s
		}
	}
	return ""
}

// widgetBy is who put a widget on the wall, when the event says: the person's
// name (an edge widget, a pin), their agent's label, or Beacon for the
// platform's own widget rows (written by an agent or the system with no edge
// agent behind them).
func widgetBy(evt client.Event) string {
	p := evt.Payload
	if s := firstText(p, "displayName", "edgeAgentLabel"); s != "" {
		return s
	}
	if strings.HasPrefix(evt.Type, "agent.widget.") && (evt.ActorType == "agent" || evt.ActorType == "system") {
		if id, _ := p["agentInstanceId"].(string); id == "" {
			return "Beacon"
		}
	}
	return ""
}

func payloadSeq(p map[string]any, key string) int64 {
	switch v := p[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// --- the adoption offer -------------------------------------------------------

// adoptableRoom is the room this workspace's person joined from the mod for
// this harness (the newest, when there are several), and its session config;
// nil when there is none.
func (d *Daemon) adoptableRoom(workspaceKey, harness string) (*Room, client.Config) {
	if workspaceKey == "" {
		return nil, client.Config{}
	}
	name := TerminalReaderNameFor(workspaceKey, harness)
	var best *Room
	var bestAt time.Time
	var bestSeat string
	for _, room := range d.rooms() {
		rd, ok := room.Reader(name)
		if !ok || rd.Kind != KindTerminal || !rd.PersonJoined {
			continue
		}
		if best == nil || rd.LastSeenAt.After(bestAt) {
			best, bestAt, bestSeat = room, rd.LastSeenAt, rd.Seat
		}
	}
	if best == nil {
		return nil, client.Config{}
	}
	// The offer names the seat the terminal join holds, so the adopting agent
	// joins that seat and not a second one under its own label (seats are
	// keyed by label). Empty when the reader predates seats: the agent then
	// uses its own label, as before.
	cfg := best.SessionConfig()
	cfg.AgentLabel = bestSeat
	return best, cfg
}

// adoptable answers `adoptable`: the room on offer to an agent of this
// workspace and harness, or OK with no room.
func (d *Daemon) adoptable(req Request) Response {
	res := ok()
	if room, cfg := d.adoptableRoom(req.WorkspaceKey, req.Harness); room != nil {
		res.RoomKey, res.Room = room.Key, &cfg
	}
	return res
}

// offered is closed and replaced each time a person joins a room from the
// mod, waking every `await-room`.
func (d *Daemon) offeredCh() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.offered == nil {
		d.offered = make(chan struct{})
	}
	return d.offered
}

func (d *Daemon) announceOffer() {
	d.mu.Lock()
	if d.offered != nil {
		close(d.offered)
	}
	d.offered = make(chan struct{})
	d.mu.Unlock()
}

// awaitRoom blocks until a room is on offer to this workspace and harness
// (answering at once when one already is), the caller goes away, or ctx ends.
// Nil when it ended with no room.
func (d *Daemon) awaitRoom(ctx context.Context, gone <-chan struct{}, req Request) *Response {
	for {
		wake := d.offeredCh()
		if res := d.adoptable(req); res.Room != nil {
			return &res
		}
		select {
		case <-wake:
		case <-gone:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// ConnectionState is the room's realtime state, read under the room's lock
// (enqueue writes it from the socket's goroutine).
func (r *Room) ConnectionState() Connection {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Connection
}
