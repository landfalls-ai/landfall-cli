package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
)

// IdleGrace is how long the daemon lives after its last reader detaches.
const IdleGrace = 60 * time.Second

// Options wire the daemon to the world; the zero value is production.
type Options struct {
	Workspace hooks.Workspace
	Deps      Deps
	// IdleGrace overrides the default; tests shorten it.
	IdleGrace time.Duration
	Log       func(string)
}

// Daemon is the per-user room daemon.
type Daemon struct {
	opts    Options
	handler *Handler

	mu           sync.Mutex
	roomsByKey   map[string]*Room
	fingerprints map[string]*Fingerprints
	doorbells    map[string]*hooks.Doorbell
	state        *State
	statePath    string
	stopCh       chan struct{}
	stopOnce     sync.Once
}

// New builds a daemon (does not start it).
func New(opts Options) *Daemon {
	if opts.Log == nil {
		opts.Log = func(string) {}
	}
	if opts.IdleGrace <= 0 {
		opts.IdleGrace = IdleGrace
	}
	if opts.Deps.Log == nil {
		opts.Deps.Log = opts.Log
	}
	d := &Daemon{
		opts:         opts,
		roomsByKey:   map[string]*Room{},
		fingerprints: map[string]*Fingerprints{},
		doorbells:    map[string]*hooks.Doorbell{},
		statePath:    StatePath(hooks.RuntimeDir(opts.Workspace)),
		stopCh:       make(chan struct{}),
	}
	d.handler = &Handler{d: d}
	// The doorbell (hooks.Doorbell) is rung by the daemon now: for every
	// workspace whose person has a terminal reader, when a room goes from
	// nothing untold to something untold. Chained ahead of the caller's own
	// OnEvent (the OS notifier) so both fire.
	userOnEvent := d.opts.Deps.OnEvent
	d.opts.Deps.OnEvent = func(room *Room, evt client.Event) {
		d.ring(room, evt)
		if userOnEvent != nil {
			userOnEvent(room, evt)
		}
	}
	return d
}

// ring rings the doorbell for each terminal reader whose untold set just
// became non-empty with this event (the 0 → non-empty edge, as hooks.RingOnEdge).
func (d *Daemon) ring(room *Room, evt client.Event) {
	if isPlumbing(evt.Type) {
		return
	}
	for _, rd := range room.Readers() {
		if rd.Kind != KindTerminal || rd.Workspace == "" {
			continue
		}
		untold := room.UntoldFor(rd.Name)
		if len(untold) != 1 {
			continue
		}
		d.mu.Lock()
		bell, ok := d.doorbells[rd.Workspace]
		if !ok {
			bell = hooks.NewDoorbell(hooks.DoorbellOptions{Cwd: rd.Workspace, Log: d.opts.Log})
			d.doorbells[rd.Workspace] = bell
		}
		d.mu.Unlock()
		bell.Ring(len(untold))
	}
}

// Run is the whole daemon: lock, restore, listen, idle-exit. Returns when
// stopped (idle, `stop` verb, or ctx). A second daemon returns ErrAlreadyRunning.
func (d *Daemon) Run(ctx context.Context) error {
	rt := hooks.RuntimeDir(d.opts.Workspace)
	if err := os.MkdirAll(rt, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(rt, 0o700)

	unlock, err := lock(filepath.Join(rt, "daemon.lock"))
	if err != nil {
		return err
	}
	defer unlock()

	st, err := LoadState(d.statePath)
	if err != nil {
		d.opts.Log(err.Error())
	}
	d.mu.Lock()
	d.state = st
	d.mu.Unlock()
	d.restore(ctx, st)

	socketPath := hooks.DaemonSocketPath(d.opts.Workspace)
	if lim := hooks.MaxSocketPathLen(); len(socketPath) >= lim {
		return fmt.Errorf("daemon socket path %q is %d bytes, over the %d-byte limit on %s", socketPath, len(socketPath), lim, runtime.GOOS)
	}
	_ = os.Remove(socketPath) // we hold the lock; a leftover node is a crashed predecessor's
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	_ = os.Chmod(socketPath, 0o600)
	defer func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	}()
	d.opts.Log(fmt.Sprintf("room daemon listening at %s (pid %d, %d room(s) restored)", socketPath, os.Getpid(), len(d.rooms())))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go d.serveConn(runCtx, conn)
		}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			d.shutdown(true)
			return nil
		case <-d.stopCh:
			d.shutdown(true)
			return nil
		case <-ticker.C:
			if d.idle() {
				d.opts.Log("no reader for " + d.opts.IdleGrace.String() + "; leaving rooms and exiting")
				d.shutdown(false)
				return nil
			}
		}
	}
}

// idle: no room has a connected reader and the last one left longer than the
// grace ago (a daemon that never had a reader waits the grace from start).
func (d *Daemon) idle() bool {
	rooms := d.rooms()
	if len(rooms) == 0 {
		return false // nothing to leave; stay for the first attach (serve spawned us)
	}
	now := d.opts.Deps.now()
	for _, r := range rooms {
		since := r.IdleSince()
		if since.IsZero() || now.Sub(since) < d.opts.IdleGrace {
			return false
		}
	}
	return true
}

// shutdown leaves every room. keepRooms says whether they are written to
// state for the next daemon to rejoin: true for a stop or a signal (readers
// may still be there and will re-attach, spec FR-013), false for an idle exit
// (nobody was reading; rejoining a room nobody reads would put a phantom
// presence in it and stale rooms in every later status line).
func (d *Daemon) shutdown(keepRooms bool) {
	rooms := d.rooms()
	if !keepRooms {
		d.mu.Lock()
		d.roomsByKey = map[string]*Room{}
		d.mu.Unlock()
	}
	d.save()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, r := range rooms {
		r.Close(ctx)
	}
}

