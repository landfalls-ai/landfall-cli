package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// seatEdge is one agent session's client: its own instance id, and a record
// of the label it joined under and the reads it made.
type seatEdge struct {
	*fakeEdge
	cfg    client.Config
	id     string
	mu     sync.Mutex
	joined int
	left   int
	beats  int
	deltas []int64
}

func (s *seatEdge) Join(context.Context) (*client.JoinResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.joined++
	return &client.JoinResult{AgentInstanceID: s.id}, nil
}
func (s *seatEdge) Leave(context.Context) error { s.mu.Lock(); s.left++; s.mu.Unlock(); return nil }
func (s *seatEdge) Heartbeat(context.Context, string) error {
	s.mu.Lock()
	s.beats++
	s.mu.Unlock()
	return nil
}
func (s *seatEdge) GetContextDelta(_ context.Context, since int64) (*client.FrameDelta, error) {
	s.mu.Lock()
	s.deltas = append(s.deltas, since)
	s.mu.Unlock()
	v := int64(30)
	return &client.FrameDelta{SinceVersion: since, ToVersion: &v, Items: []client.DeltaItem{{Seq: 30, Type: "edge.finding", By: s.cfg.AgentLabel}}}, nil
}
func (s *seatEdge) AgentInstanceID() string { return s.id }
func (s *seatEdge) Config() client.Config   { return s.cfg }

type seatFactory struct {
	mu    sync.Mutex
	base  *fakeEdge
	seats []*seatEdge
}

func (f *seatFactory) newClient(cfg client.Config) session.EdgeClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &seatEdge{fakeEdge: f.base, cfg: cfg, id: fmt.Sprintf("inst-%d", len(f.seats)+1)}
	f.seats = append(f.seats, s)
	return s
}

func (f *seatFactory) byLabel(label string) *seatEdge {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.seats {
		if s.cfg.AgentLabel == label {
			return s
		}
	}
	return nil
}

func seatDaemon(t *testing.T) (*Daemon, *seatFactory, *fakeWire) {
	t.Helper()
	f := &seatFactory{base: &fakeEdge{}}
	wire := &fakeWire{}
	d, _ := testDaemon(t, f.base, wire)
	d.opts.Deps.NewClient = f.newClient
	return d, f, wire
}

func attachAs(t *testing.T, d *Daemon, label, harness, pid, link string) Response {
	t.Helper()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t", AgentLabel: label}
	res := d.handler.Handle(context.Background(), Request{Op: "attach", Room: &cfg, Link: link, Reader: &ReaderSpec{
		Name: harness + ":ws:" + pid, Kind: "agent", Host: harness, WorkspaceKey: "ws", Harness: harness,
	}})
	if !res.OK {
		t.Fatalf("attach %s: %s", label, res.Error)
	}
	return res
}

