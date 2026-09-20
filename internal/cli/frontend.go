package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/bridge"
	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/mcp"
	"github.com/landfalls-ai/landfall-cli/internal/session"
	"github.com/landfalls-ai/landfall-cli/internal/tools"
)

// frontEnd is `serve` in daemon mode: this process is a READER of a room the
// daemon owns, not the room's owner. It still answers every MCP tool from its
// own HTTP client (reads are stateless), but it does not Join (the daemon did,
// once, for the machine), does not beat presence, does not watch the realtime
// socket, and does not keep its own cursor: its delta comes from the daemon
// for its own reader, and the per-pid hook socket it binds answers protocol-1
// hooks from the daemon's terminal reader (research R11).
type frontEnd struct {
	ws         hooks.Workspace
	socketPath string
	log        func(string)

	host     string // from the MCP initialize clientInfo, when the host sent one
	roomKey  string
	reader   string
	attached bool
	cfg      client.Config
	// lastEnsure rate-limits respawn attempts when the daemon has gone away.
	lastEnsure time.Time
}

func newFrontEnd(ws hooks.Workspace, log func(string)) *frontEnd {
	return &frontEnd{ws: ws, socketPath: hooks.DaemonSocketPath(ws), log: log}
}

// readerName is `<host>:<workspaceKey>:<pid>`: stable prefix per host and
// checkout (a returning session keeps its cursor within the TTL), pid for
// uniqueness between two windows on one checkout.
func (f *frontEnd) readerName() string {
	host := f.host
	if host == "" {
		host = os.Getenv("LANDFALL_AGENT_LABEL")
	}
	if host == "" {
		host = "agent"
	}
	return fmt.Sprintf("%s:%s:%d", host, hooks.WorkspaceKey(f.ws.Dir()), os.Getpid())
}

// onInitialize records the host the MCP client named itself as.
func (f *frontEnd) onInitialize(ci mcp.ClientInfo) {
	if ci.Name != "" {
		f.host = ci.Name
	}
}

// attach registers this process as a reader of cfg's room. The daemon joins
// the room if the machine has not yet; either way we get the machine's one
// agent instance id back and adopt it for our own HTTP client.
func (f *frontEnd) attach(cfg client.Config) (*daemon.Response, error) {
	res, err := daemon.Send(f.socketPath, daemon.Request{
		Op:   "attach",
		Room: &cfg,
		Reader: &daemon.ReaderSpec{
			Name: f.readerName(), Kind: string(daemon.KindAgent), Host: f.host,
			WorkspaceKey: hooks.WorkspaceKey(f.ws.Dir()), Workspace: f.ws.Dir(),
		},
		Fingerprints: computeFingerprints(f.ws.Dir()),
	}, 5*time.Second)
	if err != nil {
		return nil, err
	}
	f.roomKey, f.reader, f.attached, f.cfg = res.RoomKey, f.readerName(), true, cfg
	return res, nil
}

// ensure makes sure a daemon is answering before a read or a consume. A daemon
// that was stopped (`landfall daemon stop`, a crash) is respawned and this
// reader re-attaches under its own name, so it resumes at its own cursor from
// the daemon's persisted state (spec FR-013). Without this, a front end that
// lost its daemon had no room at all: no realtime watch of its own, and every
// daemon call failing quietly (found by the harness's --kill-daemon-at run,
// 2026-09-21). Rate-limited so a daemon that cannot start is not respawned on
// every tool call.
func (f *frontEnd) ensure() bool {
	if daemon.Reachable(f.ws) {
		return true
	}
	if time.Since(f.lastEnsure) < 5*time.Second {
		return false
	}
	f.lastEnsure = time.Now()
	if !daemon.EnsureRunning(f.ws, nil, f.log) {
		return false
	}
	if f.cfg.IncidentID == "" {
		return true
	}
	if _, err := f.attach(f.cfg); err != nil {
		f.log("room daemon came back but re-attach failed: " + err.Error())
		return false
	}
	f.log("room daemon restarted; re-attached as " + f.reader)
	return true
}

func (f *frontEnd) detach() {
	if !f.attached {
		return
	}
	_, _ = daemon.Send(f.socketPath, daemon.Request{Op: "detach", RoomKey: f.roomKey, ReaderName: f.reader}, time.Second)
	f.attached = false
}

// delta is this reader's own pull, rendered by the tool wrapper exactly as
// FlushPending's result was. A daemon that cannot answer means nothing owed
// this time, never an error on a tool result.
func (f *frontEnd) delta(context.Context) *client.FrameDelta {
	if !f.attached || !f.ensure() {
		return nil
	}
	res, err := daemon.Send(f.socketPath, daemon.Request{Op: "delta", RoomKey: f.roomKey, ReaderName: f.reader}, 3*time.Second)
	if err != nil || res.Delta == nil {
		return nil
	}
	if len(res.Delta.Items) == 0 && res.Delta.RoutineCount == 0 {
		return nil
	}
	return res.Delta
}