func (d *Daemon) requestStop() { d.stopOnce.Do(func() { close(d.stopCh) }) }

// restore rejoins persisted rooms with their stored tokens (research R6).
func (d *Daemon) restore(ctx context.Context, st *State) {
	for key, rs := range st.Rooms {
		room, err := RestoreRoom(ctx, rs, d.opts.Deps)
		if err != nil {
			d.opts.Log("could not rejoin " + key + ": " + err.Error())
			continue
		}
		d.opts.Log(fmt.Sprintf("restored %s with %d reader(s)", rs.Config.IncidentID, len(rs.Readers)))
		d.mu.Lock()
		d.roomsByKey[room.Key] = room
		d.mu.Unlock()
	}
}

func (d *Daemon) openOrAttach(ctx context.Context, cfg client.Config) (*Room, error) {
	key := RoomKey(cfg)
	d.mu.Lock()
	if r, ok := d.roomsByKey[key]; ok {
		d.mu.Unlock()
		return r, nil
	}
	d.mu.Unlock()
	room, err := OpenRoom(ctx, cfg, d.opts.Deps)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	if existing, ok := d.roomsByKey[key]; ok { // lost a race; keep the first
		d.mu.Unlock()
		room.Close(ctx)
		return existing, nil
	}
	d.roomsByKey[key] = room
	d.mu.Unlock()
	return room, nil
}

func (d *Daemon) room(key string) *Room {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.roomsByKey[key]
}

func (d *Daemon) rooms() []*Room {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*Room, 0, len(d.roomsByKey))
	for _, r := range d.roomsByKey {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (d *Daemon) setFingerprints(roomKey string, fp *Fingerprints) {
	d.mu.Lock()
	d.fingerprints[roomKey] = fp
	d.mu.Unlock()
}

// save persists rooms, readers and holds. Best-effort and logged; a failed
// save never fails a verb.
func (d *Daemon) save() {
	st := &State{V: StateVersion, Rooms: map[string]RoomState{}}
	for _, r := range d.rooms() {
		st.Rooms[r.Key] = r.Snapshot()
	}
	d.mu.Lock()
	d.state = st
	d.mu.Unlock()
	if err := SaveState(d.statePath, st); err != nil {
		d.opts.Log("could not save state: " + err.Error())
	}
}

// --- lifecycle helpers ------------------------------------------------------

// ErrAlreadyRunning is returned when another daemon holds the lock.
var ErrAlreadyRunning = errors.New("a room daemon is already running")

func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, ErrAlreadyRunning
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// Reachable reports whether a daemon answers on the socket.
func Reachable(ws hooks.Workspace) bool {
	_, err := reach(ws)
	return err == nil
}

// reach is Reachable with the reason. The timeout is generous on purpose: a
// `serve` asks this once, at start-up, often at the same moment the person's
// agent host, two teammates' agents and an investigation are all starting on
// the same machine (the terminal harness starts them within one second of
// each other). On 2026-09-21 a 500 ms probe missed a healthy daemon under that
// load and the session ran the whole afternoon in-process, with the
// working-directory hold out of reach (run terminal-1789965443125).
func reach(ws hooks.Workspace) (*Response, error) {
	return Send(hooks.DaemonSocketPath(ws), Request{Op: "rooms"}, 1500*time.Millisecond)
}

// SpawnWait is how long a `serve` waits for a daemon it just spawned. The
// daemon binds its socket only after restoring the rooms of its state file,
// and each restore is a join round trip plus a backfill against the server.
const SpawnWait = 8 * time.Second

// EnsureRunning returns once a daemon answers, spawning `<self> daemon`
// detached if none does. It never returns an error that should stop `serve`:
// the caller falls back to the per-process path (spec FR-006).
func EnsureRunning(ws hooks.Workspace, spawn func() error, log func(string)) bool {
	if os.Getenv("LANDFALL_DAEMON") == "0" {
		return false
	}
	if Reachable(ws) {
		return true
	}
	if spawn == nil {
		spawn = DefaultSpawn(ws)
	}
	if err := spawn(); err != nil {
		log("daemon unavailable (" + err.Error() + "); running in-process (one cursor per process)")
		return false
	}
	deadline := time.Now().Add(SpawnWait)
	var last error
	for time.Now().Before(deadline) {
		if _, last = reach(ws); last == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	log(fmt.Sprintf("daemon unavailable (did not answer within %s: %v); running in-process (one cursor per process)", SpawnWait, last))
	return false
}

// DefaultSpawn starts `landfall daemon` detached: its own session (Setsid) so
// the agent host closing our stdio does not take it down, stdio to daemon.log.
func DefaultSpawn(ws hooks.Workspace) func() error {
	return func() error {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		// Only the real binary spawns a daemon. A `go test` binary or anything
		// else that links this package must fall back, not fork itself with
		// "daemon" as its first argument.
		if !strings.HasPrefix(filepath.Base(self), "landfall") {
			return errors.New("not the landfall binary (" + filepath.Base(self) + "); refusing to spawn a daemon")
		}
		rt := hooks.RuntimeDir(ws)
		if err := os.MkdirAll(rt, 0o700); err != nil {
			return err
		}
		logf, err := os.OpenFile(filepath.Join(rt, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = logf.Close() }()
		cmd := exec.Command(self, "daemon")
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.Stdin = nil
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
}
