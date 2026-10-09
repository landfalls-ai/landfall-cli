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
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
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

	// mu guards everything below: the MCP loop writes host/harness on
	// initialize, a background join and ensure() write the attachment, and
	// the hook socket's goroutines read all of it.
	mu       sync.Mutex
	host     string        // from the MCP initialize clientInfo, when the host sent one
	harness  hooks.Harness // the same, mapped to a key and a room label
	roomKey  string
	reader   string
	attached bool
	cfg      client.Config
	// link is LinkHash of the share link this front end is joining with, sent
	// on the next attach so the room records it.
	link string
	// lastEnsure rate-limits respawn attempts when the daemon has gone away.
	lastEnsure time.Time
	// seatPin is the seat label an adoption (adopt.go) joins under, set only
	// for the length of that join. See seatLabel.
	seatPin string
}

func newFrontEnd(ws hooks.Workspace, log func(string)) *frontEnd {
	return &frontEnd{ws: ws, socketPath: hooks.DaemonSocketPath(ws), log: log}
}

// attachment is a consistent snapshot of what this front end is attached to.
type attachment struct {
	roomKey, reader string
	attached        bool
	harness         string
}

func (f *frontEnd) snapshot() attachment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return attachment{roomKey: f.roomKey, reader: f.reader, attached: f.attached, harness: f.harness.Key}
}

// readerName is `<host>:<workspaceKey>:<pid>`: stable prefix per host and
// checkout (a returning session keeps its cursor within the TTL), pid for
// uniqueness between two windows on one checkout.
func (f *frontEnd) readerName() string {
	f.mu.Lock()
	host := f.host
	f.mu.Unlock()
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
	if ci.Name == "" {
		return
	}
	f.mu.Lock()
	f.host = ci.Name
	f.harness = hooks.HarnessFromClientName(ci.Name)
	f.mu.Unlock()
}

// seatLabel is the label this harness's agent session joins the room under:
// LANDFALL_AGENT_LABEL when the person set one, else the host's own name
// ("Claude Code", "Codex"), else whatever the config already carries.
//
// While an adoption is joining (pinSeat), the label is the one the room's
// terminal join already holds, whatever LANDFALL_AGENT_LABEL says: a second
// label would be a second /edge/join, a second presence, and a seat that
// leaves the room 60 s later while every later read from this folder still
// names it. The person's custom label is therefore not shown for a room they
// put this folder in from the mod; it applies to every join the agent makes
// itself (a pasted link, join_war_room, a startup link).
func (f *frontEnd) seatLabel(cfg client.Config) string {
	f.mu.Lock()
	pin := f.seatPin
	f.mu.Unlock()
	if pin != "" {
		return pin
	}
	if env := os.Getenv("LANDFALL_AGENT_LABEL"); env != "" {
		return env
	}
	f.mu.Lock()
	label := f.harness.Label
	f.mu.Unlock()
	if label != "" {
		return label
	}
	if cfg.AgentLabel != "" {
		return cfg.AgentLabel
	}
	return daemon.DefaultSeatLabel
}

// pinSeat makes every join until the returned release use label (an adoption's
// seat). An empty label pins nothing.
func (f *frontEnd) pinSeat(label string) (release func()) {
	f.mu.Lock()
	f.seatPin = label
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.seatPin = ""
		f.mu.Unlock()
	}
}

func (f *frontEnd) setLink(hash string) {
	f.mu.Lock()
	f.link = hash
	f.mu.Unlock()
}

