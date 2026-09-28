package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// TestTheStatusLineNamesTheRoomNotItsUUID: a person knows an incident as
// "Acme 42" or by its title. The UUID is what the status line used to print.
func TestTheStatusLineNamesTheRoomNotItsUUID(t *testing.T) {
	const uuid = "8a0c2f4e-1b7d-4c55-9f3a-2d6e8b1c0a77"
	for _, tc := range []struct {
		name     string
		incident client.Incident
		want     string
	}{
		{"display id", client.Incident{DisplayID: "Acme 42", Title: "Checkout 5xx spike"}, "🔴 Acme 42"},
		{"title only", client.Incident{Title: "Checkout 5xx spike"}, "🔴 landfall: Checkout 5xx spike"},
		{"nothing yet", client.Incident{}, "🔴 landfall"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edge := &fakeEdge{frame: &client.ContextFrame{Incident: tc.incident}}
			d, _ := testDaemon(t, edge, &fakeWire{})
			ctx := context.Background()
			cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: uuid, Token: "t"}
			if a := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}}); !a.OK {
				t.Fatalf("attach: %+v", a)
			}
			s := d.handler.Handle(ctx, Request{Op: "status", WorkspaceKey: "ws"})
			if !s.OK || s.Line != tc.want {
				t.Fatalf("status line = %q, want %q", s.Line, tc.want)
			}
			if strings.Contains(s.Line, uuid) || strings.Contains(s.Line, "#") {
				t.Fatalf("status line shows the incident id: %q", s.Line)
			}
		})
	}
}

// TestARestartedDaemonStillNamesTheRoom: the name is persisted with the room,
// so a daemon that comes back names it before any frame is read again.
func TestARestartedDaemonStillNamesTheRoom(t *testing.T) {
	edge := &fakeEdge{frame: &client.ContextFrame{Incident: client.Incident{DisplayID: "Acme 42"}}}
	d, _ := testDaemon(t, edge, &fakeWire{})
	ctx := context.Background()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	a := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	if !a.OK {
		t.Fatalf("attach: %+v", a)
	}
	st := d.room(a.RoomKey).Snapshot()
	if st.Name == nil || st.Name.DisplayID != "Acme 42" {
		t.Fatalf("snapshot name = %+v", st.Name)
	}

	// A fresh edge that has not been asked for a frame yet.
	restored, err := RestoreRoom(ctx, st, Deps{NewClient: func(client.Config) session.EdgeClient { return &fakeEdge{} }})
	if err != nil {
		t.Fatalf("RestoreRoom: %v", err)
	}
	defer restored.Close(ctx)
	if got := restored.Name(); got != (narrate.RoomName{DisplayID: "Acme 42"}) {
		t.Fatalf("restored name = %+v", got)
	}
}

// TestAFrameWithNoNameKeepsTheLastOne: a frame read that comes back without
// a title (a degraded answer) must not blank a name the person already saw.
func TestAFrameWithNoNameKeepsTheLastOne(t *testing.T) {
	edge := &fakeEdge{frame: &client.ContextFrame{Incident: client.Incident{Title: "Checkout 5xx spike"}}}
	d, _ := testDaemon(t, edge, &fakeWire{})
	ctx := context.Background()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	a := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	room := d.room(a.RoomKey)
	edge.mu.Lock()
	edge.frame = &client.ContextFrame{}
	edge.mu.Unlock()
	room.mu.Lock()
	room.frame = nil // force the next read past the cache
	room.mu.Unlock()
	if _, err := room.Frame(ctx); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if got := room.Name().Title; got != "Checkout 5xx spike" {
		t.Fatalf("name after an unnamed frame = %q", got)
	}
}

// TestATitleWithEscapesReachesNoTerminal: the title is the server's words and
// the status line and `landfall rooms` print it to the person's terminal. What
// the daemon keeps, persists, restores and answers with carries no control
// character, even from a state file written before names were cleaned.
func TestATitleWithEscapesReachesNoTerminal(t *testing.T) {
	const hostile = "Checkout \x1b[31m5xx\x1b[0m \x1b]0;owned\x07spike"
	edge := &fakeEdge{frame: &client.ContextFrame{Incident: client.Incident{Title: hostile}}}
	d, _ := testDaemon(t, edge, &fakeWire{})
	ctx := context.Background()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	a := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	if !a.OK {
		t.Fatalf("attach: %+v", a)
	}
	s := d.handler.Handle(ctx, Request{Op: "status", WorkspaceKey: "ws"})
	rooms := d.handler.Handle(ctx, Request{Op: "rooms"})
	for _, text := range []string{s.Line, rooms.Rooms[0].Title} {
		if strings.ContainsAny(text, "\x1b\x07") {
			t.Fatalf("a control character reached the answer: %q", text)
		}
	}
	if s.Line != "🔴 landfall: Checkout [31m5xx[0m ]0;ownedspike" {
		t.Fatalf("status line = %q", s.Line)
	}

	st := d.room(a.RoomKey).Snapshot()
	st.Name = &narrate.RoomName{Title: hostile} // as an older build would have saved it
	restored, err := RestoreRoom(ctx, st, Deps{NewClient: func(client.Config) session.EdgeClient { return &fakeEdge{} }})
	if err != nil {
		t.Fatalf("RestoreRoom: %v", err)
	}
	defer restored.Close(ctx)
	if got := restored.Name().Title; strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("a restored name kept a control character: %q", got)
	}
}
