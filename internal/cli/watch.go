package cli

// watch.go — `landfall watch`: the rooms this workspace reads, as a stream, for
// a host that can draw them while its session is idle. Today that host is the
// Claude Code mod (plugins/claude-code), which starts this command once per
// session and draws each snapshot in the band above the prompt.
//
// WHY A STREAM AND NOT THE STATUS LINE'S POLL. A hook runs only when the host
// fires an event, and the status line only when the host redraws it, so an
// idle session learned of room news on the next 5 s refresh at best. A mod can
// hold one child process for the session's life and redraw the moment that
// child writes, so the daemon's own push (the `subscribe` verb, research R10)
// can reach the person's screen without anyone typing.
//
// WHAT IT WRITES. One JSON object per line, and only when something the
// person would see changed:
//
//	{"type":"rooms","line":"<the status line>","rooms":[{...}]}
//
// Each room is the daemon's `peek` for this workspace's terminal reader:
// what the PERSON has not been told, the room at a glance, and the votes the
// person (not their agent) is asked for. Nothing here moves a cursor. Delivery to
// the agent stays with the user-prompt-submit hook, which the mod runs on the
// person's next message, so watching never counts as telling.
//
// WHEN IT WAKES. On every change a room's `subscribe` stream pushes, and on a
// slow tick as well, which picks up a room joined after start, a daemon that
// restarted, and the held count only this checkout's spool knows. The stream
// is asked for EVERY change (`all`), not the substantive events the hooks
// read: widget rows are plumbing to a hook but are exactly what the wall and
// its toast draw, this machine's own writes (a chart from the mod) change the
// band too, and the agent arriving is the band's "agent ✓". Measured in
// watch_live_test.go: a widget landing at the daemon is a line in well under a
// millisecond locally, against a 250 ms bound; a status change still waits
// for the frame read the daemon starts on it (the 250 ms re-poll below).
//
// WHEN A LINE IS WRITTEN. When the snapshot differs from the last one written
// with the self-growing ages left out (lastEventAgoMs, agent.sinceMs): those
// are as of the line, and a reader ages them itself. A room heard from again
// after a quiet spell of watchHeardStep is written even so, so that ageing
// does not run on from a stale value.
//
// WHAT IT COSTS. Nothing on the network per tick: the peek answers from the
// daemon's cache. The daemon reads the person's view (votes, claims, lines)
// when an event changes it or it is older than PersonTTL, and, while this
// watch is attached, the person's attention every PersonWatchedPoll: a
// teammate's staged claim is never pushed to an edge socket, so that poll is
// what makes its vote card arrive in a second and a half, not thirty. A read
// that lands with a new answer wakes this loop (daemon.PersonChanged).
//
// WHEN IT ENDS. On SIGINT/SIGTERM, when stdout is closed, or when the process
// that started it is gone (a mod's child is killed with its module, but a
// crashed host must not leave this running).

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/spf13/cobra"
)

// WatchTick is the slow wake between pushes.
const WatchTick = 2 * time.Second

// watchRefreshPoll is how soon the loop asks again while the daemon is
// reading a room's frame behind its answer (a status change, a join), so the
// new status is drawn within a moment of arriving, not a tick later.
const watchRefreshPoll = 250 * time.Millisecond

// watchResubscribe is how long a lost subscribe stream waits to retry.
const watchResubscribe = 2 * time.Second

// WatchRoom is one room as the stream describes it.
type WatchRoom struct {
	RoomKey      string `json:"roomKey"`
	IncidentID   string `json:"incidentId"`
	DisplayID    string `json:"displayId,omitempty"`
	Title        string `json:"title,omitempty"`
	Slug         string `json:"slug,omitempty"`
	Connection   string `json:"connection"`
	Count        int    `json:"count"`
	Addressed    int    `json:"addressed"`
	VotesAwaited int    `json:"votesAwaited"`
	MaxSeq       int64  `json:"maxSeq"`
	// Digest is the untold set, one line each, addressed lines first, as the
	// user-prompt-submit hook would hand them to the agent.
	Digest []string `json:"digest,omitempty"`
	// Status is the room at a glance: the incident's status and severity, who
	// is in it with their agents and the latest each added, Beacon's run and
	// step and last answer, the leading theory, the held lines, the focus and
	// the pinned scope. Absent from a daemon of an older build.
	Status *narrate.RoomStatus `json:"status,omitempty"`
	// Votes is every staged claim awaiting the PERSON's position, the human
	// view (contracts/cli-json.md §1 as amended by review finding 2). Always
	// present, empty when nothing waits; VotesAwaited stays the agent's count.
	Votes []narrate.Vote `json:"votes"`

	// The live view (specs/20261008-150000-edge-components/live.md):
	//
	// WidgetSeq is the newest seq of a widget-shaping event (a widget landed,
	// refreshed, failed, pinned or unpinned, an edge widget, the shared
	// arrangement saved); 0 when none since the daemon joined. A reader that
	// sees it move re-reads the wall.
	WidgetSeq int64 `json:"widgetSeq"`
	// NewestWidget is the newest widget that landed on the wall, with its
	// title, type and who put it there, when known; omitted when none has.
	NewestWidget *daemon.WidgetView `json:"newestWidget,omitempty"`
	// LastEventAgoMs is how long since the daemon last heard from the room (an
	// event or a frame read), as of this line; omitted when unknown. An age
	// growing is not a change: the stream does not write a line only for that
	// (see watchKey), so a reader adds the time since it received the line.
	LastEventAgoMs *int64 `json:"lastEventAgoMs,omitempty"`
	// Agent is this workspace's agent session (`landfall serve`) in the room.
	// Omitted only by a daemon of an older build.
	Agent *daemon.AgentView `json:"agent,omitempty"`
}

