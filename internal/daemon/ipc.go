package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

// ProtocolVersion is the daemon socket's `v`. It started at 2 because the
// per-pid hook socket is protocol 1 and the two are different sockets with
// different verbs (contracts/daemon-ipc.md, research R11).
//
// 3 (2026-09-28) is the daemon that gives each harness its own agent session.
// A front end of this build that finds a protocol-2 daemon asks it to hand
// over (EnsureRunning) rather than attach to a daemon that would fold every
// harness back into one session until it idles out. A request carries its
// own `v`; one that carries none is a protocol-2 client (an older `serve`,
// the Edge panel, a hook), and is answered as protocol 2, whose shapes are a
// subset of these, so an older front end keeps working against this daemon.
const ProtocolVersion = 3

// LegacyProtocolVersion is what a request with no `v` is answered with.
const LegacyProtocolVersion = 2

// VersionError is Send's answer when the daemon speaks another protocol. The
// response is returned alongside it: the request was delivered and answered.
type VersionError struct {
	Theirs, Ours int
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("daemon speaks protocol %d, this binary %d", e.Theirs, e.Ours)
}

// JoinTimeout bounds one /edge/join the daemon makes (a room's first seat or
// a later harness's); FrameAttachBudget the frame read an attach makes; and
// LinkWait how long a second harness waits for a sibling that is redeeming
// the same share link.
const (
	JoinTimeout       = 10 * time.Second
	FrameAttachBudget = 3 * time.Second
	LinkWait          = 12 * time.Second
)

// RequestTimeout bounds one request on the daemon socket. The status line
// polls every 5 s; an answer must be well inside that.
const RequestTimeout = 2 * time.Second

// AttachTimeout bounds an attach, which may perform a /edge/join.
const AttachTimeout = 15 * time.Second

// QueryTimeout bounds a query: one signal read through the room's session.
const QueryTimeout = 30 * time.Second

