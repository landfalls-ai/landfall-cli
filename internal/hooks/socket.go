// Package hooks implements the local query socket, the doorbell, the stage and
// the lifecycle-hook plumbing that make a running `landfall serve` answerable
// by a separate, short-lived hook process.
//
// socket.go is a Go port of `src/hooks/socket.mjs` (#225; the operator decision
// recorded on landfalls-ai/landfall#225).
//
// The problem it solves: a Stop/FileChanged hook is a SEPARATE, short-lived
// process spawned by the agent harness. The two things it needs — the queue of
// room events the session has not consumed, and that session's cursor — are
// in-process state on the serve session (internal/session). A hook cannot reach
// that memory.
//
// So `landfall serve` binds a listener and answers questions about state it
// already maintains. Deliberately NOT a new daemon: nothing to supervise, no
// PID file, no start/stop command — `landfall serve` is the process, made
// queryable.
//
//	Claude Code ──stdio──► landfall serve ◄──socket── landfall hooks stop
//	 (host)                  │  session.Pending()      (short-lived process)
//	                         │  session.Cursor()
//	                         ▼
//	                   watchIncident() ──ws──► core-api /realtime
//
// WHY A SOCKET AND NOT A STATE FILE. It is the only option that yields exact
// per-session cursors. Under a state file, "what has THIS session consumed?" is
// a snapshot race between processes; over a socket it has one correct answer —
// which, for a hook whose whole job is "you have not seen this yet", is the
// difference between correct and approximately correct.
//
// SECURITY. There is no network listener: a filesystem socket has no port and
// nothing routable. Access control is the 0700 DIRECTORY, not the socket file —
// some BSD-derived kernels (macOS included) historically ignore permission bits
// on the socket node itself. The directory mode is the entire enforcement
// story, and that is stated rather than glossed.
//
// The verb set is the other half of the boundary. `status`, `peek` and
// `consume` read state and move a cursor. There is deliberately NO verb that
// writes to the room, posts a finding, or returns the session token — this
// socket must never become a second way to ACT in a war room, only to ask
// about one.
//
// Windows named pipes are explicitly out of scope for this rewrite (spec
// FR-012), so `socketLocation`'s `win32` branch is not ported.
package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"crypto/sha256"
	"encoding/hex"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// SocketProtocolVersion is bumped only on a breaking change to the
// request/response shapes below. Fields have been ADDED without a bump before
// (`attention`, `incidentId`, `votesAwaited`, `divergence`) — every reader must
// therefore tolerate a missing field rather than requiring one.
const SocketProtocolVersion = 1

// SocketTimeout is how long a hook waits on one socket before giving up on it.
const SocketTimeout = 250 * time.Millisecond

// ServerTimeout bounds one server-side connection. `socket.mjs` spells this
// `SOCKET_TIMEOUT_MS * 4`; it is written out here because 1000ms is the number
// the contract names.
const ServerTimeout = 1000 * time.Millisecond

// maxRequestBytes bounds one request line. A hook payload is not a stream.
const maxRequestBytes = 1 << 20

// Workspace identifies which workspace's sockets, stage and status cache to
// look at. The zero value means "this process's cwd and environment", which is
// what every production caller wants; tests pass an explicit pair.
//
// The join key between a hook process and a serve process is the WORKSPACE
// directory: the harness runs hooks with cwd = the workspace and starts the MCP
// server there too, so cwd pairs them with no session id to thread anywhere.
type Workspace struct {
	// Cwd is the workspace directory. Empty means os.Getwd().
	Cwd string
	// Env overrides the process environment. Nil means os.Getenv.
	Env map[string]string
}

