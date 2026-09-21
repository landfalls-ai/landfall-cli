package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// fakeEdge is the minimum EdgeClient the daemon touches.
type fakeEdge struct {
	mu      sync.Mutex
	joins   int
	leaves  int
	beats   int
	delta   func(since int64) *client.FrameDelta
	updates func(since int64) []client.Event
	frame   *client.ContextFrame
}

func (f *fakeEdge) Join(context.Context) (*client.JoinResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joins++
	return &client.JoinResult{AgentInstanceID: "inst-1"}, nil
}
func (f *fakeEdge) Leave(context.Context) error { f.mu.Lock(); f.leaves++; f.mu.Unlock(); return nil }
func (f *fakeEdge) Heartbeat(context.Context, string) error {
	f.mu.Lock()
	f.beats++
	f.mu.Unlock()
	return nil
}
func (f *fakeEdge) Contribute(context.Context, string, map[string]any) error { return nil }
func (f *fakeEdge) UploadArtifact(context.Context, string, string, string) (*client.ArtifactResult, error) {
	return nil, nil
}
func (f *fakeEdge) ListArtifacts(context.Context) (*client.ArtifactList, error)  { return nil, nil }
func (f *fakeEdge) OpenArtifact(context.Context, string) ([]byte, string, error) { return nil, "", nil }
func (f *fakeEdge) FlagContext(context.Context, int64, string, string) error     { return nil }
func (f *fakeEdge) PositionClaim(context.Context, int64, string, string) error   { return nil }
func (f *fakeEdge) StageClaim(context.Context, map[string]any) error             { return nil }
func (f *fakeEdge) GetBrief(context.Context) ([]client.Event, error)             { return nil, nil }
func (f *fakeEdge) GetUpdates(_ context.Context, since int64) ([]client.Event, error) {
	if f.updates != nil {
		return f.updates(since), nil
	}
	return nil, nil
}
func (f *fakeEdge) GetContextFrame(context.Context) (*client.ContextFrame, error) {
	if f.frame != nil {
		return f.frame, nil
	}
	return &client.ContextFrame{}, nil
}
func (f *fakeEdge) GetContextDelta(_ context.Context, since int64) (*client.FrameDelta, error) {
	if f.delta != nil {
		return f.delta(since), nil
	}
	v := since
	return &client.FrameDelta{ToVersion: &v}, nil
}
func (f *fakeEdge) SearchContext(context.Context, string) (*client.SearchResult, error) {
	return nil, nil
}
func (f *fakeEdge) GetAttention(context.Context) (*client.Attention, error)   { return nil, nil }
func (f *fakeEdge) GetDivergence(context.Context) (*client.Divergence, error) { return nil, nil }
func (f *fakeEdge) GetSignalCatalog(context.Context) ([]client.SignalCatalogEntry, error) {
	return nil, nil
}
func (f *fakeEdge) QuerySignals(context.Context, string, string, map[string]any, string, string) (client.SignalsQueryResult, error) {
	return client.SignalsQueryResult{}, nil
}
func (f *fakeEdge) AgentInstanceID() string { return "inst-1" }
func (f *fakeEdge) Config() client.Config   { return client.Config{} }

var _ session.EdgeClient = (*fakeEdge)(nil)

type fakeWire struct {
	mu   sync.Mutex
	push func(client.Event)
}

func (w *fakeWire) watch(_ context.Context, _ client.Config, _ func() string, onEvent func(client.Event)) func() {
	w.mu.Lock()
	w.push = onEvent
	w.mu.Unlock()
	return func() {}
}

func (w *fakeWire) emit(e client.Event) {
	w.mu.Lock()
	p := w.push
	w.mu.Unlock()
	if p != nil {
		p(e)
	}
}

