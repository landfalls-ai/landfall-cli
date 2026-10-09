package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func liveDaemon(t *testing.T) (*Daemon, *fakeWire, *clock) {
	t.Helper()
	wire := &fakeWire{}
	d, _ := testDaemon(t, &fakeEdge{}, wire)
	clk := &clock{t: time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)}
	d.opts.Deps.Now = clk.now
	return d, wire, clk
}

func attachAgentAt(t *testing.T, d *Daemon, ws, harness, pid string) Response {
	t.Helper()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t", AgentLabel: "Claude Code"}
	res := d.handler.Handle(context.Background(), Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{
		Name: harness + ":" + ws + ":" + pid, Kind: "agent", Host: harness, WorkspaceKey: ws, Harness: harness,
	}})
	if !res.OK {
		t.Fatalf("attach: %s", res.Error)
	}
	return res
}

// attachFromMod is what `landfall join --incident` sends: the person's own
// terminal reader for a workspace and harness, with no agent beside it.
func attachFromMod(t *testing.T, d *Daemon, ws, harness string) Response {
	t.Helper()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "room-token", HumanActorID: "u-alice", AgentLabel: "Claude Code"}
	res := d.handler.Handle(context.Background(), Request{Op: "attach", Room: &cfg, Link: "hash-1", Reader: &ReaderSpec{
		Name: TerminalReaderNameFor(ws, harness), Kind: "terminal", WorkspaceKey: ws, Harness: harness,
	}})
	if !res.OK {
		t.Fatalf("attach from the mod: %s", res.Error)
	}
	return res
}

func peekOne(t *testing.T, d *Daemon, ws, harness string) RoomView {
	t.Helper()
	p := d.handler.Handle(context.Background(), Request{Op: "peek", WorkspaceKey: ws, Harness: harness})
	if !p.OK || len(p.Rooms) != 1 {
		t.Fatalf("peek: %+v", p)
	}
	return p.Rooms[0]
}