// Dir is the workspace directory this Workspace refers to.
func (w Workspace) Dir() string {
	if w.Cwd != "" {
		return w.Cwd
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// Getenv reads one variable from this Workspace's environment.
func (w Workspace) Getenv(key string) string {
	if w.Env != nil {
		return w.Env[key]
	}
	return os.Getenv(key)
}

// WorkspaceKey is the first 16 characters of the lowercase hex digest of
// sha256(realpath(cwd)) — 8 bytes rendered as 16 hex chars, not 16 raw bytes.
//
// This MUST stay byte-identical to the Node implementation's output for the
// same input (spec FR-007, `src/hooks/socket.mjs:58-66`:
// `createHash('sha256').update(real).digest('hex').slice(0, 16)`): during the
// transition, a Go `landfall hooks stop` must find a Node `landfall serve`'s
// socket and vice versa.
//
// filepath.EvalSymlinks is the Go equivalent of realpathSync. ON FAILURE THE
// RAW INPUT IS HASHED UNMODIFIED — no filepath.Clean, no trailing-separator
// normalization. Node hashes `String(cwd)` as given, and a port that "improves"
// the fallback diverges on exactly the inputs the fallback exists for.
func WorkspaceKey(cwd string) string {
	real := cwd
	if resolved, err := filepath.EvalSymlinks(real); err == nil {
		real = resolved
	}
	sum := sha256.Sum256([]byte(real))
	return hex.EncodeToString(sum[:])[:16]
}

// runtimeDir is where this workspace's sockets, stage and status cache live.
//
// XDG_RUNTIME_DIR is already per-user and 0700 on Linux. macOS does not set it,
// so fall back to ~/.local/state/landfall/run/. Shared with stage.go, which in
// Node repeats the same expression (`stage.mjs:48-57`) — one copy here so the
// two can never drift apart about which directory they mean.
func runtimeDir(ws Workspace) string {
	base := ws.Getenv("XDG_RUNTIME_DIR")
	if base != "" {
		return filepath.Join(base, "landfall")
	}
	home := ws.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".local", "state", "landfall", "run")
}

// SocketLocation is where this workspace's sockets live, and how they are
// named. One socket PER SERVE PROCESS, named by pid, because two agent windows
// on one repo are two sessions with two cursors.
type SocketLocation struct {
	Key string
	Dir string
}

// SocketLocationFor resolves the socket directory for a workspace.
func SocketLocationFor(ws Workspace) SocketLocation {
	key := WorkspaceKey(ws.Dir())
	return SocketLocation{Key: key, Dir: filepath.Join(runtimeDir(ws), key)}
}

// NameFor is the socket filename for a pid (or a `<pid>-<n>` collision name).
func (l SocketLocation) NameFor(pid string) string { return pid + ".sock" }

// PathFor is the absolute path of one socket name in this location.
func (l SocketLocation) PathFor(name string) string { return filepath.Join(l.Dir, name) }

// IsOurs reports whether a directory entry is one of our sockets.
func (l SocketLocation) IsOurs(name string) bool { return strings.HasSuffix(name, ".sock") }

// maxSocketPathLen is the sockaddr_un.sun_path cap: 104 bytes on macOS/BSD, 108
// on Linux (both include the NUL terminator, so a path is usable at strictly
// less than this).
//
// This is a real, hit-in-practice limit, not a theoretical one: the Node suite
// on macOS with the default $TMPDIR (/var/folders/<…>/T/) produces ~112-byte
// socket paths and 14 of its 358 tests fail because of it. An over-length bind
// fails with EINVAL, NOT EADDRINUSE, so the 3-attempt retry below does not
// cover it — it takes the log-and-return-nil branch, which is correct (binding
// is non-fatal) but silently disables lifecycle hooks. Hence the explicit
// pre-check in StartHookSocket, so the log line names the actual cause.
//
// Do NOT "fix" an over-long base by shortening the key or changing the layout:
// both are pinned by FR-007's cross-implementation discovery requirement.
func maxSocketPathLen() int {
	switch runtime.GOOS {
	case "linux", "android":
		return 108
	default:
		return 104
	}
}

// --- the protocol, wire shapes ----------------------------------------------

// SocketRequest is one request on the wire.
//
// `Op` is `any` rather than `string` so a nonsense `{"op": 123}` still reaches
// the default branch with the value spelled out, exactly as Node's template
// literal does, instead of failing to decode into a typed field.
type SocketRequest struct {
	Op   any `json:"op"`
	UpTo any `json:"upTo,omitempty"`
}

// StatusRequest, PeekRequest and ConsumeRequest are the three requests a caller
// ever sends. Constructors rather than literals so no caller has to remember
// the verb spelling.
func StatusRequest() SocketRequest { return SocketRequest{Op: "status"} }
func PeekRequest() SocketRequest   { return SocketRequest{Op: "peek"} }
func ConsumeRequest(upTo int64) SocketRequest {
	return SocketRequest{Op: "consume", UpTo: float64(upTo)}
}

func (r SocketRequest) verb() string {
	s, _ := r.Op.(string)
	return s
}

// opLabel is what the unknown-op error spells: JS's `${op ?? '(none)'}`.
func (r SocketRequest) opLabel() string {
	switch v := r.Op.(type) {
	case nil:
		return "(none)"
	case string:
		return v
	case float64:
		// JS String(123) is "123", not "123.000000".
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", v), "0"), ".")
	default:
		return fmt.Sprint(v)
	}
}

// StatusResponse is the `status` verb's answer.
type StatusResponse struct {
	OK  bool `json:"ok"`
	V   int  `json:"v"`
	PID int  `json:"pid"`

	IncidentID string `json:"incidentId"`
	Slug       string `json:"slug"`
	Connected  bool   `json:"connected"`
	Cursor     int64  `json:"cursor"`
	Pending    int    `json:"pending"`
	Dropped    int    `json:"dropped"`

	// Feature 20260812-010632 (US5/T047): `landfall status`'s own consumer (a
	// Claude Code statusline command, run on the host's normal render cadence).
	// ADDITIVE, same as `attention` on `peek` (#252) — the protocol version does
	// not move, an older hook ignores the field.
	//
	// Deliberately last-known, not force-refreshed: unlike `peek` (which gates a
	// hard Stop-hook decision and therefore settles attention), a statusline
	// render is advisory and happens on every prompt.
	VotesAwaited int `json:"votesAwaited"`

	// T049: same last-known, background-refreshed discipline. OMITTED — not
	// null, not `{diverging:false}` — until a refresh has actually landed, so a
	// reader can never mistake "nothing known yet" for a real answer.
	Divergence *client.Divergence `json:"divergence,omitempty"`
}

// PeekResponse is the `peek` verb's answer. Non-destructive, deliberately: a
// hook that crashes between asking and reporting must leave the events queued.
type PeekResponse struct {
	OK  bool `json:"ok"`
	V   int  `json:"v"`
	PID int  `json:"pid"`

	// Additive (#252 review): the join key between sockets is the WORKSPACE, so
	// two serve processes in one checkout can be in two different incidents.
	// Without this the hook has no way to tell their per-incident sequence
	// numbers apart, and dedupes one session's real blocker away against an
	// unrelated one that happens to share a seq.
	IncidentID string   `json:"incidentId"`
	Count      int      `json:"count"`
	Dropped    int      `json:"dropped"`
	Cursor     int64    `json:"cursor"`
	MaxSeq     int64    `json:"maxSeq"`
	Digest     []string `json:"digest"`

	// #252: what the ROOM is waiting on from this agent, carried on the same
	// round trip rather than as a second verb. Both answers are "what does this
	// session still owe?", and a Stop hook needs them together.
	//
	// Present-but-null when the session has no snapshot (never omitted), which
	// is what an older serve process's absent field already reads as.
	Attention *client.Attention `json:"attention"`
}

// ConsumeResponse is the `consume` verb's answer.
type ConsumeResponse struct {
	OK     bool  `json:"ok"`
	V      int   `json:"v"`
	PID    int   `json:"pid"`
	Cursor int64 `json:"cursor"`
}

// ErrorResponse is the default branch and the panic/error branch. Note there is
// deliberately NO `pid` field on it — matching `socket.mjs`'s object literal.
type ErrorResponse struct {
	OK    bool   `json:"ok"`
	V     int    `json:"v"`
	Error string `json:"error"`
}

// SocketResponse is the permissive READ shape: one struct that can hold any
// verb's answer, mirroring how JS simply reads whichever properties it wants
// off the parsed object. Writers use the narrow per-verb structs above; readers
// use this.
//
// Pointer fields are the ones where "absent" and "zero" are different answers.
type SocketResponse struct {
	OK    bool   `json:"ok"`
	V     int    `json:"v"`
	PID   int    `json:"pid,omitempty"`
	Error string `json:"error,omitempty"`

	IncidentID   string             `json:"incidentId,omitempty"`
	Slug         string             `json:"slug,omitempty"`
	Connected    bool               `json:"connected,omitempty"`
	Cursor       *int64             `json:"cursor,omitempty"`
	Pending      int                `json:"pending,omitempty"`
	Dropped      int                `json:"dropped,omitempty"`
	VotesAwaited int                `json:"votesAwaited,omitempty"`
	Divergence   *client.Divergence `json:"divergence,omitempty"`

	Count     *int              `json:"count,omitempty"`
	MaxSeq    *int64            `json:"maxSeq,omitempty"`
	Digest    []string          `json:"digest,omitempty"`
	Attention *client.Attention `json:"attention,omitempty"`

	// Raw is the exact bytes the server sent, when this response came off a
	// socket (or out of a cache/stage file). Re-emitting those bytes rather than
	// a re-encoding is what keeps a Go-written stage or status cache readable by
	// a Node `landfall status` with no field-presence drift.
	Raw json.RawMessage `json:"-"`
}

// MarshalJSON re-emits the bytes this response arrived as, when it has them.
func (r SocketResponse) MarshalJSON() ([]byte, error) {
	if len(r.Raw) > 0 {
		return r.Raw, nil
	}
	type plain SocketResponse
	return json.Marshal(plain(r))
}

// UnmarshalJSON decodes a response and remembers the bytes it came from.
func (r *SocketResponse) UnmarshalJSON(b []byte) error {
	type plain SocketResponse
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*r = SocketResponse(p)
	r.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// CursorOr is `response.cursor ?? fallback`.
func (r SocketResponse) CursorOr(fallback int64) int64 {
	if r.Cursor == nil {
		return fallback
	}
	return *r.Cursor
}

// CountOr is `response.count ?? fallback`.
func (r SocketResponse) CountOr(fallback int) int {
	if r.Count == nil {
		return fallback
	}
	return *r.Count
}

// MaxSeqOr is `response.maxSeq ?? fallback`.
func (r SocketResponse) MaxSeqOr(fallback int64) int64 {
	if r.MaxSeq == nil {
		return fallback
	}
	return *r.MaxSeq
}

// SocketAnswer is one socket's answer, tagged with the socket it came from so a
// follow-up `consume` reaches the right session.
type SocketAnswer struct {
	SocketPath string         `json:"socketPath"`
	Response   SocketResponse `json:"response"`
}

// --- the handler ------------------------------------------------------------

// SocketSession is the live serve-session state the socket answers questions
// about. *session.Session satisfies it.
type SocketSession interface {
	Pending() []client.Event
	PendingDropped() int
	Cursor() int64
	Client() session.EdgeClient
	Attention() *client.Attention
	Divergence() *client.Divergence
}

// AttentionSettler is the OPTIONAL half of the #252 fix — see
// AnswerSocketRequest. Node tests `typeof session?.settleAttention ===
// 'function'`; an optional interface assertion is the Go spelling of the same
// capability check, and a session that does not offer it is answered
// synchronously, exactly as before.
type AttentionSettler interface {
	SettleAttention(ctx context.Context, timeout time.Duration) *client.Attention
}

// ConsumeFunc moves a session's cursor for the `consume` verb. Injected rather
// than called on the session directly, matching `socket.mjs`'s `opts.consume`:
// a nil ConsumeFunc leaves the cursor exactly where it was and reports it,
// which is what a read-only caller (and every test that is not exercising the
// cursor move) wants. `landfall serve` passes (*session.Session).ConsumeUpTo.
type ConsumeFunc func(s SocketSession, upTo int64) int64

// HandleOptions are the per-answer knobs `socket.mjs` takes as its third arg.
type HandleOptions struct {
	// PID is reported on every ok answer. Zero means os.Getpid().
	PID int
	// Consume performs the `consume` verb's cursor move. Nil is a no-op.
	Consume ConsumeFunc
}

func (o HandleOptions) pid() int {
	if o.PID != 0 {
		return o.PID
	}
	return os.Getpid()
}

// HandleSocketRequest handles one request against a live bridge session. Pure
// apart from the cursor move `consume` performs — which is why the whole
// protocol is testable without a socket, a serve process or a war room.
//
// The returned value is whichever narrow per-verb struct the verb answers with;
// it is always JSON-marshalable. KEEP THIS FUNCTION I/O-FREE: the
// synchronous/async split with AnswerSocketRequest is deliberate, and it is
// what makes the protocol testable in isolation.
func HandleSocketRequest(req SocketRequest, s SocketSession, opts HandleOptions) any {
	pid := opts.pid()
	pending := sessionPending(s)

	switch req.verb() {
	case "status":
		incidentID, slug, connected := room(s)
		return StatusResponse{
			OK:           true,
			V:            SocketProtocolVersion,
			PID:          pid,
			IncidentID:   incidentID,
			Slug:         slug,
			Connected:    connected,
			Cursor:       sessionCursor(s),
			Pending:      len(pending),
			Dropped:      sessionDropped(s),
			VotesAwaited: votesAwaited(s),
			Divergence:   sessionDivergence(s),
		}

	case "peek":
		incidentID, _, _ := room(s)
		cursor := sessionCursor(s)
		maxSeq := cursor
		if n := len(pending); n > 0 {
			maxSeq = pending[n-1].SeqOr(cursor)
		}
		digest := make([]string, 0, len(pending))
		for _, e := range pending {
			digest = append(digest, narrate.FormatEventLine(e))
		}
		return PeekResponse{
			OK:         true,
			V:          SocketProtocolVersion,
			PID:        pid,
			IncidentID: incidentID,
			Count:      len(pending),
			Dropped:    sessionDropped(s),
			Cursor:     cursor,
			MaxSeq:     maxSeq,
			Digest:     digest,
			Attention:  sessionAttention(s),
		}

	case "consume":
		upTo, ok := req.UpTo.(float64)
		if !ok {
			return ErrorResponse{OK: false, V: SocketProtocolVersion, Error: "consume requires a numeric upTo"}
		}
		cursor := sessionCursor(s)
		if opts.Consume != nil {
			cursor = opts.Consume(s, int64(upTo))
		}
		return ConsumeResponse{OK: true, V: SocketProtocolVersion, PID: pid, Cursor: cursor}

	default:
		return ErrorResponse{
			OK:    false,
			V:     SocketProtocolVersion,
			Error: "unknown op: " + req.opLabel(),
		}
	}
}

// AnswerSocketRequest is HandleSocketRequest, plus the one thing that has to
// happen before a `peek` can be answered honestly: bring the attention snapshot
// up to date.
//
// WHY IT HAS TO HAPPEN AT ALL. `peek`'s `attention` was previously whatever the
// session last happened to hold, on the reasoning that the serve process kept
// it current from the realtime socket. Nothing did: a `claim.*`/`context.*`
// event arriving only set a dirty flag, and the sole reader of that flag was a
// tool call — which the Stop hook never makes. An agent whose last tool call
// predated a quarantine therefore concluded against a blocker-free snapshot,
// which is precisely the failure the Stop-hook tier exists to prevent.
//
// Best-effort and bounded well inside the hook's own 250ms budget (see
// session.SettleBudget): a refresh that fails leaves the previous snapshot in
// place, and a hook must never be blocked by the thing it is asking about.
//
// DO NOT call HandleSocketRequest alone from a socket server and call the
// contract satisfied — this await is the whole #252 fix.
func AnswerSocketRequest(ctx context.Context, req SocketRequest, s SocketSession, opts HandleOptions) any {
	if req.verb() == "peek" {
		if settler, ok := s.(AttentionSettler); ok && s != nil {
			settler.SettleAttention(ctx, 0) // 0 → session.SettleBudget
		}
	}
	return HandleSocketRequest(req, s, opts)
}

// The `session?.x ?? default` optional chains, one place each.

func sessionPending(s SocketSession) []client.Event {
	if s == nil {
		return nil
	}
	return s.Pending()
}

func sessionCursor(s SocketSession) int64 {
	if s == nil {
		return -1
	}
	return s.Cursor()
}

func sessionDropped(s SocketSession) int {
	if s == nil {
		return 0
	}
	return s.PendingDropped()
}

func sessionAttention(s SocketSession) *client.Attention {
	if s == nil {
		return nil
	}
	return s.Attention()
}

func sessionDivergence(s SocketSession) *client.Divergence {
	if s == nil {
		return nil
	}
	return s.Divergence()
}

func votesAwaited(s SocketSession) int {
	a := sessionAttention(s)
	if a == nil {
		return 0
	}
	return len(a.VotesAwaited)
}

// room is `session?.client?.cfg?.{incidentId,slug}` plus
// `connected: Boolean(session?.client)`.
func room(s SocketSession) (incidentID, slug string, connected bool) {
	if s == nil {
		return "", "", false
	}
	cl := s.Client()
	if cl == nil {
		return "", "", false
	}
	cfg := cl.Config()
	return cfg.IncidentID, cfg.Slug, true
}

// --- the server -------------------------------------------------------------

// BoundSocket is a bound query socket for one serve session.
type BoundSocket struct {
	SocketPath string

	ln        net.Listener
	closeOnce sync.Once
}

// StartOptions configures StartHookSocket.
type StartOptions struct {
	Workspace Workspace
	// PID names the socket file. Zero means os.Getpid().
	PID int
	// Consume performs the `consume` verb's cursor move (see ConsumeFunc).
	Consume ConsumeFunc
	// Log receives the one diagnostic line a failed bind produces. Nil discards
	// it — but production callers should always pass one: an unbindable socket
	// silently disables every lifecycle hook, and the line is how that is
	// diagnosed.
	Log func(string)
}

func (o StartOptions) pid() int {
	if o.PID != 0 {
		return o.PID
	}
	return os.Getpid()
}

func (o StartOptions) log(msg string) {
	if o.Log != nil {
		o.Log(msg)
	}
}

// StartHookSocket binds the query socket for a serve session.
//
// Returns nil when binding is impossible — a serve process that cannot offer
// the socket must still serve MCP, so EVERY failure here is non-fatal by
// design. That is also why this returns no error: there is no caller that
// should treat one as fatal, and the diagnostic goes to Log instead.
func StartHookSocket(ctx context.Context, s SocketSession, opts StartOptions) *BoundSocket {
	loc := SocketLocationFor(opts.Workspace)
	if err := os.MkdirAll(loc.Dir, 0o700); err != nil {
		opts.log(fmt.Sprintf("hook socket unavailable (%v) — lifecycle hooks will not see this session.", err))
		return nil
	}
	// MkdirAll's mode only applies to directories it creates, so a pre-existing
	// one keeps whatever mode it had. The 0700 directory is the entire
	// access-control story; make it so explicitly.
	if err := os.Chmod(loc.Dir, 0o700); err != nil {
		opts.log(fmt.Sprintf("hook socket unavailable (%v) — lifecycle hooks will not see this session.", err))
		return nil
	}

	pid := opts.pid()
	socketPath := loc.PathFor(loc.NameFor(fmt.Sprint(pid)))

	var ln net.Listener
	for attempt := 0; attempt < 3; attempt++ {
		// Named explicitly rather than left to fail as an opaque EINVAL: an
		// over-long $XDG_RUNTIME_DIR (or $HOME) is the single most likely reason
		// a bind fails on macOS, and "invalid argument" is not a diagnosable
		// message for it.
		if lim := maxSocketPathLen(); len(socketPath) >= lim {
			opts.log(fmt.Sprintf(
				"hook socket unavailable (path %q is %d bytes, over the %d-byte sockaddr_un.sun_path limit on %s) — lifecycle hooks will not see this session.",
				socketPath, len(socketPath), lim, runtime.GOOS))
			return nil
		}
		var err error
		ln, err = net.Listen("unix", socketPath)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EADDRINUSE) || attempt == 2 {
			opts.log(fmt.Sprintf("hook socket unavailable (%v) — lifecycle hooks will not see this session.", err))
			return nil
		}
		// EADDRINUSE: a crashed sibling leaves the node behind. Connecting tells
		// the two apart — dead means unlink and rebind, alive means take the next
		// name rather than evict a live session.
		if probe(socketPath, SocketTimeout) {
			socketPath = loc.PathFor(loc.NameFor(fmt.Sprintf("%d-%d", pid, attempt+1)))
		} else {
			_ = os.Remove(socketPath)
		}
	}
	if ln == nil {
		opts.log("hook socket unavailable (bind exhausted 3 attempts) — lifecycle hooks will not see this session.")
		return nil
	}

	// Belt-and-braces; some BSD-derived kernels ignore this on a socket node,
	// which is exactly why the directory mode above is the real control.
	_ = os.Chmod(socketPath, 0o600)

	b := &BoundSocket{SocketPath: socketPath, ln: ln}
	// The accept loop is a goroutine, so nothing holds the process open on the
	// socket's account — the Go equivalent of `server.unref()`.
	go b.serve(ctx, s, HandleOptions{PID: pid, Consume: opts.Consume})
	return b
}

func (b *BoundSocket) serve(ctx context.Context, s SocketSession, opts HandleOptions) {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return // the listener is closed; nothing left to serve
		}
		go serveConn(ctx, conn, s, opts)
	}
}