func testDaemon(t *testing.T, edge *fakeEdge, wire *fakeWire) (*Daemon, hooks.Workspace) {
	t.Helper()
	dir := t.TempDir()
	// Unix socket paths are capped at 104 bytes on macOS and t.TempDir() is
	// long; the runtime dir goes under /tmp so the daemon socket can bind.
	rt, err := os.MkdirTemp("/tmp", "lfd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	ws := hooks.Workspace{Cwd: dir, Env: map[string]string{"XDG_RUNTIME_DIR": rt}}
	d := New(Options{
		Workspace: ws,
		IdleGrace: 200 * time.Millisecond,
		Deps: Deps{
			NewClient: func(client.Config) session.EdgeClient { return edge },
			Watch:     wire.watch,
			Heartbeat: time.Hour,
		},
	})
	return d, ws
}

func TestOneRoomTwoReadersTwoCursors(t *testing.T) {
	edge := &fakeEdge{}
	wire := &fakeWire{}
	d, _ := testDaemon(t, edge, wire)
	ctx := context.Background()
	h := d.handler
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}

	// Two agents attach to the same room: one Join, one presence identity.
	a := h.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	b := h.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "agent:ws:2", Kind: "agent", WorkspaceKey: "ws"}})
	if !a.OK || !b.OK || a.RoomKey != b.RoomKey {
		t.Fatalf("attach: %+v %+v", a, b)
	}
	if edge.joins != 1 {
		t.Fatalf("the room must be joined once for the machine, got %d joins", edge.joins)
	}

	// The room moves: a machinery row, a directed question, an admitted finding.
	wire.emit(client.Event{Seq: seq(10), Type: "agent.query"})
	wire.emit(client.Event{Seq: seq(11), Type: "chat.message", Payload: map[string]any{"text": "@alice can you confirm the TTL?"}})
	wire.emit(client.Event{Seq: seq(12), Type: "claim.admitted", Payload: map[string]any{"statement": "error rate climbed at 18:57Z"}})

	// The person's terminal (a hook process) peeks: two untold, addressed first.
	p := h.Handle(ctx, Request{Op: "peek", WorkspaceKey: "ws"})
	if !p.OK || len(p.Rooms) != 1 || p.Rooms[0].Count != 2 || len(p.Rooms[0].Digest) != 2 {
		t.Fatalf("peek: %+v", p)
	}
	if p.Rooms[0].Digest[0][:3] != "#11" {
		t.Fatalf("addressed message must come first, got %v", p.Rooms[0].Digest)
	}

	// The helper (reader b) pulls its delta: its cursor moves, nobody else's.
	edge.delta = func(since int64) *client.FrameDelta { v := int64(12); return &client.FrameDelta{ToVersion: &v} }
	if r := h.Handle(ctx, Request{Op: "delta", RoomKey: a.RoomKey, ReaderName: "agent:ws:2"}); !r.OK {
		t.Fatalf("delta: %+v", r)
	}
	p2 := h.Handle(ctx, Request{Op: "peek", WorkspaceKey: "ws"})
	if p2.Rooms[0].Count != 2 {
		t.Fatalf("the helper's read must not count as the person having been told; count = %d", p2.Rooms[0].Count)
	}
	room := d.room(a.RoomKey)
	if rd, _ := room.Reader("agent:ws:2"); rd.Cursor != 12 {
		t.Fatalf("helper cursor = %d, want 12", rd.Cursor)
	}
	if rd, _ := room.Reader("claude-code:ws:1"); rd.Cursor != 9 && rd.Cursor != -1 {
		// attach happened before any event: cursor is the room position at attach (-1 here)
		t.Fatalf("the other agent's cursor moved: %d", rd.Cursor)
	}

	// A hook delivered through #11 to the person: consume moves only the terminal reader.
	up := int64(11)
	c := h.Handle(ctx, Request{Op: "consume", RoomKey: a.RoomKey, ReaderName: TerminalReaderName("ws"), UpTo: &up})
	if !c.OK || c.Cursor == nil || *c.Cursor != 11 {
		t.Fatalf("consume: %+v", c)
	}
	p3 := h.Handle(ctx, Request{Op: "peek", WorkspaceKey: "ws"})
	if p3.Rooms[0].Count != 1 {
		t.Fatalf("after telling the person through #11, one item remains, got %d", p3.Rooms[0].Count)
	}
	// An agent may not move the person's cursor.
	bad := h.Handle(ctx, Request{Op: "consume", RoomKey: a.RoomKey, ReaderName: "agent:ws:2", UpTo: &up})
	if bad.OK {
		t.Fatal("consume must refuse to move an agent reader")
	}

	// Status line reads the person's count, not the machinery.
	s := h.Handle(ctx, Request{Op: "status", WorkspaceKey: "ws"})
	if !s.OK || s.Line == "" || s.Line != "🔴 landfall #inc-1 · 1 new" {
		t.Fatalf("status line = %q", s.Line)
	}

	// rooms lists both agents and the terminal.
	r := h.Handle(ctx, Request{Op: "rooms"})
	if len(r.Rooms) != 1 || len(r.Rooms[0].Readers) != 3 {
		t.Fatalf("rooms: %+v", r.Rooms)
	}
}