// WatchSnapshot is one line of the stream.
type WatchSnapshot struct {
	Type  string      `json:"type"`
	Line  string      `json:"line"`
	Rooms []WatchRoom `json:"rooms"`
}

// WatchDeps is everything Watch needs, injectable for tests.
type WatchDeps struct {
	// Snapshot reads the rooms once.
	Snapshot func() WatchSnapshot
	// Subscribe holds one room's push stream open, calling wake for each
	// event, and returns when the stream ends or ctx is done.
	Subscribe func(ctx context.Context, roomKey string, wake func())
	// Out receives the stream.
	Out io.Writer
	// Tick is the slow wake; zero means WatchTick.
	Tick time.Duration
	// Orphaned reports that the process that started this one is gone.
	Orphaned func() bool
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// watchHeardStep is how far the moment a room was last heard from must move
// before that alone is worth a line: a busy room's machinery (Beacon's
// queries) would otherwise redraw the band on every row.
const watchHeardStep = 5 * time.Second

// watchKey is a snapshot without the ages that grow by themselves, so a line
// is written when something happened, not because a clock moved.
func watchKey(snap WatchSnapshot) string {
	cp := snap
	cp.Rooms = make([]WatchRoom, len(snap.Rooms))
	for i, r := range snap.Rooms {
		r.LastEventAgoMs = nil
		if r.Agent != nil {
			a := *r.Agent
			a.SinceMs = nil
			r.Agent = &a
		}
		cp.Rooms[i] = r
	}
	body, _ := json.Marshal(cp)
	return string(body)
}

// Watch writes a snapshot line whenever one differs from the last, until ctx
// is done, a write fails, or Orphaned says so.
func Watch(ctx context.Context, deps WatchDeps) error {
	tick := deps.Tick
	if tick <= 0 {
		tick = WatchTick
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	wakeCh := make(chan struct{}, 1)
	wake := func() {
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}

	var mu sync.Mutex
	subscribed := map[string]context.CancelFunc{}
	defer func() {
		mu.Lock()
		for _, cancel := range subscribed {
			cancel()
		}
		mu.Unlock()
	}()
	// follow keeps one subscribe stream per room the snapshot names, and drops
	// the stream of a room that left it.
	follow := func(rooms []WatchRoom) {
		if deps.Subscribe == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		want := map[string]bool{}
		for _, r := range rooms {
			want[r.RoomKey] = true
			if _, ok := subscribed[r.RoomKey]; ok || r.RoomKey == "" {
				continue
			}
			subCtx, cancel := context.WithCancel(ctx)
			subscribed[r.RoomKey] = cancel
			go deps.Subscribe(subCtx, r.RoomKey, wake)
		}
		for key, cancel := range subscribed {
			if !want[key] {
				cancel()
				delete(subscribed, key)
			}
		}
	}

	now := deps.Now
	if now == nil {
		now = time.Now
	}
	last := ""
	// heard is when each room was last heard from, as of the last line.
	heard := map[string]time.Time{}
	for {
		snap := deps.Snapshot()
		if snap.Rooms == nil {
			snap.Rooms = []WatchRoom{}
		}
		follow(snap.Rooms)
		// The refreshing flag decides when to ask again; it is not something
		// the reader draws, so it stays off the stream.
		refreshing := false
		for _, r := range snap.Rooms {
			if r.Status != nil && r.Status.Refreshing {
				refreshing = true
				r.Status.Refreshing = false
			}
		}
		var soon <-chan time.Time
		if refreshing {
			soon = time.After(watchRefreshPoll)
		}
		key := watchKey(snap)
		at := now()
		write := key != last
		roomsHeard := map[string]time.Time{}
		for _, r := range snap.Rooms {
			if r.LastEventAgoMs == nil {
				continue
			}
			h := at.Add(-time.Duration(*r.LastEventAgoMs) * time.Millisecond)
			roomsHeard[r.RoomKey] = h
			if h.Sub(heard[r.RoomKey]) >= watchHeardStep {
				write = true
			}
		}
		if body, err := json.Marshal(snap); err == nil && write {
			if _, werr := deps.Out.Write(append(body, '\n')); werr != nil {
				return nil // the reader is gone; so is the reason to run
			}
			last, heard = key, roomsHeard
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-wakeCh:
		case <-soon:
		}
		if deps.Orphaned != nil && deps.Orphaned() {
			return nil
		}
	}
}

// watchPeekTimeout bounds the watch's one peek. Not a hook's 250 ms: a peek
// that reads the room's attention after a claim event waits on the network
// (up to 900 ms, peekAttentionBudget), and a watch that gave up sooner would
// write a line with no rooms in it, then the rooms again.
const watchPeekTimeout = daemon.RequestTimeout

// watchSnapshot reads the daemon's peek for this workspace's terminal reader.
// The status line is formatted from that same peek (the daemon's `status` is
// the same peek again) plus what only the spool knows (held items); only a
// daemon that does not answer falls back to StatusLine's own reads.
func watchSnapshot(ws hooks.Workspace) WatchSnapshot {
	snap := WatchSnapshot{Type: "rooms"}
	res, err := daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{Op: "peek", WorkspaceKey: hooks.WorkspaceKey(ws.Dir()), Harness: ws.Harness}, watchPeekTimeout)
	if err != nil || res == nil || !res.OK {
		snap.Line = StatusLine(ws)
		return snap
	}
	// The room this session is in now leads, on the status line and on the
	// stream alike: a reader of the stream takes rooms[0] as "the room".
	currentRooms(res.Rooms, hooks.WorkspaceKey(ws.Dir()), ws.Harness)
	if line := daemon.FormatStatusLine(res.Rooms); line != "" {
		ids := make([]string, 0, len(res.Rooms))
		for _, r := range res.Rooms {
			ids = append(ids, r.IncidentID)
		}
		snap.Line = line + spoolSuffix(ws, ids, time.Now())
	} else {
		snap.Line = StatusLine(ws)
	}
	snap.Rooms = watchRoomsOf(res.Rooms)
	return snap
}

// watchRoomsOf is the daemon's peek as the stream's rooms.
func watchRoomsOf(rooms []daemon.RoomView) []WatchRoom {
	var out []WatchRoom
	for _, r := range rooms {
		addressed := 0
		for _, e := range r.Events {
			if daemon.IsAddressed(e) {
				addressed++
			}
		}
		out = append(out, WatchRoom{
			RoomKey: r.RoomKey, IncidentID: r.IncidentID, DisplayID: r.DisplayID, Title: r.Title,
			Slug: r.Slug, Connection: string(r.Connection), Count: r.Count, Addressed: addressed,
			VotesAwaited: r.VotesAwaited, MaxSeq: r.MaxSeq, Digest: r.Digest, Status: r.Status,
			Votes:     votesOrEmpty(r.Votes),
			WidgetSeq: r.WidgetSeq, NewestWidget: r.NewestWidget, LastEventAgoMs: r.LastEventAgoMs, Agent: r.Agent,
		})
	}
	return out
}

// votesOrEmpty keeps `votes` an array on the wire, so a reader can take its
// length without a guard.
func votesOrEmpty(v []narrate.Vote) []narrate.Vote {
	if v == nil {
		return []narrate.Vote{}
	}
	return v
}

// watchSubscribe holds the daemon's subscribe stream for one room, waking the
// loop on every pushed event, and reconnects after a drop (a daemon restart).
func watchSubscribe(socketPath string) func(ctx context.Context, roomKey string, wake func()) {
	return func(ctx context.Context, roomKey string, wake func()) {
		for ctx.Err() == nil {
			subscribeOnce(ctx, socketPath, roomKey, wake)
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchResubscribe):
			}
		}
	}
}