// serveConn answers exactly one request. One request per connection: a hook
// lives for milliseconds, and a long-lived subscription is the second lifecycle
// this design avoids.
func serveConn(ctx context.Context, conn net.Conn, s SocketSession, opts HandleOptions) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ServerTimeout))

	r := bufio.NewReader(io.LimitReader(conn, maxRequestBytes))
	line, err := r.ReadString('\n')
	if err != nil {
		return // no complete request arrived inside the deadline
	}

	var req SocketRequest
	// A malformed line leaves the zero request, which the default branch
	// answers with `unknown op: (none)`.
	_ = json.Unmarshal([]byte(strings.TrimSpace(line)), &req)

	response := answerOrError(ctx, req, s, opts)
	body, err := json.Marshal(response)
	if err != nil {
		body, _ = json.Marshal(ErrorResponse{OK: false, V: SocketProtocolVersion, Error: err.Error()})
	}
	_, _ = conn.Write(append(body, '\n'))
}

// answerOrError turns a handler panic into a RESPONSE, not a dropped
// connection — `{ok:false, v, error:<message>}`, as the contract requires.
func answerOrError(ctx context.Context, req SocketRequest, s SocketSession, opts HandleOptions) (out any) {
	defer func() {
		if rec := recover(); rec != nil {
			out = ErrorResponse{OK: false, V: SocketProtocolVersion, Error: fmt.Sprint(rec)}
		}
	}()
	return AnswerSocketRequest(ctx, req, s, opts)
}

