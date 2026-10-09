package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// A console joins a room with only the person's terminal reader; the agent
// adopts it on its next tool call. The console's watch holds a subscription,
// and that alone keeps the room past the grace (found live, 2026-10-09: the
// daemon left the room a minute after the console joined it).
func TestAWatchedRoomIsNotIdleAndGraceCountsFromTheLastSubscriber(t *testing.T) {
	d, _ := testDaemon(t, &fakeEdge{}, &fakeWire{})
	clock := time.Now()
	d.opts.Deps.Now = func() time.Time { return clock }
	ctx := context.Background()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	att := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "terminal:ws", Kind: "terminal", WorkspaceKey: "ws"}})
	if !att.OK {
		t.Fatalf("attach: %s", att.Error)
	}
	room := d.room(att.RoomKey)
	grace := d.opts.IdleGrace

	_, unsubscribe := room.SubscribeAll()
	_, unsubscribeSub := room.Subscribe()
	clock = clock.Add(10 * grace)
	if !room.IdleSince().IsZero() || d.idle() {
		t.Fatal("a room with a live subscriber must not idle out")
	}

	unsubscribe()
	clock = clock.Add(10 * grace)
	if !room.IdleSince().IsZero() || d.idle() {
		t.Fatal("one subscriber is still left; the room is still watched")
	}

	left := clock
	unsubscribeSub()
	if got := room.IdleSince(); !got.Equal(left) {
		t.Fatalf("the grace must count from the last subscriber leaving: %v, want %v", got, left)
	}
	clock = left.Add(grace / 2)
	if d.idle() {
		t.Fatal("inside the grace the room stays")
	}
	clock = left.Add(grace)
	if !d.idle() {
		t.Fatal("once the grace has passed with nobody watching, the room idles out")
	}
}

// A seat with no agent reader yet is the console's own; while the room is
// watched no seat is left, and the usual rule applies again afterwards.
func TestNoSeatIsLeftWhileTheRoomIsWatched(t *testing.T) {
	d, _, _ := seatDaemon(t)
	ctx := context.Background()
	a := attachAs(t, d, "Claude Code", "claude-code", "1", "")
	attachAs(t, d, "Codex", "codex", "2", "")
	d.handler.Handle(ctx, Request{Op: "detach", RoomKey: a.RoomKey, ReaderName: "codex:ws:2"})
	room := d.room(a.RoomKey)

	_, unsubscribe := room.SubscribeAll()
	room.ReapSeats(ctx, 0)
	if gone := room.ReapSeats(ctx, 0); len(gone) != 0 {
		t.Fatalf("a watched room keeps its seats: %v", gone)
	}
	unsubscribe()
	room.ReapSeats(ctx, time.Hour) // start the clock
	if gone := room.ReapSeats(ctx, 0); len(gone) != 1 || gone[0] != "Codex" {
		t.Fatalf("unwatched, the idle seat is left as before: %v", gone)
	}
}