func subscribeOnce(ctx context.Context, socketPath, roomKey string, wake func()) {
	conn, err := net.DialTimeout("unix", socketPath, hooks.SocketTimeout)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	// All: every change, not only the substantive events. Widget rows are
	// plumbing to the hooks (realtime.IsPlumbing) and this machine's own
	// writes are filtered from the substantive stream, yet both change what
	// the band and the wall show; so does the agent arriving.
	req, _ := json.Marshal(daemon.Request{Op: "subscribe", V: daemon.ProtocolVersion, RoomKey: roomKey, All: true})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	first := true
	for scanner.Scan() {
		if first {
			first = false // the ack; nothing has happened yet
			continue
		}
		wake()
	}
}

// newWatchCommand wires `landfall watch [--host <id>] [--tick-ms <n>]`. It is
// for a host to start, not a person to type, so it stays out of the help text.
func newWatchCommand(ui *UI) *cobra.Command {
	var host string
	var tickMs int
	c := newCommand(ui, "watch", func(*cobra.Command, []string) error {
		ws := hooks.Workspace{Harness: hooks.DetectHookHarness(host, "", os.Getenv)}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		parent := os.Getppid()
		return Watch(ctx, WatchDeps{
			Snapshot:  func() WatchSnapshot { return watchSnapshot(ws) },
			Subscribe: watchSubscribe(hooks.DaemonSocketPath(ws)),
			Out:       stdout,
			Tick:      time.Duration(tickMs) * time.Millisecond,
			Orphaned:  func() bool { return os.Getppid() != parent },
		})
	})
	c.Flags().StringVar(&host, "host", "", "the agent host this session runs in")
	c.Flags().IntVar(&tickMs, "tick-ms", int(WatchTick/time.Millisecond), "the slow wake between pushes, in milliseconds")
	c.Hidden = true
	return c
}