// Close stops answering and removes the socket node. Idempotent.
func (b *BoundSocket) Close() error {
	var err error
	b.closeOnce.Do(func() {
		err = b.ln.Close()
		_ = os.Remove(b.SocketPath)
	})
	return err
}

// probe reports whether something is already listening at socketPath (vs. a
// stale node left by a crashed sibling).
func probe(socketPath string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// --- the client -------------------------------------------------------------

// ListHookSockets is every socket currently present for this workspace, sorted.
// Empty is the fast path: no directory means no serve process has ever run
// here — the common case for a hook, and the one that must cost nothing.
func ListHookSockets(ws Workspace) []string {
	loc := SocketLocationFor(ws)
	entries, err := os.ReadDir(loc.Dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if loc.IsOurs(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, loc.PathFor(n))
	}
	return out
}

// SendToSocket sends one request to one socket and reads one response. Fails
// rather than hanging on a stale node. A zero timeout means SocketTimeout.
func SendToSocket(socketPath string, req SocketRequest, timeout time.Duration) (*SocketResponse, error) {
	if timeout <= 0 {
		timeout = SocketTimeout
	}
	deadline := time.Now().Add(timeout)
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return nil, err
	}

	line, err := bufio.NewReader(io.LimitReader(conn, maxRequestBytes)).ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("socket closed before a response: %w", err)
	}
	var out SocketResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QueryHookSockets asks every socket in this workspace the same question