// The wall: a Beacon widget is named by the request that carried its title,
// an edge widget and a pin by their own rows; refreshes, failures, unpins and
// a saved arrangement move widgetSeq without making anything new.
func TestPeekCarriesTheWallsNewestChange(t *testing.T) {
	d, wire, _ := liveDaemon(t)
	attachAgentAt(t, d, "ws", "claude-code", "1")

	if v := peekOne(t, d, "ws", "claude-code"); v.WidgetSeq != 0 || v.NewestWidget != nil {
		t.Fatalf("no widget yet: seq %d newest %+v", v.WidgetSeq, v.NewestWidget)
	}
	wire.emit(client.Event{Seq: seq(229), Type: "agent.widget.requested", ActorType: "agent", Payload: map[string]any{"widgetId": "w1", "widgetType": "chart", "title": "5xx by target group"}})
	wire.emit(client.Event{Seq: seq(230), Type: "agent.query", ActorType: "agent"})
	if v := peekOne(t, d, "ws", "claude-code"); v.WidgetSeq != 0 || v.NewestWidget != nil {
		t.Fatalf("a request is a build starting, not the wall changing: seq %d newest %+v", v.WidgetSeq, v.NewestWidget)
	}
	wire.emit(client.Event{Seq: seq(231), Type: "agent.widget.executed", ActorType: "agent", Payload: map[string]any{"widgetId": "w1", "type": "chart", "status": "rendered"}})
	v := peekOne(t, d, "ws", "claude-code")
	want := WidgetView{Seq: 231, Title: "5xx by target group", Type: "chart", By: "Beacon"}
	if v.WidgetSeq != 231 || v.NewestWidget == nil || *v.NewestWidget != want {
		t.Fatalf("landed widget: seq %d newest %+v, want %+v", v.WidgetSeq, v.NewestWidget, want)
	}

	// A refresh re-reads the wall; nothing is new on it.
	wire.emit(client.Event{Seq: seq(232), Type: "agent.widget.refreshed", ActorType: "agent", Payload: map[string]any{"widgetId": "w1"}})
	wire.emit(client.Event{Seq: seq(233), Type: "agent.widget.executed", ActorType: "agent", Payload: map[string]any{"widgetId": "w1", "type": "chart"}})
	if v := peekOne(t, d, "ws", "claude-code"); v.WidgetSeq != 233 || v.NewestWidget.Seq != 231 {
		t.Fatalf("after a refresh: seq %d newest %+v", v.WidgetSeq, v.NewestWidget)
	}

	// A teammate's edge widget, then the person pinning it.
	wire.emit(client.Event{Seq: seq(240), Type: "edge.widget", ActorType: "human", Payload: map[string]any{"widgetType": "stat", "title": "p95 latency", "displayName": "bob", "humanActorId": "h-bob", "data": map[string]any{"value": 840}}})
	if v := peekOne(t, d, "ws", "claude-code"); v.NewestWidget == nil || *v.NewestWidget != (WidgetView{Seq: 240, Title: "p95 latency", Type: "stat", By: "bob", Scope: "person", HumanActorID: "h-bob"}) {
		t.Fatalf("edge widget: %+v", v.NewestWidget)
	}
	wire.emit(client.Event{Seq: seq(241), Type: "widget.pinned", ActorType: "human", Payload: map[string]any{"sourceSeq": float64(240), "displayName": "alice"}})
	if v := peekOne(t, d, "ws", "claude-code"); v.WidgetSeq != 241 || *v.NewestWidget != (WidgetView{Seq: 241, Title: "p95 latency", Type: "stat", By: "alice"}) {
		t.Fatalf("pin: seq %d newest %+v", v.WidgetSeq, v.NewestWidget)
	}
	wire.emit(client.Event{Seq: seq(242), Type: "widget.unpinned", Payload: map[string]any{"sourceSeq": float64(240)}})
	wire.emit(client.Event{Seq: seq(243), Type: "canvas.layout.saved", Payload: map[string]any{"scope": "shared", "displayName": "alice"}})
	wire.emit(client.Event{Seq: seq(244), Type: "agent.widget.failed", ActorType: "agent", Payload: map[string]any{"widgetId": "w2", "error": "no data"}})
	if v := peekOne(t, d, "ws", "claude-code"); v.WidgetSeq != 244 || v.NewestWidget.Seq != 241 {
		t.Fatalf("unpin, layout, failure: seq %d newest %+v", v.WidgetSeq, v.NewestWidget)
	}
	// A pin is the shared wall's, not the pinner's own dashboard's.
	if v := peekOne(t, d, "ws", "claude-code"); v.NewestWidget.Scope != "" || v.NewestWidget.HumanActorID != "" {
		t.Fatalf("a pin sits on the shared wall: %+v", v.NewestWidget)
	}
	// An event older than the newest (a late backfill) never takes its place.
	wire.emit(client.Event{Seq: seq(200), Type: "edge.widget", Payload: map[string]any{"widgetType": "stat", "title": "old"}})
	if v := peekOne(t, d, "ws", "claude-code"); v.WidgetSeq != 244 || v.NewestWidget.Seq != 241 {
		t.Fatalf("an older row moved the wall: seq %d newest %+v", v.WidgetSeq, v.NewestWidget)
	}
}

// A widget title is printed on the person's screen: control characters and
// runs of space are gone, and a long one is cut.
func TestWidgetTitlesAreCleaned(t *testing.T) {
	d, wire, _ := liveDaemon(t)
	attachAgentAt(t, d, "ws", "claude-code", "1")
	wire.emit(client.Event{Seq: seq(5), Type: "edge.widget", Payload: map[string]any{"widgetType": "stat", "title": "a\x1b[31m  red\n" + strings.Repeat("x", 300)}})
	v := peekOne(t, d, "ws", "claude-code")
	if strings.ContainsAny(v.NewestWidget.Title, "\x1b\n") || len([]rune(v.NewestWidget.Title)) > widgetTitleMax {
		t.Fatalf("title = %q", v.NewestWidget.Title)
	}
}