// Request is one line on the daemon socket.
type Request struct {
	Op string `json:"op"`
	// V is the protocol the caller speaks; absent is LegacyProtocolVersion.
	V int `json:"v,omitempty"`

	// attach
	Room         *client.Config `json:"room,omitempty"`
	Reader       *ReaderSpec    `json:"reader,omitempty"`
	Fingerprints *Fingerprints  `json:"fingerprints,omitempty"`

	// detach, frame, delta, peek, consume, status, share, allow-cwd, held
	RoomKey      string `json:"roomKey,omitempty"`
	ReaderName   string `json:"readerName,omitempty"`
	WorkspaceKey string `json:"workspaceKey,omitempty"`
	UpTo         *int64 `json:"upTo,omitempty"`
	// Harness (peek, consume, status) is the asking hook's agent host: the
	// terminal reader it speaks for is the one for this workspace AND harness.
	Harness string `json:"harness,omitempty"`
	// Since (delta) is an explicit sinceSeq from get_updates; absent means the
	// reader's own cursor.
	Since *int64 `json:"since,omitempty"`
	// Link (attach, link, link-redeemed, link-failed) is LinkHash of a share
	// link: attach records it on the room; `link` looks it up or claims the
	// right to redeem it; `link-redeemed` records what the redeem gave (Room);
	// `link-failed` releases the claim.
	Link string `json:"link,omitempty"`

	// query (the Claude Code mod's chart key, through `landfall chart`): one
	// signal read with the room's own session, as query_signals makes it.
	Source     string         `json:"source,omitempty"`
	Operation  string         `json:"operation,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
	Connection string         `json:"connection,omitempty"`
	Account    string         `json:"account,omitempty"`

	// share
	Text              string         `json:"text,omitempty"`
	Refs              []string       `json:"refs,omitempty"`
	Kind              string         `json:"kind,omitempty"`
	SourceQueryFailed bool           `json:"sourceQueryFailed,omitempty"`
	Widget            map[string]any `json:"widget,omitempty"`
	EntryID           string         `json:"entryId,omitempty"`

	// read (read.go): a room route, relative to the incident, with its query.
	Path string `json:"path,omitempty"`
}

// ReaderSpec is how a reader introduces itself at attach.
type ReaderSpec struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Host         string `json:"host"`
	WorkspaceKey string `json:"workspaceKey"`
	// Workspace is the directory itself, for the doorbell the daemon writes
	// there (hooks watch a file under the workspace, not a key).
	Workspace string `json:"workspace,omitempty"`
	// Harness is the agent host's key (hooks.HarnessFromClientName). The
	// attach's own room config carries the seat label (AgentLabel).
	Harness string `json:"harness,omitempty"`
}

// Fingerprints is what the front end computed from its working directory
// (Phase US3 fills them; the verb accepts them from day one so the wire does
// not change).
type Fingerprints struct {
	Paths   []string `json:"paths,omitempty"`
	Commits []string `json:"commits,omitempty"`
	Names   []string `json:"names,omitempty"`
}

// Response is one line back. Every response has OK and V; the rest depends on
// the verb. Errors are one readable sentence.
type Response struct {
	OK    bool   `json:"ok"`
	V     int    `json:"v"`
	Error string `json:"error,omitempty"`

	RoomKey         string               `json:"roomKey,omitempty"`
	AgentInstanceID string               `json:"agentInstanceId,omitempty"`
	Reader          *Reader              `json:"reader,omitempty"`
	Frame           *client.ContextFrame `json:"frame,omitempty"`
	Delta           *client.FrameDelta   `json:"delta,omitempty"`
	Connection      Connection           `json:"connection,omitempty"`
	Cursor          *int64               `json:"cursor,omitempty"`
	Rooms           []RoomView           `json:"rooms,omitempty"`
	Line            string               `json:"line,omitempty"`
	Released        int                  `json:"released,omitempty"`
	Held            bool                 `json:"held,omitempty"`
	Matched         []string             `json:"matched,omitempty"`
	EntryID         string               `json:"entryId,omitempty"`
	Redacted        bool                 `json:"redacted,omitempty"`
	// Since (delta) is the seq the delta was read from.
	Since *int64 `json:"since,omitempty"`
	// Room (link) is the config a share link already gave this machine, with
	// no seat label: the asking front end adds its own.
	Room *client.Config `json:"room,omitempty"`
	// Claimed (link) says the caller holds the claim on a link nobody has
	// redeemed yet: it redeems, then reports link-redeemed or link-failed.
	Claimed bool `json:"claimed,omitempty"`
	// Allowed (match): the person has allowed working-directory content for the room.
	Allowed bool `json:"allowed,omitempty"`
	// Signals (query) is the read's result, as query_signals receives it.
	Signals map[string]any `json:"signals,omitempty"`
	// Body (read) is the route's answer, verbatim; HTTPStatus is the
	// server's status when it refused the read.
	Body       json.RawMessage `json:"body,omitempty"`
	HTTPStatus int             `json:"httpStatus,omitempty"`
}

// RoomView is a room as `rooms`, `peek` and `status` describe it.
type RoomView struct {
	RoomKey    string `json:"roomKey"`
	IncidentID string `json:"incidentId"`
	// DisplayID and Title are what the person knows the room as: the status
	// line shows these, never IncidentID.
	DisplayID  string     `json:"displayId,omitempty"`
	Title      string     `json:"title,omitempty"`
	Slug       string     `json:"slug"`
	Connection Connection `json:"connection"`
	Readers    []*Reader  `json:"readers,omitempty"`
	// Seats (rooms only) are this machine's agent sessions in the room, one
	// per harness.
	Seats        []SeatView `json:"seats,omitempty"`
	Count        int        `json:"count"`
	Held         int        `json:"held"`
	VotesAwaited int        `json:"votesAwaited"`
	MaxSeq       int64      `json:"maxSeq"`
	Cursor       int64      `json:"cursor"`
	Digest       []string   `json:"digest,omitempty"`
	// Attention (peek only) is what the room wants from this machine's agent:
	// the Stop hook's second reason to refuse, read here so a hook needs no
	// front end alive to learn it (contracts/daemon-ipc.md).
	Attention *client.Attention `json:"attention,omitempty"`
	// Events is the untold set itself (peek only), so a front end's per-pid
	// hook socket can answer protocol-1 hooks from the daemon's view.
	Events []client.Event `json:"events,omitempty"`
	// Status (peek only) is the room at a glance: status, severity, people,
	// Beacon, the leading theory. From the cached frame; never a network wait.
	Status *narrate.RoomStatus `json:"status,omitempty"`
	// Votes (peek only) is what awaits the PERSON's position: the human view
	// of the attention projection, read with no agent instance (review
	// finding 2). VotesAwaited above stays the agent's count.
	Votes []narrate.Vote `json:"votes,omitempty"`
}

// SeatView is one agent session as `rooms` lists it.
type SeatView struct {
	Label      string `json:"label"`
	InstanceID string `json:"instanceId"`
}

// Handler answers requests; it is the daemon's brain and is pure enough to
// test without a socket.
type Handler struct {
	d *Daemon
}

func fail(msg string) Response { return Response{OK: false, V: ProtocolVersion, Error: msg} }
func ok() Response             { return Response{OK: true, V: ProtocolVersion} }

// Handle answers one request. `callerKind` is bound to the connection at
// attach; a hook process or a person command connects fresh each time with
// kind implied by its verb (peek/consume/status: terminal).
func (h *Handler) Handle(ctx context.Context, req Request) Response {
	d := h.d
	switch req.Op {
	case "attach":
		if req.Room == nil || req.Reader == nil {
			return fail("attach needs room and reader")
		}
		kind, err := ParseKind(req.Reader.Kind)
		if err != nil {
			return fail(err.Error())
		}
		room, err := d.openOrAttach(ctx, *req.Room)
		if err != nil {
			return fail("could not join the room: " + err.Error())
		}
		// One agent session per harness: the first attach under a new label
		// performs its own /edge/join, so Claude Code and Codex in the same
		// checkout are two participants in the room, not one.
		seat, err := room.EnsureSeat(ctx, *req.Room)
		if err != nil {
			return fail("could not join the room as " + SeatLabel(*req.Room) + ": " + err.Error())
		}
		room.AddLink(req.Link)
		// The frame first: a new reader starts at the room's current position
		// (the frame's as-of seq), not at -1. At -1 its first delta would replay
		// the whole incident and its untold set would count history as news.
		fctx, fcancel := context.WithTimeout(ctx, FrameAttachBudget)
		frame, _ := room.Frame(fctx)
		fcancel()
		harness := req.Reader.Harness
		rd := room.Attach(Reader{Name: req.Reader.Name, Kind: kind, Host: req.Reader.Host, WorkspaceKey: req.Reader.WorkspaceKey, Workspace: req.Reader.Workspace, Harness: harness, Seat: seat.Label})
		// An agent front end runs in the person's terminal: the person becomes a
		// reader of this room at the same moment, at the same position, so what
		// happens from here on is untold to THEM until a hook shows it. One such
		// reader per workspace AND harness: a Codex hook marking the room read
		// must not mark it read for the person's Claude Code session.
		if kind == KindAgent && req.Reader.WorkspaceKey != "" {
			room.Attach(Reader{Name: TerminalReaderNameFor(req.Reader.WorkspaceKey, harness), Kind: KindTerminal, WorkspaceKey: req.Reader.WorkspaceKey, Workspace: req.Reader.Workspace, Harness: harness, Seat: seat.Label})
		}
		if req.Fingerprints != nil {
			d.setFingerprints(room.Key+"|"+req.Reader.WorkspaceKey, req.Fingerprints)
		}
		d.opts.Log(fmt.Sprintf("attached %s (%s) to %s at cursor %d; %d reader(s) connected", rd.Name, rd.Kind, room.Config.IncidentID, rd.Cursor, room.ConnectedReaders()))
		d.save()
		res := ok()
		res.RoomKey, res.AgentInstanceID, res.Reader, res.Frame, res.Connection = room.Key, seat.InstanceID, rd, frame, room.Connection
		return res

	case "link":
		return d.lookupOrClaimLink(ctx, req.Link)

	case "link-redeemed":
		if req.Room == nil || req.Link == "" {
			return fail("link-redeemed needs the link and the room")
		}
		d.links.fulfil(req.Link, *req.Room)
		return ok()

	case "link-failed":
		d.links.release(req.Link)
		return ok()

	case "seen":
		// An agent reader was shown the room up to a point by a read of its
		// own (get_brief, join_war_room, read_timeline): its cursor moves
		// there, never backwards and never anyone else's.
		room := d.room(req.RoomKey)
		if room == nil {
			return fail("no such room")
		}
		if req.UpTo == nil {
			return fail("seen needs upTo")
		}
		rd, okr := room.Reader(req.ReaderName)
		if !okr || rd.Kind != KindAgent {
			return fail("seen is an agent reader's own")
		}
		cur, err := room.Advance(req.ReaderName, KindAgent, *req.UpTo)
		if err != nil {
			return fail(err.Error())
		}
		d.save()
		res := ok()
		res.Cursor = &cur
		return res

	case "detach":
		room := d.room(req.RoomKey)
		if room == nil {
			return fail("no such room")
		}
		room.Detach(req.ReaderName)
		d.opts.Log(fmt.Sprintf("detached %s from %s; %d reader(s) connected", req.ReaderName, room.Config.IncidentID, room.ConnectedReaders()))
		d.save()
		return ok()

	case "frame":
		room := d.room(req.RoomKey)
		if room == nil {
			return fail("no such room")
		}
		frame, err := room.Frame(ctx)
		if err != nil {
			return fail("brief unavailable: " + err.Error())
		}
		res := ok()
		res.Frame, res.Connection = frame, room.Connection
		return res

	case "delta":
		room := d.room(req.RoomKey)
		if room == nil {
			return fail("no such room")
		}
		rd, okr := room.Reader(req.ReaderName)
		if !okr || rd.Kind != KindAgent {
			return fail("delta is an agent reader's own pull")
		}
		delta, since, err := room.Delta(ctx, req.ReaderName, req.Since)
		if err != nil {
			return fail("delta unavailable: " + err.Error())
		}
		d.save()
		res := ok()
		res.Delta, res.Connection, res.Since = delta, room.Connection, &since
		return res

	case "peek":
		return d.peek(req)

	case "query":
		room := d.room(req.RoomKey)
		if room == nil {
			return fail("no such room")
		}
		if strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.Operation) == "" {
			return fail("query needs a source and an operation")
		}
		cl, instance := room.ClientAndInstance()
		if cl == nil {
			return fail("the room is not connected yet")
		}
		result, err := cl.QuerySignals(ctx, req.Source, req.Operation, req.Params, req.Connection, req.Account)
		if err != nil {
			return fail("the signal read was refused or unavailable: " + err.Error())
		}
		res := ok()
		res.RoomKey, res.Signals, res.AgentInstanceID = room.Key, result, instance
		return res

	case "consume":
		room := d.room(req.RoomKey)
		if room == nil {
			return fail("no such room")
		}
		if req.UpTo == nil {
			return fail("consume needs upTo")
		}
		// A hook process speaks for the person: it may move the terminal
		// reader(s) its peek answered for, and nothing else (research R3).
		names := d.terminalReadersFor(room, req)
		if len(names) == 0 {
			if req.ReaderName != "" {
				if rd, okr := room.Reader(req.ReaderName); okr && rd.Kind != KindTerminal {
					return fail(ErrWrongKind.Error())
				}
				return fail("no reader " + req.ReaderName)
			}
			return fail("no terminal reader for this workspace")
		}
		cur := int64(-1)
		for i, name := range names {
			c, err := room.Advance(name, KindTerminal, *req.UpTo)
			if err != nil {
				return fail(err.Error())
			}
			if i == 0 || c < cur {
				cur = c
			}
		}
		d.save()
		res := ok()
		res.Cursor = &cur
		return res

	case "status":
		return d.status(req.WorkspaceKey, req.Harness)

	case "rooms":
		res := ok()
		for _, room := range d.rooms() {
			var seats []SeatView
			for _, s := range room.Seats() {
				seats = append(seats, SeatView{Label: s.Label, InstanceID: s.InstanceID})
			}
			name := room.Name()
			res.Rooms = append(res.Rooms, RoomView{
				RoomKey: room.Key, IncidentID: room.Config.IncidentID, DisplayID: name.DisplayID, Title: name.Title,
				Slug: room.Config.Slug, Connection: room.Connection, Readers: room.Readers(), Seats: seats, MaxSeq: room.MaxSeq(),
			})
		}
		return res

	case "session":
		// The room's own session, for a command that acts as the person when
		// that session IS the person's (`landfall vote`, `landfall lines`): the
		// same config `link` hands a sibling harness, with no seat label.
		room := d.room(req.RoomKey)
		if room == nil {
			return fail("no such room")
		}
		cfg := room.SessionConfig()
		res := ok()
		res.RoomKey, res.Room = room.Key, &cfg
		return res

	case "stop":
		d.opts.Log("stop requested; rooms are kept for the next daemon")
		d.requestStop()
		return ok()

	case "match":
		return d.match(req)

	case "read":
		return d.read(ctx, req)

	case "allow-cwd":
		return d.allowCwd(req)
	}
	return fail("unknown op " + strings.TrimSpace(fmt.Sprint(req.Op)))
}

// peek answers for one workspace's terminal reader across every room that has
// one (a checkout with two rooms reports both), never advancing anything.
// peekAttentionBudget bounds the attention read a peek makes: within a hook's
// own patience (hooks.SocketTimeout is 250 ms per socket, the daemon call is
// one of them), so a slow server degrades to the last snapshot, never to a
// hook that hangs.
const peekAttentionBudget = 900 * time.Millisecond

func (d *Daemon) peek(req Request) Response {
	res := ok()
	for _, room := range d.rooms() {
		// Only rooms this workspace actually reads. The terminal reader is
		// created when an agent from that workspace attaches (Handle "attach"),
		// never here: a hook or status line asking about a checkout must not
		// enrol that checkout into every room the machine has open.
		names := d.terminalReadersFor(room, req)
		if len(names) == 0 {
			continue
		}
		untold, cursor, seat := room.untoldForAll(names)
		digest := make([]string, 0, len(untold))
		for _, e := range untold {
			line := narrate.FormatEventLine(e)
			if IsAddressed(e) {
				line += "  ← addressed to a person"
			}
			digest = append(digest, line)
		}
		name := room.Name()
		attention := room.AttentionFor(context.Background(), seat, peekAttentionBudget)
		// VotesAwaited is read off the same attention snapshot the Stop hook
		// reads. It was left unset here, so in daemon mode the status line
		// never said a vote was waiting, while the per-pid socket status did.
		votes := 0
		if attention != nil {
			votes = len(attention.VotesAwaited)
		}
		res.Rooms = append(res.Rooms, RoomView{
			RoomKey: room.Key, IncidentID: room.Config.IncidentID, DisplayID: name.DisplayID, Title: name.Title,
			Slug: room.Config.Slug, Connection: room.Connection,
			Count: len(untold), MaxSeq: room.MaxSeq(), Cursor: cursor, Digest: digest, Events: untold,
			Attention: attention, VotesAwaited: votes,
		})
		st := room.StatusView()
		res.Rooms[len(res.Rooms)-1].Status = &st
		res.Rooms[len(res.Rooms)-1].Votes = room.VotesView()
	}
	return res
}

// terminalReadersFor is which terminal reader(s) of a room a hook's request
// speaks for:
//
//   - a reader named outright, if the room has it (a hook of an older build
//     names the harness-less reader; one that no longer exists falls through
//     to the workspace rule below);
//   - with a harness: that harness's reader in this workspace, else the
//     workspace's harness-less reader (a front end that could not name its
//     host), else every terminal reader of the workspace together, logged
//     once: a hook whose harness key does not match any front end here (an
//     inherited environment, a host renamed between builds) must not go
//     silent, and over-reporting is the recoverable direction;
//   - with no harness: the harness-less reader, else every terminal reader of
//     the workspace together.
func (d *Daemon) terminalReadersFor(room *Room, req Request) []string {
	// A hook names the harness-less reader alongside its harness so a daemon
	// of an older build still understands it; here the harness decides.
	if req.Harness != "" && req.WorkspaceKey != "" && req.ReaderName == TerminalReaderName(req.WorkspaceKey) {
		req.ReaderName = ""
	}
	if req.ReaderName != "" {
		if rd, ok := room.Reader(req.ReaderName); ok {
			if rd.Kind != KindTerminal {
				return nil
			}
			return []string{req.ReaderName}
		}
		if req.WorkspaceKey == "" || req.ReaderName != TerminalReaderName(req.WorkspaceKey) {
			return nil
		}
	}
	if req.WorkspaceKey == "" {
		return nil
	}
	legacy := TerminalReaderName(req.WorkspaceKey)
	if req.Harness != "" {
		if _, ok := room.Reader(TerminalReaderNameFor(req.WorkspaceKey, req.Harness)); ok {
			return []string{TerminalReaderNameFor(req.WorkspaceKey, req.Harness)}
		}
	}
	if _, ok := room.Reader(legacy); ok {
		return []string{legacy}
	}
	var names []string
	for _, rd := range room.Readers() {
		if rd.Kind == KindTerminal && rd.WorkspaceKey == req.WorkspaceKey {
			names = append(names, rd.Name)
		}
	}
	if req.Harness != "" && len(names) > 0 {
		d.logOnce("harness-fallback|"+room.Key+"|"+req.WorkspaceKey+"|"+req.Harness, fmt.Sprintf(
			"a %q hook asked about %s, where no %q session reads the room; answering for every session in that checkout (%s)",
			req.Harness, room.Config.IncidentID, req.Harness, strings.Join(names, ", ")))
	}
	return names
}

// status is the status line's one call.
func (d *Daemon) status(workspaceKey, harness string) Response {
	res := d.peek(Request{Op: "peek", WorkspaceKey: workspaceKey, Harness: harness})
	if !res.OK {
		return res
	}
	res.Line = FormatStatusLine(res.Rooms)
	return res
}

// FormatStatusLine renders the status line for the rooms a workspace reads.
// "" when there is nothing to say (no room open here). A room is named by its
// display id or title (narrate.RoomName), never by its UUID.
func FormatStatusLine(rooms []RoomView) string {
	if len(rooms) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rooms))
	for _, r := range rooms {
		seg := narrate.RoomName{DisplayID: r.DisplayID, Title: r.Title}.StatusHead()
		if r.Count > 0 {
			seg += fmt.Sprintf(" · %d new", r.Count)
		}
		if r.Held > 0 {
			seg += fmt.Sprintf(" · %d held", r.Held)
		}
		if r.VotesAwaited == 1 {
			seg += " · 1 vote awaited"
		} else if r.VotesAwaited > 1 {
			seg += fmt.Sprintf(" · %d votes awaited", r.VotesAwaited)
		}
		if r.Connection != Live {
			seg += " · " + string(r.Connection)
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, "  ")
}

// --- the wire ---------------------------------------------------------------

// serveConn answers one request per connection, one JSON line each way — the
// hook socket's framing (internal/hooks/socket.go serveConn), unchanged.
func (d *Daemon) serveConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(RequestTimeout))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req Request
	var res Response
	if uerr := json.Unmarshal(line, &req); uerr != nil {
		res = fail("bad request: " + uerr.Error())
	} else if req.Op == "subscribe" {
		d.subscribe(ctx, conn, req)
		return
	} else {
		// An attach may join the room over the network (once per harness),
		// and a link lookup may wait for a sibling's redeem; the two-second
		// budget is for the status line's reads, not those.
		switch req.Op {
		case "attach":
			_ = conn.SetDeadline(time.Now().Add(AttachTimeout))
		case "query":
			// A signal read goes to the room's source through the credential
			// proxy; the two-second budget is for the status line's reads.
			_ = conn.SetDeadline(time.Now().Add(QueryTimeout))
		case "read":
			_ = conn.SetDeadline(time.Now().Add(ReadTimeout))
		case "link":
			_ = conn.SetDeadline(time.Now().Add(LinkWait + 2*time.Second))
		}
		res = d.handler.Handle(ctx, req)
	}
	res.V = answerVersion(req)
	body, _ := json.Marshal(res)
	_, _ = conn.Write(append(body, '\n'))
}

// subscribe is the one long-lived connection (research R10): substantive
// events of a room stream to the caller as they arrive, one JSON line each.
// The reader's cursor is NOT moved by this stream; the panel or push host
// says what it has shown with consume/delta, as every other reader does.
func (d *Daemon) subscribe(ctx context.Context, conn net.Conn, req Request) {
	room := d.room(req.RoomKey)
	if room == nil {
		body, _ := json.Marshal(fail("no such room"))
		_, _ = conn.Write(append(body, '\n'))
		return
	}
	_ = conn.SetDeadline(time.Time{})
	ch, unsubscribe := room.Subscribe()
	defer unsubscribe()
	ack := ok()
	ack.V = answerVersion(req)
	ack.RoomKey, ack.Connection = room.Key, room.Connection
	body, _ := json.Marshal(ack)
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return
	}
	// A closed peer is noticed on the next write; a read in the background
	// notices it sooner and ends the loop.
	gone := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(conn).ReadByte()
		close(gone)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gone:
			return
		case evt := <-ch:
			line, err := json.Marshal(map[string]any{"seq": evt.SeqOr(-1), "event": evt})
			if err != nil {
				continue
			}
			if _, err := conn.Write(append(line, '\n')); err != nil {
				return
			}
		}
	}
}

// answerVersion is the protocol a request is answered in: its own, or 2 for a
// request that names none.
func answerVersion(req Request) int {
	if req.V == 0 {
		return LegacyProtocolVersion
	}
	return ProtocolVersion
}

// Send is the client side: one request, one answer, over the daemon socket.
// A daemon that answers in another protocol returns its response AND a
// *VersionError: the request was delivered, the caller decides what that means.
func Send(socketPath string, req Request, timeout time.Duration) (*Response, error) {
	req.V = ProtocolVersion
	if timeout <= 0 {
		timeout = RequestTimeout
	}
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	var res Response
	if err := json.Unmarshal(line, &res); err != nil {
		return nil, err
	}
	if res.V != ProtocolVersion {
		return &res, &VersionError{Theirs: res.V, Ours: ProtocolVersion}
	}
	if !res.OK {
		return &res, errors.New(res.Error)
	}
	return &res, nil
}
