package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// personServer is a Landfall that answers the room's reads, and counts them.
type personServer struct {
	mu     sync.Mutex
	hits   map[string]int
	agentQ int // attention reads that named an agent instance
	humanQ int // attention reads that named none
	lines  string
	claims string
	srv    *httptest.Server
}

const personFrame = `{"asOfSeq":40,"version":1,
 "incident":{"displayId":"Acme 82","title":"cloudfront-5xx-high","severity":"SEV2","status":"investigating"},
 "brief":{"established":[],"workingTheory":[],"disproved":[],"open":[]},
 "participants":[
  {"displayName":"Alice","kind":"member","humanActorId":"u-alice","agentInstanceId":"web:u-alice:t1","active":true},
  {"displayName":"Bob","kind":"member","humanActorId":"u-bob","agentInstanceId":"web:u-bob:t1","active":true},
  {"displayName":"bob","kind":"agent","humanActorId":"u-bob","agentInstanceId":"i-bob","edgeAgentLabel":"bob-codex","active":true}],
 "attachments":[{"seq":3,"kind":"component","ref":{},"label":"web-edge","by":{"humanActorId":"u-alice"},"stale":false}],
 "focus":{"focus":"origin-b in us-east-1c","by":{"humanActorId":"u-alice"}}}`

// The human view: what awaits Alice herself.
const humanAttention = `{"votesAwaited":[{"claimSeq":212,"class":"finding","statement":"The 5xx rise starts at 15:45Z","authoredBy":"bob-codex","authorIsAgent":true,
 "at":"2026-10-08T15:50:00Z","positionsSoFar":1,"stale":false,"expiresInMs":250000,"shortfall":{"text":"needs 1 more","missing":{"corroborators":1}}}],
 "flaggedOwnContext":[],"expiringClaims":[]}`

// The agent's view: the teammate's claim held back as a count.
const agentAttention = `{"votesAwaited":[],"flaggedOwnContext":[],"expiringClaims":[],"waitingForPeople":{"count":1,"text":"1 finding from a teammate is waiting for a person's vote"}}`

const personClaims = `{"claims":[
 {"seq":205,"class":"finding","author":{"humanActorId":"u-alice","displayName":"Alice","kind":"member"},"statement":"Rollback of web-edge v2.3.1 is ready","provenance":[],"state":"admitted","at":"2026-10-08T15:40:00Z","positions":[]},
 {"seq":212,"class":"finding","author":{"humanActorId":"u-bob","agentInstanceId":"i-bob","displayName":"bob-codex","kind":"agent"},"statement":"The 5xx rise starts at 15:45Z",
  "provenance":[{"sourceType":"telemetry","sourceSeq":201,"quote":"CloudWatch 5xxErrorRate"},{"sourceType":"finding","sourceSeq":199,"quote":""}],
  "state":"staged","at":"2026-10-08T15:50:00Z","positions":[{"position":"corroborate","actor":{"humanActorId":"u-carol","kind":"member"}}],
  "outcome":{"admit":false,"requiredBar":{"corroborators":2},"shortfall":{"text":"needs 1 more","missing":{"corroborators":1}}}}],
 "admittedSeqs":[205],"gateEnabled":true}`

func newPersonServer(t *testing.T) *personServer {
	t.Helper()
	ps := &personServer{
		hits:   map[string]int{},
		claims: personClaims,
		lines:  `[{"claimId":"c-1","lineKey":"eu-west-1-5xx","label":"eu-west-1 5xx","holder":{"humanActorId":"u-bob","displayName":"Bob","kind":"member"},"claimedAtSeq":30,"state":"held"},{"claimId":"c-0","lineKey":"dns","label":"dns","holder":{"humanActorId":"u-alice","displayName":"Alice","kind":"member"},"claimedAtSeq":20,"state":"released"}]`,
	}
	ps.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/o/acme/incidents/inc-1")
		ps.mu.Lock()
		ps.hits[r.Method+" "+path]++
		lines, claims := ps.lines, ps.claims
		if path == "/vetting/attention" {
			if r.URL.Query().Get("agentInstanceId") != "" {
				ps.agentQ++
			} else {
				ps.humanQ++
			}
		}
		ps.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		switch path {
		case "/edge/join":
			_, _ = w.Write([]byte(`{"agentInstanceId":"inst-1"}`))
		case "/edge/context/frame":
			_, _ = w.Write([]byte(personFrame))
		case "/vetting/attention":
			if r.URL.Query().Get("agentInstanceId") != "" {
				_, _ = w.Write([]byte(agentAttention))
			} else {
				_, _ = w.Write([]byte(humanAttention))
			}
		case "/claims":
			_, _ = w.Write([]byte(claims))
		case "/edge/lines":
			_, _ = w.Write([]byte(lines))
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(ps.srv.Close)
	return ps
}

func (ps *personServer) count(key string) int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.hits[key]
}

