package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func yes() *bool { b := true; return &b }

func roomFrame(status string) *client.ContextFrame {
	return &client.ContextFrame{
		Incident: client.Incident{DisplayID: "Acme 82", Title: "cloudfront-5xx-high", Severity: "SEV2", Status: status},
		Participants: []client.Participant{
			{DisplayName: "carol", Kind: "member", HumanActorID: "u-carol", AgentInstanceID: "web:u-carol:t1", Active: yes()},
			{DisplayName: "alice", Kind: "member", HumanActorID: "u-alice", AgentInstanceID: "inst-1", EdgeAgentLabel: "alice-claude-code", Active: yes(), Doing: "reading CloudFront 5xx by region"},
		},
		Brief: client.Brief{WorkingTheory: []client.BriefItem{{Seq: 40, Statement: "the 14:32 web-edge deploy dropped healthy hosts"}}},
	}
}

// TestPeekCarriesTheRoomAtAGlance: the peek a status line or a mod reads says
// the incident's status, who is in the room (you marked), and the leading
// theory, from the frame the attach already read.
func TestPeekCarriesTheRoomAtAGlance(t *testing.T) {
	edge := &fakeEdge{frame: roomFrame("investigating")}
	d, _ := testDaemon(t, edge, &fakeWire{})
	ctx := context.Background()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t", HumanActorID: "u-alice"}
	if a := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}}); !a.OK {
		t.Fatalf("attach: %+v", a)
	}
	p := d.handler.Handle(ctx, Request{Op: "peek", WorkspaceKey: "ws"})
	if !p.OK || len(p.Rooms) != 1 || p.Rooms[0].Status == nil {
		t.Fatalf("peek: %+v", p)
	}
	st := p.Rooms[0].Status
	if st.Status != "investigating" || st.Severity != "SEV2" {
		t.Fatalf("status = %+v", st)
	}
	if len(st.People) != 2 || !st.People[0].You || st.People[0].Name != "alice" || st.People[0].Agents[0].Tool != "Claude Code" {
		t.Fatalf("people = %+v", st.People)
	}
	if !st.People[1].Browser || st.People[1].Name != "carol" {
		t.Fatalf("carol = %+v", st.People[1])
	}
	if st.Theory != "the 14:32 web-edge deploy dropped healthy hosts" {
		t.Fatalf("theory = %q", st.Theory)
	}
}

// TestAStatusChangeReachesThePeekWithoutWaitingOutTheCache: a resolved
// incident must not read "investigating" for FrameTTL. The event marks the
// frame stale; the next peek answers at once from cache and refreshes behind
// it, and a peek shortly after reads the new status and Beacon's run.
func TestAStatusChangeReachesThePeekWithoutWaitingOutTheCache(t *testing.T) {
	edge := &fakeEdge{frame: roomFrame("investigating")}
	wire := &fakeWire{}
	d, _ := testDaemon(t, edge, wire)
	ctx := context.Background()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t", HumanActorID: "u-alice"}
	if a := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}}); !a.OK {
		t.Fatalf("attach: %+v", a)
	}
	edge.setFrame(roomFrame("resolved"))
	wire.emit(client.Event{Seq: seq(50), Type: "agent.run.started"})
	wire.emit(client.Event{Seq: seq(51), Type: "status.changed", Payload: map[string]any{"status": "resolved"}})

	deadline := time.Now().Add(2 * time.Second)
	for {
		p := d.handler.Handle(ctx, Request{Op: "peek", WorkspaceKey: "ws"})
		st := p.Rooms[0].Status
		if st.Status == "resolved" {
			if st.Beacon != "investigating" {
				t.Fatalf("beacon = %q", st.Beacon)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status still %q after the resolve event", st.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAnExpiredFrameIsReadAgainWithoutAnEvent: a change the room pushes no
// event for (a presence beat) still reaches the peek once the cached frame is
// older than FrameTTL.
func TestAnExpiredFrameIsReadAgainWithoutAnEvent(t *testing.T) {
	edge := &fakeEdge{frame: roomFrame("investigating")}
	d, _ := testDaemon(t, edge, &fakeWire{})
	ctx := context.Background()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t", HumanActorID: "u-alice"}
	a := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	if !a.OK {
		t.Fatalf("attach: %+v", a)
	}
	edge.setFrame(roomFrame("mitigated"))
	room := d.room(a.RoomKey)
	room.mu.Lock()
	room.frameAt = room.frameAt.Add(-FrameTTL - time.Second)
	room.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for {
		p := d.handler.Handle(ctx, Request{Op: "peek", WorkspaceKey: "ws"})
		if p.Rooms[0].Status.Status == "mitigated" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status still %q after the frame expired", p.Rooms[0].Status.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