func TestStateSurvivesARestartAndReadersKeepTheirCursors(t *testing.T) {
	edge := &fakeEdge{}
	wire := &fakeWire{}
	d, ws := testDaemon(t, edge, wire)
	ctx := context.Background()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "edge-token"}
	d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	wire.emit(client.Event{Seq: seq(5), Type: "chat.message", Payload: map[string]any{"text": "hello"}})
	up := int64(5)
	d.handler.Handle(ctx, Request{Op: "peek", WorkspaceKey: "ws"})
	d.handler.Handle(ctx, Request{Op: "consume", RoomKey: RoomKey(cfg), ReaderName: TerminalReaderName("ws"), UpTo: &up})

	// A second daemon (a restart) loads the same state path.
	st, err := LoadState(StatePath(hooks.RuntimeDir(ws)))
	if err != nil {
		t.Fatal(err)
	}
	room, ok := st.Rooms[RoomKey(cfg)]
	if !ok || room.Config.Token != "edge-token" {
		t.Fatalf("the room and its token must be persisted: %+v", st.Rooms)
	}
	if room.Readers[TerminalReaderName("ws")].Cursor != 5 {
		t.Fatalf("terminal cursor not persisted: %+v", room.Readers)
	}
	d2 := New(Options{Workspace: ws, Deps: Deps{NewClient: func(client.Config) session.EdgeClient { return edge }, Watch: wire.watch, Heartbeat: time.Hour}})
	d2.restore(ctx, st)
	if edge.joins != 2 {
		t.Fatalf("restore must rejoin with the stored token, joins = %d", edge.joins)
	}
	r2 := d2.room(RoomKey(cfg))
	if rd, _ := r2.Reader(TerminalReaderName("ws")); rd.Cursor != 5 || rd.Connected {
		t.Fatalf("restored reader: %+v", rd)
	}
}

func TestSecondDaemonCannotTakeTheLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lock(filepath.Join(dir, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := lock(filepath.Join(dir, "daemon.lock")); err != ErrAlreadyRunning {
		t.Fatalf("second lock: %v, want ErrAlreadyRunning", err)
	}
}

func TestIdleDaemonLeavesAndExits(t *testing.T) {
	edge := &fakeEdge{}
	wire := &fakeWire{}
	d, _ := testDaemon(t, edge, wire)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	// wait for the socket
	deadline := time.Now().Add(2 * time.Second)
	for !Reachable(d.opts.Workspace) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	res, err := Send(hooks.DaemonSocketPath(d.opts.Workspace), Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "a:ws:1", Kind: "agent", WorkspaceKey: "ws"}}, time.Second)
	if err != nil || !res.OK {
		t.Fatalf("attach over the socket: %v %+v", err, res)
	}
	if _, err := Send(hooks.DaemonSocketPath(d.opts.Workspace), Request{Op: "detach", RoomKey: res.RoomKey, ReaderName: "a:ws:1"}, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon did not exit after its last reader left")
	}
	if edge.leaves != 1 {
		t.Fatalf("the room must be left once on exit, leaves = %d", edge.leaves)
	}
}