// Claude Code and Codex in one checkout: two agent sessions in the room, each
// under its own label, each with its own person-reader, and each hearing what
// the other wrote.
func TestTwoHarnessesAreTwoAgentSessionsInTheRoom(t *testing.T) {
	d, f, wire := seatDaemon(t)
	ctx := context.Background()
	claude := attachAs(t, d, "Claude Code", "claude-code", "1", "")
	codex := attachAs(t, d, "Codex", "codex", "2", "")

	if claude.RoomKey != codex.RoomKey {
		t.Fatal("one room")
	}
	if claude.AgentInstanceID == codex.AgentInstanceID || claude.AgentInstanceID == "" || codex.AgentInstanceID == "" {
		t.Fatalf("each harness must get its own agent instance: %q %q", claude.AgentInstanceID, codex.AgentInstanceID)
	}
	if s := f.byLabel("Claude Code"); s == nil || s.joined != 1 {
		t.Fatalf("Claude Code's seat must join once under its own label: %+v", s)
	}
	if s := f.byLabel("Codex"); s == nil || s.joined != 1 {
		t.Fatalf("Codex's seat must join once under its own label: %+v", s)
	}
	// A second Claude Code window joins nothing new: same harness, same seat.
	again := attachAs(t, d, "Claude Code", "claude-code", "3", "")
	if again.AgentInstanceID != claude.AgentInstanceID || len(f.seats) != 2 {
		t.Fatalf("a second window of the same harness shares its seat: %q vs %q, %d joins", again.AgentInstanceID, claude.AgentInstanceID, len(f.seats))
	}

	room := d.room(claude.RoomKey)
	for _, name := range []string{TerminalReaderNameFor("ws", "claude-code"), TerminalReaderNameFor("ws", "codex")} {
		if _, ok := room.Reader(name); !ok {
			t.Fatalf("missing the person's reader %s", name)
		}
	}

	// Claude Code shares a finding. It is news to the person's Codex session
	// and to Codex's agent, and Claude Code's own echo to neither of Claude's.
	wire.emit(client.Event{Seq: seq(20), Type: "edge.finding", Payload: map[string]any{"text": "TTL is 60s", "agentInstanceId": claude.AgentInstanceID, "edgeAgentLabel": "Claude Code"}})
	peek := func(harness string) RoomView {
		t.Helper()
		p := d.handler.Handle(ctx, Request{Op: "peek", WorkspaceKey: "ws", Harness: harness})
		if !p.OK || len(p.Rooms) != 1 {
			t.Fatalf("peek %s: %+v", harness, p)
		}
		return p.Rooms[0]
	}
	if v := peek("codex"); v.Count != 1 {
		t.Fatalf("Codex's person-reader must see Claude Code's finding, count = %d", v.Count)
	}
	if v := peek("claude-code"); v.Count != 0 {
		t.Fatalf("Claude Code's own finding is not news to its own session, count = %d", v.Count)
	}
	if u := room.UntoldFor("codex:ws:2"); len(u) != 1 {
		t.Fatalf("Codex's agent reader must not drop Claude Code's write as its own echo: %+v", u)
	}

	// A Codex hook delivers and consumes: Claude Code's reader does not move.
	wire.emit(client.Event{Seq: seq(21), Type: "chat.message", Payload: map[string]any{"text": "rolling back now"}})
	up := int64(21)
	// (The hook also names the harness-less reader, for a daemon of an older
	// build; the harness decides here.)
	if c := d.handler.Handle(ctx, Request{Op: "consume", RoomKey: claude.RoomKey, WorkspaceKey: "ws", Harness: "codex", ReaderName: TerminalReaderName("ws"), UpTo: &up}); !c.OK {
		t.Fatalf("consume: %+v", c)
	}
	if v := peek("codex"); v.Count != 0 {
		t.Fatalf("Codex's reader was consumed, count = %d", v.Count)
	}
	if v := peek("claude-code"); v.Count != 1 {
		t.Fatalf("a Codex hook must not mark the room read for Claude Code, count = %d", v.Count)
	}
	// A hook that cannot tell which host ran it sees the workspace together.
	if v := peek(""); v.Count != 1 {
		t.Fatalf("an unidentified hook reads every person-reader of the workspace, count = %d", v.Count)
	}

	// Each agent's pull goes out under its own seat, from its own cursor.
	if r := d.handler.Handle(ctx, Request{Op: "delta", RoomKey: claude.RoomKey, ReaderName: "codex:ws:2"}); !r.OK || r.Since == nil {
		t.Fatalf("delta: %+v", r)
	}
	if s := f.byLabel("Codex"); len(s.deltas) != 1 {
		t.Fatalf("Codex's delta must be read by Codex's seat: %+v", s.deltas)
	}
	if s := f.byLabel("Claude Code"); len(s.deltas) != 0 {
		t.Fatalf("Claude Code's seat must not read for Codex: %+v", s.deltas)
	}
	if rd, _ := room.Reader("claude-code:ws:1"); rd.Cursor == 30 {
		t.Fatal("Codex's pull moved Claude Code's cursor")
	}

	// rooms lists both seats.
	rooms := d.handler.Handle(ctx, Request{Op: "rooms"})
	if len(rooms.Rooms) != 1 || len(rooms.Rooms[0].Seats) != 2 {
		t.Fatalf("rooms must list both agent sessions: %+v", rooms.Rooms)
	}
}