func personDaemon(t *testing.T, wire *fakeWire, now func() time.Time) *Daemon {
	t.Helper()
	rt, err := os.MkdirTemp("/tmp", "lfd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	ws := hooks.Workspace{Cwd: t.TempDir(), Env: map[string]string{"XDG_RUNTIME_DIR": rt}}
	return New(Options{
		Workspace: ws,
		IdleGrace: time.Hour,
		Deps: Deps{
			NewClient: func(cfg client.Config) session.EdgeClient { return client.New(cfg, nil) },
			Watch:     wire.watch,
			Heartbeat: time.Hour,
			Now:       now,
		},
	})
}

// peekUntil peeks until cond holds of the first room, or fails.
func peekUntil(t *testing.T, d *Daemon, what string, cond func(RoomView) bool) RoomView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		p := d.handler.Handle(context.Background(), Request{Op: "peek", WorkspaceKey: "ws"})
		if p.OK && len(p.Rooms) == 1 && cond(p.Rooms[0]) {
			return p.Rooms[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: last peek %+v", what, p)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// TestPeekCarriesThePersonsView: the peek a watch reads carries the votes the
// PERSON is asked for (the human view, not the agent's count), each person's
// latest contribution with its state, the held lines, the focus and scope, and
// Beacon's live step, all read through the room's own session.
func TestPeekCarriesThePersonsView(t *testing.T) {
	ps := newPersonServer(t)
	wire := &fakeWire{}
	clk := &clock{t: time.Date(2026, 10, 8, 15, 53, 0, 0, time.UTC)}
	d := personDaemon(t, wire, clk.now)
	cfg := client.Config{BaseURL: ps.srv.URL, Slug: "acme", IncidentID: "inc-1", Token: "room-tok", HumanActorID: "u-alice"}
	if a := d.handler.Handle(context.Background(), Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}}); !a.OK {
		t.Fatalf("attach: %+v", a)
	}
	wire.emit(client.Event{Seq: seq(30), Type: "line.claimed", Payload: map[string]any{"claimId": "c-1", "at": "2026-10-08T15:52:00Z"}})
	wire.emit(client.Event{Seq: seq(41), Type: "agent.run.started"})
	wire.emit(client.Event{Seq: seq(42), Type: "agent.step", Payload: map[string]any{"text": "listing target groups", "runSeq": float64(41)}})
	wire.emit(client.Event{Seq: seq(43), Type: "agent.step", Payload: map[string]any{"text": "comparing 5xx per target group", "runSeq": float64(41)}})

	room := peekUntil(t, d, "the person's view", func(r RoomView) bool {
		return len(r.Votes) == 1 && r.Status != nil && len(r.Status.Lines) == 1 && r.Status.People != nil && r.Status.People[0].Latest != nil
	})
	if room.VotesAwaited != 0 {
		t.Fatalf("votesAwaited is the agent's count and stays 0 here, got %d", room.VotesAwaited)
	}
	v := room.Votes[0]
	if v.ClaimSeq != 212 || v.Statement != "The 5xx rise starts at 15:45Z" || !v.AuthorIsAgent || v.AuthorHuman != "Bob" || v.Mine {
		t.Fatalf("vote = %+v", v)
	}
	if v.Needed == nil || *v.Needed != 2 || v.Shortfall == nil || *v.Shortfall != 1 || v.PositionsSoFar != 1 {
		t.Fatalf("vote bar = %+v", v)
	}
	if v.Evidence != "CloudWatch 5xxErrorRate · finding #199" {
		t.Fatalf("evidence = %q", v.Evidence)
	}
	st := room.Status
	alice := st.People[0]
	if alice.Name != "Alice" || alice.Latest == nil || alice.Latest.Seq != 205 || alice.Latest.State != "admitted" || alice.Latest.AgeMs == nil || *alice.Latest.AgeMs != 13*60*1000 {
		t.Fatalf("alice = %+v latest %+v", alice, alice.Latest)
	}
	bob := st.People[1]
	if bob.Latest == nil || bob.Latest.Seq != 212 || bob.Latest.State != "corroborated" {
		t.Fatalf("bob latest = %+v", bob.Latest)
	}
	l := st.Lines[0]
	if l.ClaimID != "c-1" || l.Owner != "Bob" || l.You || l.Label != "eu-west-1 5xx" || l.AgeMs == nil || *l.AgeMs != 60000 {
		t.Fatalf("line = %+v", l)
	}
	if st.Focus != "origin-b in us-east-1c" || len(st.Scope) != 1 || st.Scope[0] != "component:web-edge" {
		t.Fatalf("focus %q scope %v", st.Focus, st.Scope)
	}
	if st.BeaconStep == nil || st.BeaconStep.Text != "comparing 5xx per target group" || st.BeaconStep.Step != 2 || st.BeaconStep.RunSeq != 41 {
		t.Fatalf("beacon step = %+v", st.BeaconStep)
	}
	ps.mu.Lock()
	humanQ := ps.humanQ
	ps.mu.Unlock()
	if humanQ < 1 {
		t.Fatal("the person's attention was never read without an agent instance")
	}
}

// TestPersonReadsAreCachedBetweenEvents: peeking again and again reads
// nothing new until an event changes the view or PersonTTL passes, so a watch
// ticking every two seconds costs the room nothing.
func TestPersonReadsAreCachedBetweenEvents(t *testing.T) {
	ps := newPersonServer(t)
	wire := &fakeWire{}
	clk := &clock{t: time.Date(2026, 10, 8, 15, 53, 0, 0, time.UTC)}
	d := personDaemon(t, wire, clk.now)
	cfg := client.Config{BaseURL: ps.srv.URL, Slug: "acme", IncidentID: "inc-1", Token: "room-tok", HumanActorID: "u-alice"}
	if a := d.handler.Handle(context.Background(), Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}}); !a.OK {
		t.Fatalf("attach: %+v", a)
	}
	peekUntil(t, d, "first read", func(r RoomView) bool { return len(r.Votes) == 1 })
	room := d.rooms()[0]
	waitIdle := func() {
		deadline := time.Now().Add(2 * time.Second)
		for room.personLoading() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitIdle()
	claims, lines := ps.count("GET /claims"), ps.count("GET /edge/lines")
	for i := 0; i < 20; i++ {
		d.handler.Handle(context.Background(), Request{Op: "peek", WorkspaceKey: "ws"})
	}
	waitIdle()
	if ps.count("GET /claims") != claims || ps.count("GET /edge/lines") != lines {
		t.Fatalf("twenty quiet peeks read again: claims %d→%d, lines %d→%d", claims, ps.count("GET /claims"), lines, ps.count("GET /edge/lines"))
	}

	// A line event reads the lines again, and only the lines.
	ps.mu.Lock()
	ps.lines = `[]`
	ps.mu.Unlock()
	wire.emit(client.Event{Seq: seq(60), Type: "line.released", Payload: map[string]any{"claimId": "c-1"}})
	peekUntil(t, d, "lines after line.released", func(r RoomView) bool { return r.Status != nil && len(r.Status.Lines) == 0 })
	waitIdle()
	if ps.count("GET /claims") != claims {
		t.Fatalf("a line event read the claims again")
	}

	// A claim event reads the person's attention and the claims again.
	ps.mu.Lock()
	ps.claims = strings.Replace(personClaims, `"state":"staged"`, `"state":"admitted"`, 1)
	ps.mu.Unlock()
	wire.emit(client.Event{Seq: seq(61), Type: "claim.admitted", Payload: map[string]any{"claimSeq": float64(212)}})
	peekUntil(t, d, "claims after claim.admitted", func(r RoomView) bool {
		return r.Status != nil && len(r.Status.People) > 1 && r.Status.People[1].Latest != nil && r.Status.People[1].Latest.State == "admitted"
	})

	// With no event at all, the reads come again after PersonTTL.
	waitIdle()
	lines = ps.count("GET /edge/lines")
	clk.add(PersonTTL + time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for ps.count("GET /edge/lines") == lines {
		d.handler.Handle(context.Background(), Request{Op: "peek", WorkspaceKey: "ws"})
		if time.Now().After(deadline) {
			t.Fatal("the lines were not read again after PersonTTL")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestBeaconsConclusionIsKeptAfterTheRingMovesOn: the answer a concluded run
// gave is captured when agent.run.concluded arrives and still reported once
// the events that said it have fallen out of the ring.
func TestBeaconsConclusionIsKeptAfterTheRingMovesOn(t *testing.T) {
	ps := newPersonServer(t)
	wire := &fakeWire{}
	d := personDaemon(t, wire, nil)
	cfg := client.Config{BaseURL: ps.srv.URL, Slug: "acme", IncidentID: "inc-1", Token: "room-tok", HumanActorID: "u-alice"}
	if a := d.handler.Handle(context.Background(), Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}}); !a.OK {
		t.Fatalf("attach: %+v", a)
	}
	wire.emit(client.Event{Seq: seq(100), Type: "agent.run.started"})
	wire.emit(client.Event{Seq: seq(101), Type: "agent.hypothesis.raised", Payload: map[string]any{"statement": "origin-b in us-east-1c returns 5xx since 15:45Z"}})
	wire.emit(client.Event{Seq: seq(102), Type: "agent.run.concluded", Payload: map[string]any{"ok": true}})
	for i := int64(0); i < RingMax+5; i++ {
		wire.emit(client.Event{Seq: seq(200 + i), Type: "chat.message", Payload: map[string]any{"text": "noise"}})
	}
	room := peekUntil(t, d, "the kept conclusion", func(r RoomView) bool { return r.Status != nil && r.Status.BeaconConclusion != nil })
	c := room.Status.BeaconConclusion
	if c.Text != "origin-b in us-east-1c returns 5xx since 15:45Z" || c.Seq != 102 {
		t.Fatalf("conclusion = %+v", c)
	}
	if room.Status.BeaconStep != nil {
		t.Fatalf("a concluded run has no live step, got %+v", room.Status.BeaconStep)
	}
}

// TestSessionHandsOverTheRoomsConfig: `landfall vote` asks the daemon for the
// room's session; the answer is the config the room was opened with.
func TestSessionHandsOverTheRoomsConfig(t *testing.T) {
	ps := newPersonServer(t)
	d := personDaemon(t, &fakeWire{}, nil)
	cfg := client.Config{BaseURL: ps.srv.URL, Slug: "acme", IncidentID: "inc-1", Token: "room-tok", HumanActorID: "u-alice", AgentLabel: "alice-claude-code"}
	a := d.handler.Handle(context.Background(), Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	if !a.OK {
		t.Fatalf("attach: %+v", a)
	}
	res := d.handler.Handle(context.Background(), Request{Op: "session", RoomKey: a.RoomKey})
	if !res.OK || res.Room == nil || res.Room.Token != "room-tok" || res.Room.HumanActorID != "u-alice" || res.Room.AgentLabel != "" {
		t.Fatalf("session = %+v room %+v", res, res.Room)
	}
	if bad := d.handler.Handle(context.Background(), Request{Op: "session", RoomKey: "nope"}); bad.OK {
		t.Fatalf("an unknown room must fail: %+v", bad)
	}
	body, _ := json.Marshal(res)
	if !strings.Contains(string(body), `"room"`) {
		t.Fatalf("wire = %s", body)
	}
}