func TestMatchHoldsWorkingDirectoryContentUntilThePersonAllows(t *testing.T) {
	edge := &fakeEdge{}
	wire := &fakeWire{}
	d, _ := testDaemon(t, edge, wire)
	ctx := context.Background()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	a := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg,
		Reader:       &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"},
		Fingerprints: &Fingerprints{Paths: []string{"src/cache.js", "go.mod"}, Commits: []string{"82400b8", "82400b8f1e2d3c4b5a69788796a5b4c3d2e1f0a9"}, Names: []string{"checkout-service"}},
	})
	if !a.OK {
		t.Fatal(a.Error)
	}
	m := d.handler.Handle(ctx, Request{Op: "match", RoomKey: a.RoomKey, ReaderName: "claude-code:ws:1", Text: "TTL_MS in src/cache.js is 60s, see commit 82400b8 in checkout-service"})
	if !m.OK || !m.Held || len(m.Matched) != 3 {
		t.Fatalf("match = %+v, want held with three matches (path, commit, name)", m)
	}
	clean := d.handler.Handle(ctx, Request{Op: "match", RoomKey: a.RoomKey, ReaderName: "claude-code:ws:1", Text: "p99 latency spiked at 14:05Z on the payments service"})
	if !clean.OK || clean.Held || len(clean.Matched) != 0 {
		t.Fatalf("a share that names nothing local must pass: %+v", clean)
	}
	short := d.handler.Handle(ctx, Request{Op: "match", RoomKey: a.RoomKey, ReaderName: "claude-code:ws:1", Text: "see go.mod"})
	if short.Held {
		t.Fatalf("a path of eight characters or fewer is too common to hold on: %+v", short)
	}
	if r := d.handler.Handle(ctx, Request{Op: "allow-cwd", RoomKey: a.RoomKey}); !r.OK {
		t.Fatal(r.Error)
	}
	after := d.handler.Handle(ctx, Request{Op: "match", RoomKey: a.RoomKey, ReaderName: "claude-code:ws:1", Text: "TTL_MS in src/cache.js is 60s"})
	if after.Held || !after.Allowed || len(after.Matched) != 1 {
		t.Fatalf("after allow-cwd the same text matches but is not held: %+v", after)
	}
	st, _ := LoadState(d.statePath)
	if !st.Rooms[a.RoomKey].CwdAllowed {
		t.Fatal("the person's allowance must survive a restart")
	}
}

func TestSubscribeStreamsSubstantiveEventsWithoutMovingAnyCursor(t *testing.T) {
	edge := &fakeEdge{}
	wire := &fakeWire{}
	d, _ := testDaemon(t, edge, wire)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = d.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for !Reachable(d.opts.Workspace) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	sock := hooks.DaemonSocketPath(d.opts.Workspace)
	att, err := Send(sock, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "panel", Kind: "panel", WorkspaceKey: "ws"}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	body, _ := json.Marshal(Request{Op: "subscribe", RoomKey: att.RoomKey, ReaderName: "panel"})
	if _, err := conn.Write(append(body, '\n')); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	ackLine, _ := r.ReadBytes('\n')
	var ack Response
	if json.Unmarshal(ackLine, &ack) != nil || !ack.OK {
		t.Fatalf("subscribe ack: %s", ackLine)
	}
	// give the subscription a moment to register before events flow
	time.Sleep(50 * time.Millisecond)
	wire.emit(client.Event{Seq: seq(1), Type: "agent.query"})
	wire.emit(client.Event{Seq: seq(2), Type: "chat.message", Payload: map[string]any{"text": "hello"}})
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("no event streamed: %v", err)
	}
	var got struct {
		Seq   int64        `json:"seq"`
		Event client.Event `json:"event"`
	}
	if json.Unmarshal(line, &got) != nil || got.Seq != 2 || got.Event.Type != "chat.message" {
		t.Fatalf("streamed %s, want the chat message (seq 2) and never the agent.query", line)
	}
	rooms, _ := Send(sock, Request{Op: "rooms"}, time.Second)
	for _, rd := range rooms.Rooms[0].Readers {
		if rd.Name == "panel" && rd.Cursor != -1 {
			t.Fatalf("subscribing must not move the reader's cursor, got %d", rd.Cursor)
		}
	}
}