func TestGetUpdatesReadsTheDaemonCursorAndHonoursAnExplicitSince(t *testing.T) {
	d, _, _ := seatDaemon(t)
	ctx := context.Background()
	a := attachAs(t, d, "Codex", "codex", "1", "")
	from := int64(4)
	r := d.handler.Handle(ctx, Request{Op: "delta", RoomKey: a.RoomKey, ReaderName: "codex:ws:1", Since: &from})
	if !r.OK || r.Since == nil || *r.Since != 4 {
		t.Fatalf("an explicit since must be the one read from: %+v", r)
	}
	if rd, _ := d.room(a.RoomKey).Reader("codex:ws:1"); rd.Cursor != 30 {
		t.Fatalf("the reader's cursor moves to what was delivered, got %d", rd.Cursor)
	}
	r2 := d.handler.Handle(ctx, Request{Op: "delta", RoomKey: a.RoomKey, ReaderName: "codex:ws:1"})
	if r2.Since == nil || *r2.Since != 30 {
		t.Fatalf("the next read starts at the daemon's cursor: %+v", r2.Since)
	}
	up := int64(40)
	if s := d.handler.Handle(ctx, Request{Op: "seen", RoomKey: a.RoomKey, ReaderName: "codex:ws:1", UpTo: &up}); !s.OK || *s.Cursor != 40 {
		t.Fatalf("seen: %+v", s)
	}
	if s := d.handler.Handle(ctx, Request{Op: "seen", RoomKey: a.RoomKey, ReaderName: TerminalReaderNameFor("ws", "codex"), UpTo: &up}); s.OK {
		t.Fatal("seen must refuse to move the person's reader")
	}
}

// A share link is single-use. The second harness handed the same link joins
// through the room the daemon already holds.
func TestASecondHarnessJoinsWithTheSameLinkThroughTheDaemon(t *testing.T) {
	d, f, _ := seatDaemon(t)
	ctx := context.Background()
	link := LinkHash("https://app.landfalls.ai/j/abc123")
	a := attachAs(t, d, "Claude Code", "claude-code", "1", link)
	l := d.handler.Handle(ctx, Request{Op: "link", Link: link})
	if !l.OK || l.Room == nil || l.RoomKey != a.RoomKey || l.Room.Token != "t" || l.Room.AgentLabel != "" {
		t.Fatalf("link lookup: %+v", l)
	}
	if miss := d.handler.Handle(ctx, Request{Op: "link", Link: LinkHash("https://app.landfalls.ai/j/other")}); !miss.OK || miss.Room != nil || !miss.Claimed {
		t.Fatalf("an unknown link is the caller's to redeem: %+v", miss)
	}
	cfg := *l.Room
	cfg.AgentLabel = "Codex"
	b := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "codex:ws:2", Kind: "agent", WorkspaceKey: "ws", Harness: "codex"}})
	if !b.OK || b.AgentInstanceID == a.AgentInstanceID || len(f.seats) != 2 {
		t.Fatalf("the second harness must join its own seat with the room's credential: %+v", b)
	}
	st, _ := LoadState(d.statePath)
	rs := st.Rooms[a.RoomKey]
	if len(rs.Links) != 1 || rs.Links[0] != link || len(rs.Seats) != 1 || rs.Seats[0].AgentLabel != "Codex" {
		t.Fatalf("links and seats must persist: %+v", rs)
	}
	for _, h := range rs.Links {
		if h == "https://app.landfalls.ai/j/abc123" {
			t.Fatal("the link itself carries a credential and must never be written down")
		}
	}
}