// lastEventAgoMs is measured from the last event or frame read; agent says
// whether THIS workspace's agent (and harness) is in the room, and since when.
func TestPeekSaysWhenTheRoomWasHeardAndWhetherTheAgentIsIn(t *testing.T) {
	d, wire, clk := liveDaemon(t)
	attachAgentAt(t, d, "ws", "claude-code", "1")
	v := peekOne(t, d, "ws", "claude-code")
	if v.LastEventAgoMs == nil || *v.LastEventAgoMs != 0 {
		t.Fatalf("the attach read the frame just now: %v", v.LastEventAgoMs)
	}
	if v.Agent == nil || !v.Agent.InRoom || v.Agent.Label != "claude-code" || v.Agent.SinceMs == nil || *v.Agent.SinceMs != 0 {
		t.Fatalf("agent: %+v", v.Agent)
	}
	clk.add(7 * time.Second)
	v = peekOne(t, d, "ws", "claude-code")
	if *v.LastEventAgoMs != 7000 || *v.Agent.SinceMs != 7000 {
		t.Fatalf("after 7 s: heard %d ago, agent in for %d", *v.LastEventAgoMs, *v.Agent.SinceMs)
	}
	// Plumbing is still the room talking.
	wire.emit(client.Event{Seq: seq(9), Type: "agent.query"})
	if v := peekOne(t, d, "ws", "claude-code"); *v.LastEventAgoMs != 0 {
		t.Fatalf("an event resets it: %d", *v.LastEventAgoMs)
	}

	// Another harness's agent in the same checkout is not this session's.
	if v := peekOne(t, d, "ws", "codex"); v.Agent == nil || v.Agent.InRoom {
		// codex has no terminal reader, so the peek fell back to the
		// workspace's readers; its own agent is still not in the room.
		t.Fatalf("codex's agent: %+v", v.Agent)
	}
	d.handler.Handle(context.Background(), Request{Op: "detach", RoomKey: RoomKey(client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1"}), ReaderName: "claude-code:ws:1"})
	v = peekOne(t, d, "ws", "claude-code")
	if v.Agent == nil || v.Agent.InRoom || v.Agent.SinceMs != nil || v.Agent.Label != "" {
		t.Fatalf("after the agent left: %+v", v.Agent)
	}
	body, _ := json.Marshal(v.Agent)
	if string(body) != `{"inRoom":false}` {
		t.Fatalf("an absent agent on the wire: %s", body)
	}
}

// A watch asks for every change: widget rows (plumbing to the hooks), this
// machine's own writes, and a reader arriving. The substantive stream every
// other subscriber reads is unchanged.
func TestSubscribeAllWakesForWhatTheSubstantiveStreamLeavesOut(t *testing.T) {
	d, wire, _ := liveDaemon(t)
	res := attachAgentAt(t, d, "ws", "claude-code", "1")
	room := d.room(res.RoomKey)
	all, stopAll := room.SubscribeAll()
	defer stopAll()
	subst, stopSubst := room.Subscribe()
	defer stopSubst()

	wire.emit(client.Event{Seq: seq(31), Type: "agent.widget.executed", ActorType: "agent", Payload: map[string]any{"widgetId": "w1"}})
	wire.emit(client.Event{Seq: seq(32), Type: "edge.widget", Payload: map[string]any{"agentInstanceId": res.AgentInstanceID, "title": "mine"}})
	for _, want := range []int64{31, 32} {
		select {
		case e := <-all:
			if e.SeqOr(-1) != want {
				t.Fatalf("all: got %d, want %d", e.SeqOr(-1), want)
			}
		case <-time.After(time.Second):
			t.Fatalf("SubscribeAll never woke for %d", want)
		}
	}
	select {
	case e := <-subst:
		t.Fatalf("the substantive stream must still leave out plumbing and own writes, got %+v", e)
	default:
	}

	attachAgentAt(t, d, "ws", "codex", "2")
	select {
	case e := <-all:
		if e.Type != ReaderChanged || e.Seq != nil {
			t.Fatalf("reader change: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("a reader arriving must wake a watch")
	}
}

// The room is on offer to an agent only when the PERSON put the folder in it
// from the mod, and only to the same workspace and harness.
func TestARoomIsOnOfferOnlyAfterThePersonJoinsFromTheMod(t *testing.T) {
	d, _, _ := liveDaemon(t)
	ctx := context.Background()
	// A link pasted to an agent creates the person's reader too, but that is
	// not an offer: a new session in that folder stays out until asked.
	attachAgentAt(t, d, "ws-link", "claude-code", "1")
	if r := d.handler.Handle(ctx, Request{Op: "adoptable", WorkspaceKey: "ws-link", Harness: "claude-code"}); !r.OK || r.Room != nil {
		t.Fatalf("a pasted link is not an offer: %+v", r)
	}

	attachFromMod(t, d, "ws-mod", "claude-code")
	r := d.handler.Handle(ctx, Request{Op: "adoptable", WorkspaceKey: "ws-mod", Harness: "claude-code"})
	// The room was opened by the link join above, so its session is that one.
	// The offer names the seat the person's terminal join holds, so the
	// adopting agent joins it instead of making a second one.
	if !r.OK || r.Room == nil || r.Room.Token != "t" || r.Room.IncidentID != "inc-1" || r.Room.AgentLabel != "Claude Code" || r.RoomKey == "" {
		t.Fatalf("the person's join is on offer with the room's own session: %+v", r)
	}
	for _, other := range []Request{
		{Op: "adoptable", WorkspaceKey: "ws-mod", Harness: "codex"},
		{Op: "adoptable", WorkspaceKey: "ws-other", Harness: "claude-code"},
		{Op: "adoptable", WorkspaceKey: "", Harness: "claude-code"},
	} {
		if r := d.handler.Handle(ctx, other); r.Room != nil {
			t.Fatalf("%+v must not be offered the room", other)
		}
	}
	// The agent's own attach beside it keeps the offer (a second window in
	// the same folder joins too).
	attachAgentAt(t, d, "ws-mod", "claude-code", "7")
	if r := d.handler.Handle(ctx, Request{Op: "adoptable", WorkspaceKey: "ws-mod", Harness: "claude-code"}); r.Room == nil {
		t.Fatal("an agent attaching beside the person's reader must not withdraw the offer")
	}
}

// await-room answers at once when a room is already on offer, and otherwise
// the moment the person joins.
func TestAwaitRoomAnswersWhenThePersonJoins(t *testing.T) {
	d, _, _ := liveDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan *Response, 1)
	go func() { got <- d.awaitRoom(ctx, nil, Request{WorkspaceKey: "ws", Harness: "claude-code"}) }()
	select {
	case r := <-got:
		t.Fatalf("answered with nothing on offer: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	began := time.Now()
	attachFromMod(t, d, "ws", "claude-code")
	select {
	case r := <-got:
		if r == nil || r.Room == nil || r.Room.Token != "room-token" {
			t.Fatalf("await-room: %+v", r)
		}
		if took := time.Since(began); took > 250*time.Millisecond {
			t.Fatalf("await-room took %s after the join", took)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("await-room never answered")
	}
	// Already on offer: at once.
	if r := d.awaitRoom(ctx, nil, Request{WorkspaceKey: "ws", Harness: "claude-code"}); r == nil || r.Room == nil {
		t.Fatalf("already on offer: %+v", r)
	}
	// A caller that goes away ends the wait with nothing.
	gone := make(chan struct{})
	done := make(chan *Response, 1)
	go func() { done <- d.awaitRoom(ctx, gone, Request{WorkspaceKey: "ws", Harness: "codex"}) }()
	close(gone)
	select {
	case r := <-done:
		if r != nil {
			t.Fatalf("gone: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("a caller that went away kept the wait open")
	}
}

// The offer survives a daemon restart: PersonJoined is persisted with the
// reader.
func TestTheOfferIsPersisted(t *testing.T) {
	d, _, _ := liveDaemon(t)
	attachFromMod(t, d, "ws", "claude-code")
	st := d.room(RoomKey(client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1"})).Snapshot()
	body, _ := json.Marshal(st)
	var back RoomState
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	if rd := back.Readers[TerminalReaderNameFor("ws", "claude-code")]; rd == nil || !rd.PersonJoined {
		t.Fatalf("persisted reader: %+v", rd)
	}
}

// Beacon's status and remediation widgets are requested once, before this
// daemon joined, and re-executed all through the run. The wall is read once
// when the room opens, so a re-execution keeps its title and is not news;
// a widget no event ever titled reads as its id made human, not "w-status".
// (Measured live: the band toasted "New on the wall: w-status".)
func TestTheWallBeforeJoiningIsRemembered(t *testing.T) {
	wire := &fakeWire{}
	edge := &fakeEdge{}
	edge.updates = func(since int64) []client.Event {
		if since != 0 {
			return nil
		}
		return []client.Event{
			{Seq: seq(6), Type: "agent.widget.requested", ActorType: "system", Payload: map[string]any{"title": "Investigation status", "widgetId": "w-status", "widgetType": "logView"}},
			{Seq: seq(7), Type: "agent.widget.executed", ActorType: "system", Payload: map[string]any{"widgetId": "w-status", "type": "logView", "status": "rendered"}},
			{Seq: seq(12), Type: "agent.widget.requested", ActorType: "system", Payload: map[string]any{"title": "Remediation: fast fix", "widgetId": "w-remediation", "widgetType": "logView"}},
			{Seq: seq(13), Type: "agent.widget.executed", ActorType: "system", Payload: map[string]any{"widgetId": "w-remediation", "type": "logView", "status": "rendered"}},
		}
	}
	d, _ := testDaemon(t, edge, wire)
	attachAgentAt(t, d, "ws", "claude-code", "1")
	room := d.rooms()[0]
	deadline := time.Now().Add(2 * time.Second)
	for {
		room.mu.Lock()
		meta := room.wall.byID.get("w-status")
		room.mu.Unlock()
		if meta != nil && meta.landed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the wall was never read when the room opened")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if v := peekOne(t, d, "ws", "claude-code"); v.WidgetSeq != 0 || v.NewestWidget != nil {
		t.Fatalf("the seed is memory only: seq %d newest %+v", v.WidgetSeq, v.NewestWidget)
	}

	// The status widget re-executes: the wall changed, nothing landed.
	wire.emit(client.Event{Seq: seq(56), Type: "agent.widget.executed", ActorType: "system", Payload: map[string]any{"widgetId": "w-status", "type": "logView", "status": "rendered"}})
	if v := peekOne(t, d, "ws", "claude-code"); v.WidgetSeq != 56 || v.NewestWidget != nil {
		t.Fatalf("a re-execution is not a new widget: seq %d newest %+v", v.WidgetSeq, v.NewestWidget)
	}

	// A widget whose request carried its title lands under that title.
	wire.emit(client.Event{Seq: seq(60), Type: "agent.widget.requested", ActorType: "agent", Payload: map[string]any{"title": "5xx by region", "widgetId": "w-geo", "widgetType": "geo"}})
	wire.emit(client.Event{Seq: seq(61), Type: "agent.widget.executed", ActorType: "agent", Payload: map[string]any{"widgetId": "w-geo", "type": "geo", "status": "rendered"}})
	if v := peekOne(t, d, "ws", "claude-code"); v.NewestWidget == nil || v.NewestWidget.Title != "5xx by region" {
		t.Fatalf("newest = %+v", v.NewestWidget)
	}

	// One that nothing titled reads as its id made human.
	wire.emit(client.Event{Seq: seq(62), Type: "agent.widget.executed", ActorType: "system", Payload: map[string]any{"widgetId": "w-error_budget", "type": "stat", "status": "rendered"}})
	if v := peekOne(t, d, "ws", "claude-code"); v.NewestWidget == nil || v.NewestWidget.Title != "Error budget" {
		t.Fatalf("newest = %+v", v.NewestWidget)
	}
}

func TestHumanWidgetID(t *testing.T) {
	for id, want := range map[string]string{
		"w-status":      "Status",
		"w-remediation": "Remediation",
		"widget_p99":    "P99",
		"error-rate":    "Error rate",
		"w-":            "W",
		"":              "Widget",
	} {
		if got := humanWidgetID(id); got != want {
			t.Errorf("humanWidgetID(%q) = %q, want %q", id, got, want)
		}
	}
}

// A widget built before the daemon joined is pinned after: the pin is still
// news, named from the wall the room read when it opened.
func TestAPinOfAWidgetFromBeforeJoiningLands(t *testing.T) {
	wire := &fakeWire{}
	edge := &fakeEdge{}
	edge.updates = func(since int64) []client.Event {
		if since != 0 {
			return nil
		}
		return []client.Event{
			{Seq: seq(6), Type: "edge.widget", ActorType: "human", Payload: map[string]any{"widgetType": "stat", "title": "Pool saturation", "displayName": "alice", "humanActorId": "h-alice"}},
		}
	}
	d, _ := testDaemon(t, edge, wire)
	attachAgentAt(t, d, "ws", "claude-code", "1")
	room := d.rooms()[0]
	deadline := time.Now().Add(2 * time.Second)
	for {
		room.mu.Lock()
		meta := room.wall.bySeq.get(6)
		room.mu.Unlock()
		if meta != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the wall was never read when the room opened")
		}
		time.Sleep(5 * time.Millisecond)
	}
	wire.emit(client.Event{Seq: seq(57), Type: "widget.pinned", ActorType: "human", Payload: map[string]any{"sourceSeq": float64(6), "displayName": "bob"}})
	v := peekOne(t, d, "ws", "claude-code")
	if v.WidgetSeq != 57 || v.NewestWidget == nil || *v.NewestWidget != (WidgetView{Seq: 57, Title: "Pool saturation", Type: "stat", By: "bob"}) {
		t.Fatalf("pin: seq %d newest %+v", v.WidgetSeq, v.NewestWidget)
	}
}