func TestTheDaemonRingsTheDoorbellForAWorkspaceOnTheFirstUntoldItem(t *testing.T) {
	edge := &fakeEdge{}
	wire := &fakeWire{}
	d, _ := testDaemon(t, edge, wire)
	ctx := context.Background()
	work := t.TempDir()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws", Workspace: work}})
	wire.emit(client.Event{Seq: seq(1), Type: "agent.query"})
	if _, err := os.Stat(hooks.DoorbellPath(work)); !os.IsNotExist(err) {
		t.Fatal("machinery must not ring the doorbell")
	}
	wire.emit(client.Event{Seq: seq(2), Type: "edge.finding", Payload: map[string]any{"text": "p99 spiked"}})
	if _, err := os.Stat(hooks.DoorbellPath(work)); err != nil {
		t.Fatalf("the first untold substantive item must ring the workspace's doorbell: %v", err)
	}
}

func TestARestoredRoomNobodyReadsIsIdleFromTheStart(t *testing.T) {
	edge := &fakeEdge{}
	wire := &fakeWire{}
	d, _ := testDaemon(t, edge, wire)
	past := time.Now().Add(-time.Hour)
	st := &State{V: StateVersion, Rooms: map[string]RoomState{
		"http://x/o/acme/inc-1": {
			Config:  client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"},
			Readers: map[string]*Reader{"claude-code:ws:1": {Name: "claude-code:ws:1", Kind: KindAgent, Cursor: 3, LastSeenAt: past}},
		},
	}}
	d.restore(context.Background(), st)
	if !d.idle() {
		// grace in the test daemon is 200 ms; openedAt is "now", so wait it out
		time.Sleep(250 * time.Millisecond)
	}
	if !d.idle() {
		t.Fatal("a restored room with every reader detached must count as idle, or the daemon lives for ever")
	}
}

func TestRestoreBackfillsTheRingSoUntoldItemsSurviveARestart(t *testing.T) {
	edge := &fakeEdge{}
	edge.updates = func(since int64) []client.Event {
		if since != 20 {
			t.Fatalf("backfill must start at the lowest reader cursor (20), got %d", since)
		}
		return []client.Event{
			{Seq: seq(21), Type: "agent.query"},
			{Seq: seq(22), Type: "chat.message", Payload: map[string]any{"text": "posted one second before the stop"}},
		}
	}
	wire := &fakeWire{}
	d, _ := testDaemon(t, edge, wire)
	st := &State{V: StateVersion, Rooms: map[string]RoomState{
		"http://x/o/acme/inc-1": {
			Config: client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"},
			Readers: map[string]*Reader{
				"claude-code:ws:1":       {Name: "claude-code:ws:1", Kind: KindAgent, Cursor: 25, WorkspaceKey: "ws"},
				TerminalReaderName("ws"): {Name: TerminalReaderName("ws"), Kind: KindTerminal, Cursor: 20, WorkspaceKey: "ws"},
			},
		},
	}}
	d.restore(context.Background(), st)
	p := d.handler.Handle(context.Background(), Request{Op: "peek", WorkspaceKey: "ws"})
	if !p.OK || len(p.Rooms) != 1 || p.Rooms[0].Count != 1 {
		t.Fatalf("after a restart the person is still owed the chat message: %+v", p)
	}
}

func TestANewReaderStartsAtTheFramesPositionNotAtMinusOne(t *testing.T) {
	edge := &fakeEdge{}
	asOf := int64(17)
	edge.frame = &client.ContextFrame{AsOfSeq: &asOf}
	wire := &fakeWire{}
	d, _ := testDaemon(t, edge, wire)
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	a := d.handler.Handle(context.Background(), Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	if !a.OK || a.Reader == nil || a.Reader.Cursor != 17 {
		t.Fatalf("a new reader must start at the frame's as-of seq (17): %+v", a.Reader)
	}
}
