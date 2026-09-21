package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// fakeDaemon listens where DaemonSocketPath points and answers `peek` with a
// fixed set of rooms; it records every `consume` it is sent.
type fakeDaemon struct {
	rooms    []DaemonRoom
	mu       sync.Mutex
	consumes []SocketRequest
}

func startFakeDaemon(t *testing.T, ws Workspace, rooms []DaemonRoom) *fakeDaemon {
	t.Helper()
	path := DaemonSocketPath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	fd := &fakeDaemon{rooms: rooms}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				var req SocketRequest
				_ = json.Unmarshal([]byte(strings.TrimSpace(line)), &req)
				var out any
				switch req.verb() {
				case "peek":
					out = map[string]any{"ok": true, "v": 2, "rooms": fd.rooms}
				case "consume":
					fd.mu.Lock()
					fd.consumes = append(fd.consumes, req)
					fd.mu.Unlock()
					out = map[string]any{"ok": true, "v": 2, "cursor": req.UpTo}
				default:
					out = map[string]any{"ok": false, "v": 2, "error": "unknown op"}
				}
				body, _ := json.Marshal(out)
				_, _ = conn.Write(append(body, '\n'))
			}(conn)
		}
	}()
	return fd
}

func TestAHookReadsTheDaemonsTerminalReaderDirectly(t *testing.T) {
	ws := tempWorkspace(t)
	fd := startFakeDaemon(t, ws, []DaemonRoom{{
		RoomKey: "http://x/o/acme/inc-1", IncidentID: "inc-1", Slug: "acme", Connection: "live",
		Count: 2, Cursor: 19, MaxSeq: 41, Digest: []string{"bob: rolling back", "carol: cache TTL finding"},
		Attention: &client.Attention{VotesAwaited: []client.VoteAwaited{{Statement: "TTL was 60 s"}}},
	}})

	answers := QueryWorkspace(PeekRequest(), ws, 0)
	if len(answers) != 1 {
		t.Fatalf("want one answer from the daemon, got %d: %+v", len(answers), answers)
	}
	a := answers[0]
	if !IsDaemonAnswer(a.SocketPath) || DaemonRoomKeyOf(a.SocketPath) != "http://x/o/acme/inc-1" {
		t.Fatalf("answer path %q", a.SocketPath)
	}
	if a.Response.IncidentID != "inc-1" || a.Response.CountOr(0) != 2 || a.Response.CursorOr(-1) != 19 || a.Response.MaxSeqOr(-1) != 41 || !a.Response.Connected {
		t.Fatalf("answer %+v", a.Response)
	}
	if !OwesUpdates(a) {
		t.Fatal("the daemon's untold count must read as owed")
	}
	if a.Response.Attention == nil || len(a.Response.Attention.VotesAwaited) != 1 {
		t.Fatalf("the daemon's attention must reach the hook: %+v", a.Response.Attention)
	}

	// A consume for that answer reaches the daemon, for the terminal reader of
	// this workspace and the answer's room — never a per-pid socket.
	if err := SendToAnswer(ws, a.SocketPath, ConsumeRequest(41), 0); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.consumes) != 1 {
		t.Fatalf("consumes %+v", fd.consumes)
	}
	c := fd.consumes[0]
	if c.RoomKey != "http://x/o/acme/inc-1" || c.ReaderName != TerminalReaderName(WorkspaceKey(ws.Dir())) {
		t.Fatalf("consume %+v", c)
	}
	if up, _ := c.UpTo.(float64); up != 41 {
		t.Fatalf("upTo %v", c.UpTo)
	}
}