// clientFactory wraps the real HTTP client so that Join attaches to the daemon
// instead of joining the room a second time. The instance id the daemon holds
// is adopted, so contributions and heartbeats from this process carry the
// machine's one presence identity.
func (f *frontEnd) clientFactory(cfg client.Config) session.EdgeClient {
	return &attachingClient{Client: client.New(cfg, nil), fe: f, cfg: cfg}
}

type attachingClient struct {
	*client.Client
	fe  *frontEnd
	cfg client.Config
}

func (a *attachingClient) Join(context.Context) (*client.JoinResult, error) {
	res, err := a.fe.attach(a.cfg)
	if err != nil {
		return nil, fmt.Errorf("room daemon: %w", err)
	}
	a.Client.SetAgentInstanceID(res.AgentInstanceID)
	return &client.JoinResult{AgentInstanceID: res.AgentInstanceID}, nil
}

// Leave detaches this reader; the daemon leaves the room when the last one goes.
func (a *attachingClient) Leave(context.Context) error {
	a.fe.detach()
	return nil
}

// daemonSession answers the per-pid protocol-1 hook socket from the daemon's
// view of the TERMINAL reader for this workspace (research R11): a hook or a
// `landfall status` from an older binary still gets a correct answer.
type daemonSession struct {
	fe   *frontEnd
	sess *session.Session
}

func (d *daemonSession) peek() *daemon.RoomView {
	if !d.fe.ensure() {
		return nil
	}
	res, err := daemon.Send(d.fe.socketPath, daemon.Request{Op: "peek", WorkspaceKey: hooks.WorkspaceKey(d.fe.ws.Dir())}, time.Second)
	if err != nil {
		return nil
	}
	for i := range res.Rooms {
		if res.Rooms[i].RoomKey == d.fe.roomKey {
			return &res.Rooms[i]
		}
	}
	if len(res.Rooms) > 0 {
		return &res.Rooms[0]
	}
	return nil
}

func (d *daemonSession) Pending() []client.Event {
	if v := d.peek(); v != nil {
		return v.Events
	}
	return nil
}
func (d *daemonSession) PendingForHuman() []client.Event { return d.Pending() }
func (d *daemonSession) PendingDropped() int             { return 0 }
func (d *daemonSession) Cursor() int64 {
	if v := d.peek(); v != nil {
		return v.Cursor
	}
	return -1
}
func (d *daemonSession) Client() session.EdgeClient     { return d.sess.Client() }
func (d *daemonSession) Attention() *client.Attention   { return d.sess.Attention() }
func (d *daemonSession) Divergence() *client.Divergence { return d.sess.Divergence() }

// consume forwards a hook's delivery to the daemon for the terminal reader.
func (d *daemonSession) consume(upTo int64) int64 {
	if !d.fe.ensure() {
		return -1
	}
	res, err := daemon.Send(d.fe.socketPath, daemon.Request{
		Op: "consume", RoomKey: d.fe.roomKey, ReaderName: daemon.TerminalReaderName(hooks.WorkspaceKey(d.fe.ws.Dir())), UpTo: &upTo,
	}, time.Second)
	if err != nil || res.Cursor == nil {
		return -1
	}
	return *res.Cursor
}

// holdingAccepter is the daemon-mode Accepter: the spool accepts as always,
// then the daemon is asked what the text names from this workspace. A match in
// a room whose person has not allowed working-directory content holds the
// entry (spec FR-008); share_with_room tells the agent so through HeldReporter.
type holdingAccepter struct {
	inner *bridge.Accepter
	fe    *frontEnd
	mu    sync.Mutex
	held  map[string][]string
}

func newHoldingAccepter(inner *bridge.Accepter, fe *frontEnd) *holdingAccepter {
	return &holdingAccepter{inner: inner, fe: fe, held: map[string][]string{}}
}

func (h *holdingAccepter) Accept(incidentID, agentInstanceID, text string, refs []string, widget *tools.WidgetPayload, sourceQueryFailed bool, kind string) (string, bool, error) {
	id, redacted, err := h.inner.Accept(incidentID, agentInstanceID, text, refs, widget, sourceQueryFailed, kind)
	if err != nil || !h.fe.attached || !h.fe.ensure() {
		return id, redacted, err
	}
	res, merr := daemon.Send(h.fe.socketPath, daemon.Request{Op: "match", RoomKey: h.fe.roomKey, ReaderName: h.fe.reader, Text: text}, 2*time.Second)
	if merr != nil || res == nil || !res.Held {
		return id, redacted, nil
	}
	if herr := h.inner.Hold(incidentID, id, res.Matched); herr != nil {
		h.fe.log("hold: could not mark " + id + " held (" + herr.Error() + "); it will publish")
		return id, redacted, nil
	}
	h.mu.Lock()
	h.held[id] = res.Matched
	h.mu.Unlock()
	h.fe.log("held " + id + ": names the working directory (" + strings.Join(res.Matched, ", ") + "); `landfall allow-cwd` releases it")
	return id, redacted, nil
}

// HeldReason implements tools.HeldReporter.
func (h *holdingAccepter) HeldReason(id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held[id]
}