// concurrently and keeps the answers that arrived, each tagged with the socket
// it came from so a follow-up `consume` reaches the right session.
//
// A hook UNIONS across sockets. That over-reports (you may be shown a sibling
// session's context) and never under-reports — and under-reporting is the exact
// failure this feature exists to prevent, so it is the right direction to be
// wrong in. An unreachable socket is silently skipped: it is a crashed
// session's leftover far more often than a live one that owes us an answer.
//
// Only `ok:true` answers survive. Order follows ListHookSockets, matching
// Node's order-preserving Promise.all.
func QueryHookSockets(req SocketRequest, ws Workspace, timeout time.Duration) []SocketAnswer {
	sockets := ListHookSockets(ws)
	if len(sockets) == 0 {
		return nil
	}
	results := make([]*SocketResponse, len(sockets))
	var wg sync.WaitGroup
	for i, socketPath := range sockets {
		wg.Add(1)
		go func(i int, socketPath string) {
			defer wg.Done()
			resp, err := SendToSocket(socketPath, req, timeout)
			if err != nil {
				return // dropped silently, by design
			}
			results[i] = resp
		}(i, socketPath)
	}
	wg.Wait()

	answers := make([]SocketAnswer, 0, len(sockets))
	for i, resp := range results {
		if resp == nil || !resp.OK {
			continue
		}
		answers = append(answers, SocketAnswer{SocketPath: sockets[i], Response: *resp})
	}
	return answers
}