func TestAHarnessThatLeftGivesUpItsSeatWhileItsSiblingStays(t *testing.T) {
	d, f, _ := seatDaemon(t)
	ctx := context.Background()
	a := attachAs(t, d, "Claude Code", "claude-code", "1", "")
	attachAs(t, d, "Codex", "codex", "2", "")
	d.handler.Handle(ctx, Request{Op: "detach", RoomKey: a.RoomKey, ReaderName: "codex:ws:2"})
	room := d.room(a.RoomKey)
	if gone := room.ReapSeats(ctx, time.Hour); len(gone) != 0 {
		t.Fatalf("inside the grace nothing is left: %v", gone)
	}
	if gone := room.ReapSeats(ctx, 0); len(gone) != 1 || gone[0] != "Codex" {
		t.Fatalf("Codex's seat must be left once its grace passed: %v", gone)
	}
	if s := f.byLabel("Codex"); s.left != 1 {
		t.Fatalf("Codex's seat must /edge/leave, left = %d", s.left)
	}
	if s := f.byLabel("Claude Code"); s.left != 0 {
		t.Fatal("Claude Code is still reading; its seat stays")
	}
	// Claude Code closes too: the room idles out whole, nothing is reaped.
	d.handler.Handle(ctx, Request{Op: "detach", RoomKey: a.RoomKey, ReaderName: "claude-code:ws:1"})
	if gone := room.ReapSeats(ctx, 0); len(gone) != 0 {
		t.Fatalf("with every seat idle the room's own idle exit decides: %v", gone)
	}
}

func TestThePrimarySeatLeavingPromotesItsSibling(t *testing.T) {
	d, _, _ := seatDaemon(t)
	ctx := context.Background()
	a := attachAs(t, d, "Claude Code", "claude-code", "1", "")
	b := attachAs(t, d, "Codex", "codex", "2", "")
	d.handler.Handle(ctx, Request{Op: "detach", RoomKey: a.RoomKey, ReaderName: "claude-code:ws:1"})
	room := d.room(a.RoomKey)
	room.ReapSeats(ctx, time.Hour) // start the clock
	if gone := room.ReapSeats(ctx, 0); len(gone) != 1 || gone[0] != "Claude Code" {
		t.Fatalf("reaped %v", gone)
	}
	if room.AgentInstanceID != b.AgentInstanceID || room.Snapshot().Config.AgentLabel != "Codex" {
		t.Fatalf("the remaining seat must become the room's primary: %q", room.AgentInstanceID)
	}
}

// A console read goes out under a seat somebody is in: while the primary's
// agent has gone and a sibling is still reading, the server would refuse a
// read under the primary's instance (no live grant).
func TestAConsoleReadUsesALiveSeatNotAnIdlePrimary(t *testing.T) {
	d, _, _ := seatDaemon(t)
	ctx := context.Background()
	a := attachAs(t, d, "Claude Code", "claude-code", "1", "")
	b := attachAs(t, d, "Codex", "codex", "2", "")
	room := d.room(a.RoomKey)
	if _, id := room.ClientAndInstance(); id != a.AgentInstanceID {
		t.Fatalf("with the primary in use the read uses it: %q", id)
	}
	d.handler.Handle(ctx, Request{Op: "detach", RoomKey: a.RoomKey, ReaderName: "claude-code:ws:1"})
	if _, id := room.ClientAndInstance(); id != b.AgentInstanceID {
		t.Fatalf("an idle primary must not carry a read while a sibling is live: %q", id)
	}
	d.handler.Handle(ctx, Request{Op: "detach", RoomKey: a.RoomKey, ReaderName: "codex:ws:2"})
	if _, id := room.ClientAndInstance(); id != a.AgentInstanceID {
		t.Fatalf("with no seat in use the primary answers: %q", id)
	}
}