// redeemLink turns a share link into a config through the daemon's link book
// (internal/daemon/links.go), so a single-use link is redeemed once per
// machine:
//
//   - a link this machine already redeemed answers with that session (a
//     second harness, or a retry after a join that failed);
//   - a link a sibling is redeeming right now waits for that redeem;
//   - otherwise this front end holds the claim, redeems, and records the
//     session with the daemon at once, before any join can fail.
//
// With no daemon to ask, the link is simply redeemed.
func (f *frontEnd) redeemLink(ctx context.Context, link string, redeem func(context.Context, string) (*client.Config, error)) (*client.Config, error) {
	hash := daemon.LinkHash(link)
	if hash == "" || !f.ensure() {
		return redeem(ctx, link)
	}
	res, err := daemon.Send(f.socketPath, daemon.Request{Op: "link", Link: hash}, daemon.LinkWait+3*time.Second)
	switch {
	case err != nil && res != nil && !res.OK:
		// The daemon answered: a sibling is still redeeming. Redeeming here
		// as well would spend the link out from under it.
		return nil, err
	case err != nil:
		f.log("room daemon did not answer about the link (" + err.Error() + "); redeeming it here")
		return redeem(ctx, link)
	case res.Room != nil:
		f.setLink(hash)
		f.log(fmt.Sprintf("this link was already redeemed on this machine (incident %s); joining with that session.", res.Room.IncidentID))
		cfg := *res.Room
		return &cfg, nil
	}
	cfg, rerr := redeem(ctx, link)
	if rerr != nil || cfg == nil {
		_, _ = daemon.Send(f.socketPath, daemon.Request{Op: "link-failed", Link: hash}, time.Second)
		if rerr == nil {
			rerr = fmt.Errorf("the link did not resolve to an incident")
		}
		return nil, rerr
	}
	if _, serr := daemon.Send(f.socketPath, daemon.Request{Op: "link-redeemed", Link: hash, Room: cfg}, time.Second); serr != nil {
		f.log("could not record the redeemed link with the room daemon: " + serr.Error())
	}
	f.setLink(hash)
	return cfg, nil
}

// attach registers this process as a reader of cfg's room. The daemon joins
// the room if the machine has not yet; either way we get the machine's one
// agent instance id back and adopt it for our own HTTP client.
//
// The attach names this harness (its key, and its label on the room config):
// the daemon joins the room separately for each harness, so the agent id that
// comes back is this harness's own, never a sibling's. timeout bounds the
// whole attach, the daemon's join included.
func (f *frontEnd) attach(cfg client.Config, timeout time.Duration) (*daemon.Response, error) {
	cfg.AgentLabel = f.seatLabel(cfg)
	name := f.readerName()
	f.mu.Lock()
	link, host, harness := f.link, f.host, f.harness.Key
	f.mu.Unlock()
	if timeout <= 0 || timeout > daemon.AttachTimeout {
		timeout = daemon.AttachTimeout
	}
	res, err := daemon.Send(f.socketPath, daemon.Request{
		Op:   "attach",
		Room: &cfg,
		Link: link,
		Reader: &daemon.ReaderSpec{
			Name: name, Kind: string(daemon.KindAgent), Host: host,
			WorkspaceKey: hooks.WorkspaceKey(f.ws.Dir()), Workspace: f.ws.Dir(),
			Harness: harness,
		},
		Fingerprints: computeFingerprints(f.ws.Dir()),
	}, timeout)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.roomKey, f.reader, f.attached, f.cfg = res.RoomKey, name, true, cfg
	f.mu.Unlock()
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
	f.mu.Lock()
	if time.Since(f.lastEnsure) < 5*time.Second {
		f.mu.Unlock()
		return false
	}
	f.lastEnsure = time.Now()
	cfg := f.cfg
	f.mu.Unlock()
	if !daemon.EnsureRunning(f.ws, nil, f.log) {
		return false
	}
	if cfg.IncidentID == "" {
		return true
	}
	if _, err := f.attach(cfg, 0); err != nil {
		f.log("room daemon came back but re-attach failed: " + err.Error())
		return false
	}
	f.log("room daemon restarted; re-attached as " + f.snapshot().reader)
	return true
}

func (f *frontEnd) detach() {
	f.mu.Lock()
	at, roomKey, reader := f.attached, f.roomKey, f.reader
	f.attached = false
	f.mu.Unlock()
	if !at {
		return
	}
	_, _ = daemon.Send(f.socketPath, daemon.Request{Op: "detach", RoomKey: roomKey, ReaderName: reader}, time.Second)
}

// delta is this reader's own pull, rendered by the tool wrapper exactly as
// FlushPending's result was. A daemon that cannot answer means nothing owed
// this time, never an error on a tool result.
func (f *frontEnd) delta(context.Context) *client.FrameDelta {
	if !f.snapshot().attached || !f.ensure() {
		return nil
	}
	at := f.snapshot()
	res, err := daemon.Send(f.socketPath, daemon.Request{Op: "delta", RoomKey: at.roomKey, ReaderName: at.reader}, 3*time.Second)
	if err != nil || res.Delta == nil {
		return nil
	}
	if len(res.Delta.Items) == 0 && res.Delta.RoutineCount == 0 {
		return nil
	}
	return res.Delta
}