func TestAPerPidProxyForTheSameIncidentAddsOnlyItsAttention(t *testing.T) {
	ws := tempWorkspace(t)
	startFakeDaemon(t, ws, []DaemonRoom{{
		RoomKey: "http://x/o/acme/inc-1", IncidentID: "inc-1", Connection: "live",
		Count: 1, Cursor: 19, MaxSeq: 20, Digest: []string{"bob: rolling back"},
	}})
	// A front end in daemon mode: its per-pid socket proxies the same terminal
	// reader (the same event), and carries the room's attention.
	attn := &client.Attention{VotesAwaited: []client.VoteAwaited{{}}}
	s := &fakeSession{
		pending:   []client.Event{{Seq: seq(20), Type: "chat.message", Payload: map[string]any{"text": "rolling back"}}},
		cursor:    19,
		attention: attn,
		cfg:       &client.Config{IncidentID: "inc-1"},
	}
	bound := StartHookSocket(context.Background(), s, StartOptions{Workspace: ws, PID: 4242})
	if bound == nil {
		t.Fatal("bind")
	}
	defer bound.Close()

	answers := QueryWorkspace(PeekRequest(), ws, 0)
	if len(answers) != 1 {
		t.Fatalf("one merged answer, got %d: %+v", len(answers), answers)
	}
	if !IsDaemonAnswer(answers[0].SocketPath) {
		t.Fatalf("the daemon's answer must be the one kept, got %q", answers[0].SocketPath)
	}
	if answers[0].Response.CountOr(0) != 1 {
		t.Fatalf("the proxy's copy of the same event must not be counted twice: %+v", answers[0].Response)
	}
	if answers[0].Response.Attention == nil || len(answers[0].Response.Attention.VotesAwaited) != 1 {
		t.Fatalf("attention from the front end's session must be kept: %+v", answers[0].Response.Attention)
	}
}

func TestAFallbackServeForAnotherIncidentStandsOnItsOwn(t *testing.T) {
	ws := tempWorkspace(t)
	startFakeDaemon(t, ws, []DaemonRoom{{RoomKey: "k1", IncidentID: "inc-1", Connection: "live", Count: 1, Cursor: 0, MaxSeq: 1, Digest: []string{"a"}}})
	s := &fakeSession{
		pending: []client.Event{{Seq: seq(7), Type: "chat.message", Payload: map[string]any{"text": "other room"}}},
		cursor:  6,
		cfg:     &client.Config{IncidentID: "inc-2"},
	}
	bound := StartHookSocket(context.Background(), s, StartOptions{Workspace: ws, PID: 4343})
	if bound == nil {
		t.Fatal("bind")
	}
	defer bound.Close()

	answers := QueryWorkspace(PeekRequest(), ws, 0)
	if len(answers) != 2 {
		t.Fatalf("daemon room plus the fallback serve's room, got %d: %+v", len(answers), answers)
	}
	var perPid *SocketAnswer
	for i := range answers {
		if !IsDaemonAnswer(answers[i].SocketPath) {
			perPid = &answers[i]
		}
	}
	if perPid == nil || perPid.Response.IncidentID != "inc-2" {
		t.Fatalf("the fallback serve's answer must survive: %+v", answers)
	}
	// And its consume goes to its own socket, untouched by the daemon path.
	if err := SendToAnswer(ws, perPid.SocketPath, ConsumeRequest(7), 0); err != nil {
		t.Fatal(err)
	}
}

func TestNoDaemonMeansThePerPidSocketsAlone(t *testing.T) {
	ws := tempWorkspace(t)
	s := &fakeSession{pending: []client.Event{{Seq: seq(1), Type: "chat.message"}}, cfg: &client.Config{IncidentID: "inc-9"}}
	bound := StartHookSocket(context.Background(), s, StartOptions{Workspace: ws, PID: 4444})
	if bound == nil {
		t.Fatal("bind")
	}
	defer bound.Close()
	start := time.Now()
	answers := QueryWorkspace(PeekRequest(), ws, 0)
	if len(answers) != 1 || IsDaemonAnswer(answers[0].SocketPath) {
		t.Fatalf("answers %+v", answers)
	}
	if time.Since(start) > 2*SocketTimeout+500*time.Millisecond {
		t.Fatalf("an absent daemon must not slow a hook down: %s", time.Since(start))
	}
}