func TestRestoreRejoinsEverySeat(t *testing.T) {
	d, f, _ := seatDaemon(t)
	st := &State{V: StateVersion, Rooms: map[string]RoomState{
		"http://x/o/acme/inc-1": {
			Config: client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t", AgentLabel: "Claude Code"},
			Seats:  []client.Config{{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t", AgentLabel: "Codex"}},
			Readers: map[string]*Reader{
				"codex:ws:2": {Name: "codex:ws:2", Kind: KindAgent, Seat: "Codex", Harness: "codex", WorkspaceKey: "ws"},
			},
		},
	}}
	d.restore(context.Background(), st)
	if f.byLabel("Claude Code") == nil || f.byLabel("Codex") == nil {
		t.Fatalf("both seats must rejoin: %d joins", len(f.seats))
	}
	if seats := d.room("http://x/o/acme/inc-1").Seats(); len(seats) != 2 || seats[0].Label != "Claude Code" {
		t.Fatalf("seats after restore: %+v", seats)
	}
}

func TestEachSeatBeatsItsOwnPresence(t *testing.T) {
	f := &seatFactory{base: &fakeEdge{}}
	wire := &fakeWire{}
	d, _ := testDaemon(t, f.base, wire)
	d.opts.Deps.NewClient = f.newClient
	d.opts.Deps.Heartbeat = 10 * time.Millisecond
	attachAs(t, d, "Claude Code", "claude-code", "1", "")
	attachAs(t, d, "Codex", "codex", "2", "")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, x := f.byLabel("Claude Code"), f.byLabel("Codex")
		c.mu.Lock()
		cb := c.beats
		c.mu.Unlock()
		x.mu.Lock()
		xb := x.beats
		x.mu.Unlock()
		if cb > 0 && xb > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("both seats must heartbeat")
}

// Two harnesses started together with the same --link: the first to ask
// redeems, the second waits for it and joins with what it got. The session is
// recorded the moment the redeem lands, before any join, so a join that then
// fails does not burn the link.
func TestConcurrentRedeemsOfOneLinkAreSerialized(t *testing.T) {
	d, _, _ := seatDaemon(t)
	ctx := context.Background()
	link := LinkHash("https://app.landfalls.ai/j/together")
	first := d.handler.Handle(ctx, Request{Op: "link", Link: link})
	if !first.Claimed {
		t.Fatalf("the first caller holds the claim: %+v", first)
	}
	second := make(chan Response, 1)
	go func() { second <- d.handler.Handle(ctx, Request{Op: "link", Link: link}) }()
	select {
	case r := <-second:
		t.Fatalf("the second caller must wait for the first redeem, got %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "redeemed", AgentLabel: "Claude Code"}
	if r := d.handler.Handle(ctx, Request{Op: "link-redeemed", Link: link, Room: &cfg}); !r.OK {
		t.Fatal(r.Error)
	}
	select {
	case r := <-second:
		if !r.OK || r.Claimed || r.Room == nil || r.Room.Token != "redeemed" || r.Room.AgentLabel != "" {
			t.Fatalf("the second caller joins with the first redeem's session: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the second caller never woke")
	}
	// No room was ever attached (the first join failed, say): the session is
	// still there for a retry.
	if r := d.handler.Handle(ctx, Request{Op: "link", Link: link}); r.Room == nil || r.Room.Token != "redeemed" {
		t.Fatalf("a recorded redeem outlives a failed join: %+v", r)
	}
}

func TestAFailedRedeemHandsTheClaimToTheNextCaller(t *testing.T) {
	d, _, _ := seatDaemon(t)
	ctx := context.Background()
	link := LinkHash("https://app.landfalls.ai/j/flaky")
	if r := d.handler.Handle(ctx, Request{Op: "link", Link: link}); !r.Claimed {
		t.Fatalf("%+v", r)
	}
	second := make(chan Response, 1)
	go func() { second <- d.handler.Handle(ctx, Request{Op: "link", Link: link}) }()
	time.Sleep(50 * time.Millisecond)
	d.handler.Handle(ctx, Request{Op: "link-failed", Link: link})
	select {
	case r := <-second:
		if !r.Claimed {
			t.Fatalf("after a failed redeem the waiter takes the claim: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never woke")
	}
}

// A hook whose harness key matches no front end here (an inherited
// CLAUDECODE, a renamed host) must not go silent.
func TestAHookWhoseHarnessHasNoReaderFallsBackToTheWorkspace(t *testing.T) {
	d, _, wire := seatDaemon(t)
	ctx := context.Background()
	attachAs(t, d, "Codex", "codex", "1", "")
	wire.emit(client.Event{Seq: seq(9), Type: "edge.finding", Payload: map[string]any{"text": "p99 spiked"}})
	p := d.handler.Handle(ctx, Request{Op: "peek", WorkspaceKey: "ws", Harness: "claude-code"})
	if !p.OK || len(p.Rooms) != 1 || p.Rooms[0].Count != 1 {
		t.Fatalf("an unmatched harness reads the workspace's sessions together: %+v", p)
	}
}

// A reader whose seat was left has no own echo: a sibling's writes are news.
func TestAReaderWhoseSeatIsGoneTreatsNothingAsItsOwnEcho(t *testing.T) {
	d, _, wire := seatDaemon(t)
	ctx := context.Background()
	a := attachAs(t, d, "Claude Code", "claude-code", "1", "")
	attachAs(t, d, "Codex", "codex", "2", "")
	room := d.room(a.RoomKey)
	d.handler.Handle(ctx, Request{Op: "detach", RoomKey: a.RoomKey, ReaderName: "codex:ws:2"})
	room.ReapSeats(ctx, time.Hour)
	if gone := room.ReapSeats(ctx, 0); len(gone) != 1 {
		t.Fatalf("reaped %v", gone)
	}
	wire.emit(client.Event{Seq: seq(9), Type: "edge.finding", Payload: map[string]any{"text": "x", "agentInstanceId": a.AgentInstanceID}})
	if u := room.UntoldFor(TerminalReaderNameFor("ws", "codex")); len(u) != 1 {
		t.Fatalf("Claude Code's write is not Codex's own echo just because Codex's seat is gone: %+v", u)
	}
}

// Protocol 3 answers its own clients in 3 and a request with no `v` (an older
// serve, the Edge panel) in 2.
func TestTheDaemonAnswersEachClientInItsOwnProtocol(t *testing.T) {
	d, _, _ := seatDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for !Reachable(d.opts.Workspace) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	res, err := Send(hooks.DaemonSocketPath(d.opts.Workspace), Request{Op: "rooms"}, time.Second)
	if err != nil || res.V != ProtocolVersion {
		t.Fatalf("a current client is answered in %d: %v %+v", ProtocolVersion, err, res)
	}
	conn, err := net.Dial("unix", hooks.DaemonSocketPath(d.opts.Workspace))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte(`{"op":"rooms"}` + "\n"))
	line, _ := bufio.NewReader(conn).ReadBytes('\n')
	var legacy Response
	if json.Unmarshal(line, &legacy) != nil || legacy.V != LegacyProtocolVersion || !legacy.OK {
		t.Fatalf("an older client is answered in protocol 2: %s", line)
	}
}

// An upgrade: a protocol-2 daemon is still running when a new front end
// starts. It is asked to hand over, and the new daemon takes its place.
func TestANewFrontEndAsksAnOlderDaemonToHandOver(t *testing.T) {
	d, _, _ := seatDaemon(t)
	ws := d.opts.Workspace
	rt := hooks.RuntimeDir(ws)
	if err := os.MkdirAll(rt, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lock(filepath.Join(rt, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", hooks.DaemonSocketPath(ws))
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { // the old daemon: answers everything in protocol 2
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadBytes('\n')
			_, _ = conn.Write([]byte(`{"ok":true,"v":2}` + "\n"))
			_ = conn.Close()
			if strings.Contains(string(line), `"op":"stop"`) {
				_ = ln.Close()
				unlock()
				close(stopped)
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs []string
	spawn := func() error { go func() { _ = d.Run(ctx) }(); return nil }
	if !EnsureRunning(ws, spawn, func(m string) { logs = append(logs, m) }) {
		t.Fatalf("the new daemon must take over; log: %v", logs)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("the older daemon was never asked to stop")
	}
	res, err := Send(hooks.DaemonSocketPath(ws), Request{Op: "rooms"}, time.Second)
	if err != nil || res.V != ProtocolVersion {
		t.Fatalf("the daemon answering now is this version's: %v %+v", err, res)
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "older room daemon (protocol 2)") {
		t.Fatalf("log: %v", logs)
	}
}