// updates is get_updates in daemon mode: the same reader, the same cursor as
// the flush above. ok=false (not attached, no daemon) sends get_updates back
// to the process's own cursor.
func (f *frontEnd) updates(_ context.Context, since *int64) (*client.FrameDelta, int64, bool) {
	if !f.snapshot().attached || !f.ensure() {
		return nil, -1, false
	}
	at := f.snapshot()
	res, err := daemon.Send(f.socketPath, daemon.Request{Op: "delta", RoomKey: at.roomKey, ReaderName: at.reader, Since: since}, 5*time.Second)
	if err != nil || res.Since == nil {
		return nil, -1, false
	}
	return res.Delta, *res.Since, true
}

// seen tells the daemon a raw-event read (read_timeline) showed the agent the
// room up to upTo.
func (f *frontEnd) seen(upTo int64) {
	at := f.snapshot()
	if !at.attached {
		return
	}
	_, _ = daemon.Send(f.socketPath, daemon.Request{Op: "seen", RoomKey: at.roomKey, ReaderName: at.reader, UpTo: &upTo}, time.Second)
}

// clientFactory wraps the real HTTP client so that Join attaches to the daemon
// instead of joining the room a second time. The instance id the daemon holds
// for this harness is adopted, so contributions from this process carry this
// harness's own presence identity.
func (f *frontEnd) clientFactory(cfg client.Config) session.EdgeClient {
	cfg.AgentLabel = f.seatLabel(cfg)
	return &attachingClient{Client: client.New(cfg, nil), fe: f, cfg: cfg}
}

type attachingClient struct {
	*client.Client
	fe  *frontEnd
	cfg client.Config
}

// Join attaches through the daemon, within the caller's deadline.
func (a *attachingClient) Join(ctx context.Context) (*client.JoinResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := daemon.AttachTimeout
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left < timeout {
			timeout = left
		}
	}
	res, err := a.fe.attach(a.cfg, timeout)
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
	at := d.fe.snapshot()
	res, err := daemon.Send(d.fe.socketPath, daemon.Request{Op: "peek", WorkspaceKey: hooks.WorkspaceKey(d.fe.ws.Dir()), Harness: at.harness}, time.Second)
	if err != nil {
		return nil
	}
	for i := range res.Rooms {
		if res.Rooms[i].RoomKey == at.roomKey {
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
func (d *daemonSession) RoomName() narrate.RoomName     { return d.sess.RoomName() }
func (d *daemonSession) Attention() *client.Attention   { return d.sess.Attention() }
func (d *daemonSession) Divergence() *client.Divergence { return d.sess.Divergence() }

// consume forwards a hook's delivery to the daemon for the terminal reader.
func (d *daemonSession) consume(upTo int64) int64 {
	if !d.fe.ensure() {
		return -1
	}
	at := d.fe.snapshot()
	key := hooks.WorkspaceKey(d.fe.ws.Dir())
	res, err := daemon.Send(d.fe.socketPath, daemon.Request{
		Op: "consume", RoomKey: at.roomKey, WorkspaceKey: key, Harness: at.harness, UpTo: &upTo,
		ReaderName: daemon.TerminalReaderName(key),
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
	if err != nil || !h.fe.snapshot().attached || !h.fe.ensure() {
		return id, redacted, err
	}
	at := h.fe.snapshot()
	res, merr := daemon.Send(h.fe.socketPath, daemon.Request{Op: "match", RoomKey: at.roomKey, ReaderName: at.reader, Text: text}, 2*time.Second)
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

// TakeRefusals implements tools.RefusalReporter by asking the spool, which
// this process's worker writes refusals into.
func (h *holdingAccepter) TakeRefusals(incidentID string) []tools.Refusal {
	return h.inner.TakeRefusals(incidentID)
}

// SessionExpired implements tools.SessionReporter by asking the spool, which
// this process's worker marks when the room answers 401.
func (h *holdingAccepter) SessionExpired(incidentID string) (int, bool) {
	return h.inner.SessionExpired(incidentID)
}

// HeldReason implements tools.HeldReporter.
func (h *holdingAccepter) HeldReason(id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held[id]
}
