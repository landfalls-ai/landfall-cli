package cli

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// outLines splits what the watch loop wrote into its lines.
func outLines(b *lockedBuffer) []string {
	return strings.Split(strings.TrimSpace(b.String()), "\n")
}

func TestWatchWritesOnlyChanges(t *testing.T) {
	var mu sync.Mutex
	count := 0
	reads := 0
	out := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = Watch(ctx, WatchDeps{
			Snapshot: func() WatchSnapshot {
				mu.Lock()
				defer mu.Unlock()
				reads++
				return WatchSnapshot{Type: "rooms", Rooms: []WatchRoom{{RoomKey: "r1", Count: count}}}
			},
			Out:  out,
			Tick: 5 * time.Millisecond,
		})
		close(done)
	}()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return reads >= 5 }, "the watch loop")
	if got := len(outLines(out)); got != 1 {
		t.Fatalf("an unchanged room was written %d times, want once", got)
	}
	mu.Lock()
	count = 2
	mu.Unlock()
	waitFor(t, func() bool { return len(outLines(out)) == 2 }, "the watch loop")
	if !strings.Contains(outLines(out)[1], `"count":2`) {
		t.Fatalf("second line = %s, want the new count", outLines(out)[1])
	}
	cancel()
	<-done
}

func TestWatchWakesOnPushAndFollowsRooms(t *testing.T) {
	var mu sync.Mutex
	rooms := []WatchRoom{{RoomKey: "a"}}
	subscribed := map[string]bool{}
	ended := map[string]bool{}
	var wakeA func()
	out := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Watch(ctx, WatchDeps{
			Snapshot: func() WatchSnapshot {
				mu.Lock()
				defer mu.Unlock()
				return WatchSnapshot{Type: "rooms", Rooms: append([]WatchRoom(nil), rooms...)}
			},
			Subscribe: func(sctx context.Context, key string, wake func()) {
				mu.Lock()
				subscribed[key] = true
				if key == "a" {
					wakeA = wake
				}
				mu.Unlock()
				<-sctx.Done()
				mu.Lock()
				ended[key] = true
				mu.Unlock()
			},
			Out:  out,
			Tick: time.Hour, // only a push can wake it
		})
	}()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return wakeA != nil }, "the watch loop")
	mu.Lock()
	rooms = []WatchRoom{{RoomKey: "b", Count: 1}}
	wake := wakeA
	mu.Unlock()
	wake()
	waitFor(t, func() bool { return len(outLines(out)) == 2 }, "the watch loop")
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return subscribed["b"] && ended["a"] }, "the watch loop")
}

func TestWatchEndsWhenOrphaned(t *testing.T) {
	done := make(chan struct{})
	go func() {
		_ = Watch(context.Background(), WatchDeps{
			Snapshot: func() WatchSnapshot { return WatchSnapshot{Type: "rooms"} },
			Out:      &lockedBuffer{},
			Tick:     time.Millisecond,
			Orphaned: func() bool { return true },
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watch kept running after its parent was gone")
	}
}

func TestWatchWritesEmptyRoomsAsList(t *testing.T) {
	out := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = Watch(ctx, WatchDeps{Snapshot: func() WatchSnapshot { return WatchSnapshot{Type: "rooms"} }, Out: out, Tick: time.Hour})
	}()
	waitFor(t, func() bool { return outLines(out)[0] != "" }, "the watch loop")
	cancel()
	if !strings.Contains(outLines(out)[0], `"rooms":[]`) {
		t.Fatalf("got %s, want rooms as an empty list so a reader never sees null", outLines(out)[0])
	}
}

func TestModOwnsEvent(t *testing.T) {
	set := func(string) string { return "claude-code" }
	unset := func(string) string { return "" }
	cases := []struct {
		event   string
		fromMod bool
		getenv  func(string) string
		want    bool
	}{
		{"user-prompt-submit", false, set, true},
		{"stop", false, set, true},
		{"file-changed", false, set, true},
		{"pre-tool-use", false, set, false},
		{"user-prompt-submit", true, set, false},
		{"user-prompt-submit", false, unset, false},
	}
	for _, c := range cases {
		if got := modOwnsEvent(c.event, c.fromMod, c.getenv); got != c.want {
			t.Errorf("modOwnsEvent(%q, fromMod=%v) = %v, want %v", c.event, c.fromMod, got, c.want)
		}
	}
}
