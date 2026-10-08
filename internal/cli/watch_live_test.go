package cli

// watch_live_test.go — the live half of `landfall watch` (specs/
// 20261008-150000-edge-components/live.md): a real daemon on a real socket,
// the real subscribe stream and the real peek, a stand-in for the realtime
// socket only. Asserts the bound from an event arriving at the daemon to the
// line a mod redraws from.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// watchLineBound is the most a pushed event may take to become a line. The
// mod's own target is 2 s from append to redraw (FR-L1); this is the watch's
// share of it, with no network in the path.
const watchLineBound = 250 * time.Millisecond

// liveWire stands in for the realtime socket: whatever the test pushes enters
// the room as a pushed event does.
type liveWire struct {
	mu   sync.Mutex
	push func(client.Event)
}

func (w *liveWire) watch(_ context.Context, _ client.Config, _ func() string, onEvent func(client.Event)) func() {
	w.mu.Lock()
	w.push = onEvent
	w.mu.Unlock()
	return func() {}
}

func (w *liveWire) emit(e client.Event) {
	w.mu.Lock()
	p := w.push
	w.mu.Unlock()
	if p != nil {
		p(e)
	}
}

func startLiveDaemon(t *testing.T, ws hooks.Workspace) *liveWire {
	t.Helper()
	wire := &liveWire{}
	d := daemon.New(daemon.Options{
		Workspace: ws,
		IdleGrace: time.Hour,
		Deps: daemon.Deps{
			NewClient: func(cfg client.Config) session.EdgeClient {
				return &labelledEdge{fakeEdge: &fakeEdge{cfg: cfg}, id: "inst-" + strings.ReplaceAll(strings.ToLower(cfg.AgentLabel), " ", "-")}
			},
			Watch:     wire.watch,
			Heartbeat: time.Hour,
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for !daemon.Reachable(ws) {
		if time.Now().After(deadline) {
			t.Fatal("the test daemon never answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return wire
}

// lineLog is the watch's stdout, with the moment each line arrived.
type lineLog struct {
	mu    sync.Mutex
	lines []string
	at    []time.Time
}

func (l *lineLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		l.lines = append(l.lines, line)
		l.at = append(l.at, time.Now())
	}
	return len(p), nil
}

// waitLine is the first line after index `from` that contains want, and when
// it arrived.
func (l *lineLog) waitLine(t *testing.T, from int, want string, within time.Duration) (string, time.Time, int) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		l.mu.Lock()
		for i := from; i < len(l.lines); i++ {
			if strings.Contains(l.lines[i], want) {
				line, at := l.lines[i], l.at[i]
				l.mu.Unlock()
				return line, at, i + 1
			}
		}
		n := len(l.lines)
		l.mu.Unlock()
		if time.Now().After(deadline) {
			l.mu.Lock()
			last := ""
			if n > 0 {
				last = l.lines[n-1]
			}
			l.mu.Unlock()
			t.Fatalf("no line with %s within %s; last line: %s", want, within, last)
		}
		time.Sleep(time.Millisecond)
	}
}

func (l *lineLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

// liveWorkspace is a checkout with a short runtime dir (socket path limits).
func liveWorkspace(t *testing.T, harness string) hooks.Workspace {
	dir, runDir := shortTempDir(t), shortTempDir(t)
	return hooks.Workspace{Cwd: dir, Env: map[string]string{"XDG_RUNTIME_DIR": runDir}, Harness: harness}
}

func attachReader(t *testing.T, ws hooks.Workspace, kind daemon.Kind, pid string) *daemon.Response {
	t.Helper()
	key := hooks.WorkspaceKey(ws.Dir())
	name := daemon.TerminalReaderNameFor(key, ws.Harness)
	if kind == daemon.KindAgent {
		name = "claude-code:" + key + ":" + pid
	}
	cfg := client.Config{BaseURL: "http://127.0.0.1:1", Slug: "acme", IncidentID: "inc-1", Token: "room-token", AgentLabel: "Claude Code"}
	res, err := daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{Op: "attach", Room: &cfg, Reader: &daemon.ReaderSpec{
		Name: name, Kind: string(kind), Host: "claude-code", WorkspaceKey: key, Workspace: ws.Dir(), Harness: ws.Harness,
	}}, daemon.AttachTimeout)
	if err != nil {
		t.Fatalf("attach %s: %v", kind, err)
	}
	return res
}

func startWatch(t *testing.T, ws hooks.Workspace) *lineLog {
	t.Helper()
	out := &lineLog{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = Watch(ctx, WatchDeps{
			Snapshot:  func() WatchSnapshot { return watchSnapshot(ws) },
			Subscribe: watchSubscribe(hooks.DaemonSocketPath(ws)),
			Out:       out,
			// The slow wake is out of the picture: only the push can explain
			// a line inside the bound.
			Tick: time.Hour,
		})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return out
}

// A widget landing on the wall is a line within the bound, with its title from
// the request that named it; widget rows are plumbing to the hooks, so this
// is the path that used to wait for the next tick.
func TestWatchWritesANewWidgetWithinTheBound(t *testing.T) {
	ws := liveWorkspace(t, "claude-code")
	wire := startLiveDaemon(t, ws)
	attachReader(t, ws, daemon.KindAgent, "1")
	out := startWatch(t, ws)
	first, _, next := out.waitLine(t, 0, `"rooms":[{`, 3*time.Second)
	if !strings.Contains(first, `"widgetSeq":0`) || strings.Contains(first, `"newestWidget"`) {
		t.Fatalf("no widget yet: %s", first)
	}
	if !strings.Contains(first, `"agent":{"inRoom":true,"label":"claude-code","sinceMs":`) {
		t.Fatalf("the agent is in the room: %s", first)
	}
	// Let the subscribe stream come up before the clock starts.
	time.Sleep(100 * time.Millisecond)

	var took []time.Duration
	seq := int64(100)
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("w%d", i)
		title := fmt.Sprintf("5xx by target group %d", i)
		s1, s2 := seq+1, seq+2
		seq = s2
		wire.emit(client.Event{Seq: &s1, Type: "agent.widget.requested", ActorType: "agent", Payload: map[string]any{"widgetId": id, "widgetType": "chart", "title": title}})
		began := time.Now()
		wire.emit(client.Event{Seq: &s2, Type: "agent.widget.executed", ActorType: "agent", Payload: map[string]any{"widgetId": id, "type": "chart", "status": "rendered"}})
		want := fmt.Sprintf(`"widgetSeq":%d,"newestWidget":{"seq":%d,"title":%q,"type":"chart","by":"Beacon"}`, s2, s2, title)
		_, at, n := out.waitLine(t, next, want, 2*time.Second)
		next = n
		took = append(took, at.Sub(began))
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	t.Logf("widget event to watch line over %d runs: median %s, max %s", len(took), took[len(took)/2], took[len(took)-1])
	if max := took[len(took)-1]; max > watchLineBound {
		t.Fatalf("a widget took %s to become a line; the bound is %s", max, watchLineBound)
	}
}

// The person joined from the mod and their agent arrives: the band's "agent ✓"
// is a line within the bound, not at the next tick.
func TestWatchWritesTheAgentArrivingWithinTheBound(t *testing.T) {
	ws := liveWorkspace(t, "claude-code")
	startLiveDaemon(t, ws)
	attachReader(t, ws, daemon.KindTerminal, "")
	out := startWatch(t, ws)
	_, _, next := out.waitLine(t, 0, `"agent":{"inRoom":false}`, 3*time.Second)
	time.Sleep(100 * time.Millisecond)
	began := time.Now()
	attachReader(t, ws, daemon.KindAgent, "9")
	_, at, _ := out.waitLine(t, next, `"agent":{"inRoom":true,"label":"claude-code"`, 2*time.Second)
	t.Logf("agent attach to watch line: %s", at.Sub(began))
	if took := at.Sub(began); took > watchLineBound+100*time.Millisecond {
		// The attach itself (a frame read) is inside this measurement.
		t.Fatalf("the agent arriving took %s to become a line", took)
	}
}

// The exact fields a room gains on the stream.
func TestWatchRoomCarriesTheLiveFields(t *testing.T) {
	ago, since := int64(1200), int64(60000)
	r := WatchRoom{
		RoomKey: "rk", IncidentID: "inc-1", Connection: "live", Votes: []narrate.Vote{},
		WidgetSeq:      231,
		NewestWidget:   &daemon.WidgetView{Seq: 231, Title: "5xx by target group", Type: "chart", By: "bob"},
		LastEventAgoMs: &ago,
		Agent:          &daemon.AgentView{InRoom: true, Label: "claude-code", SinceMs: &since},
	}
	body, _ := json.Marshal(r)
	want := `"widgetSeq":231,"newestWidget":{"seq":231,"title":"5xx by target group","type":"chart","by":"bob"},"lastEventAgoMs":1200,"agent":{"inRoom":true,"label":"claude-code","sinceMs":60000}`
	if !strings.Contains(string(body), want) {
		t.Fatalf("room = %s\nwant it to carry %s", body, want)
	}
	// Unknown: widgetSeq stays (0), the rest are left out.
	body, _ = json.Marshal(WatchRoom{RoomKey: "rk", Votes: []narrate.Vote{}})
	if !strings.Contains(string(body), `"widgetSeq":0`) || strings.Contains(string(body), "newestWidget") || strings.Contains(string(body), "lastEventAgoMs") || strings.Contains(string(body), `"agent"`) {
		t.Fatalf("an empty room = %s", body)
	}
}

// An age growing is not news: the line is written again only when something
// happened, or when the room was heard from again after a quiet spell.
func TestWatchDoesNotWriteALineBecauseAnAgeGrew(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)
	heardAt := now
	agentAt := now
	reads := 0
	snapshot := func() WatchSnapshot {
		mu.Lock()
		defer mu.Unlock()
		reads++
		ago := now.Sub(heardAt).Milliseconds()
		since := now.Sub(agentAt).Milliseconds()
		return WatchSnapshot{Type: "rooms", Rooms: []WatchRoom{{RoomKey: "rk", LastEventAgoMs: &ago, Agent: &daemon.AgentView{InRoom: true, Label: "claude-code", SinceMs: &since}}}}
	}
	out := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Watch(ctx, WatchDeps{Snapshot: snapshot, Out: out, Tick: 2 * time.Millisecond, Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			now = now.Add(time.Second) // a second passes every snapshot
			return now
		}})
	}()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return reads >= 20 }, "the watch loop")
	if got := len(outLines(out)); got != 1 {
		t.Fatalf("growing ages wrote %d lines, want 1:\n%s", got, out.String())
	}
	// The room is heard from again, long after the last line: worth a line,
	// so a reader's own ageing of the old value is corrected.
	mu.Lock()
	heardAt = now
	mu.Unlock()
	waitFor(t, func() bool { return len(outLines(out)) == 2 }, "a line for the room being heard again")
}
