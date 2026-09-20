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
	"github.com/landfalls-ai/landfall-cli/internal/realtime"
)

// ProtocolVersion is the daemon socket's `v`. It is 2 because the per-pid hook
// socket is protocol 1 and the two are different sockets with different verbs;
// see contracts/daemon-ipc.md and research R11 for why a version mismatch is
// not the compatibility mechanism here (a second socket is).
const ProtocolVersion = 2

// RequestTimeout bounds one request on the daemon socket. The status line
// polls every 5 s; an answer must be well inside that.
const RequestTimeout = 2 * time.Second

// Request is one line on the daemon socket.
type Request struct {
	Op string `json:"op"`

	// attach
	Room         *client.Config `json:"room,omitempty"`
	Reader       *ReaderSpec    `json:"reader,omitempty"`
	Fingerprints *Fingerprints  `json:"fingerprints,omitempty"`

	// detach, frame, delta, peek, consume, status, share, allow-cwd, held
	RoomKey      string `json:"roomKey,omitempty"`
	ReaderName   string `json:"readerName,omitempty"`
	WorkspaceKey string `json:"workspaceKey,omitempty"`
	UpTo         *int64 `json:"upTo,omitempty"`

	// share
	Text              string         `json:"text,omitempty"`
	Refs              []string       `json:"refs,omitempty"`
	Kind              string         `json:"kind,omitempty"`
	SourceQueryFailed bool           `json:"sourceQueryFailed,omitempty"`
	Widget            map[string]any `json:"widget,omitempty"`
	EntryID           string         `json:"entryId,omitempty"`
}

// ReaderSpec is how a reader introduces itself at attach.
type ReaderSpec struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Host         string `json:"host"`
	WorkspaceKey string `json:"workspaceKey"`
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
}

// RoomView is a room as `rooms`, `peek` and `status` describe it.
type RoomView struct {
	RoomKey      string     `json:"roomKey"`
	IncidentID   string     `json:"incidentId"`
	Slug         string     `json:"slug"`
	Connection   Connection `json:"connection"`
	Readers      []*Reader  `json:"readers,omitempty"`
	Count        int        `json:"count"`
	Held         int        `json:"held"`
	VotesAwaited int        `json:"votesAwaited"`
	MaxSeq       int64      `json:"maxSeq"`
	Cursor       int64      `json:"cursor"`
	Digest       []string   `json:"digest,omitempty"`
	// Events is the untold set itself (peek only), so a front end's per-pid
	// hook socket can answer protocol-1 hooks from the daemon's view.
	Events []client.Event `json:"events,omitempty"`
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
		rd := room.Attach(Reader{Name: req.Reader.Name, Kind: kind, Host: req.Reader.Host, WorkspaceKey: req.Reader.WorkspaceKey})
		// An agent front end runs in the person's terminal: the person becomes a
		// reader of this room at the same moment, at the same position, so what
		// happens from here on is untold to THEM until a hook shows it.
		if kind == KindAgent && req.Reader.WorkspaceKey != "" {
			room.Attach(Reader{Name: TerminalReaderName(req.Reader.WorkspaceKey), Kind: KindTerminal, WorkspaceKey: req.Reader.WorkspaceKey})
		}
		if req.Fingerprints != nil {
			d.setFingerprints(room.Key, req.Fingerprints)
		}
		d.save()
		frame, _ := room.Frame(ctx)
		res := ok()
		res.RoomKey, res.AgentInstanceID, res.Reader, res.Frame, res.Connection = room.Key, room.AgentInstanceID, rd, frame, room.Connection
		return res

	case "detach":
		room := d.room(req.RoomKey)
		if room == nil {
			return fail("no such room")
		}
		room.Detach(req.ReaderName)
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
		delta, err := room.Delta(ctx, req.ReaderName)
		if err != nil {
			return fail("delta unavailable: " + err.Error())
		}
		d.save()
		res := ok()
		res.Delta, res.Connection = delta, room.Connection
		return res

	case "peek":
		return d.peek(req)

	case "consume":
		room := d.room(req.RoomKey)
		if room == nil {
			return fail("no such room")
		}
		if req.UpTo == nil {
			return fail("consume needs upTo")
		}
		rd, okr := room.Reader(req.ReaderName)
		if !okr {
			return fail("no reader " + req.ReaderName)
		}
		// A hook process speaks for the person: it may move the terminal
		// reader only. Any other name is refused (research R3).
		if rd.Kind != KindTerminal {
			return fail(ErrWrongKind.Error())
		}
		cur, err := room.Advance(req.ReaderName, KindTerminal, *req.UpTo)
		if err != nil {
			return fail(err.Error())
		}
		d.save()
		res := ok()
		res.Cursor = &cur
		return res

	case "status":
		return d.status(req.WorkspaceKey)

	case "rooms":
		res := ok()
		for _, room := range d.rooms() {
			res.Rooms = append(res.Rooms, RoomView{
				RoomKey: room.Key, IncidentID: room.Config.IncidentID, Slug: room.Config.Slug,
				Connection: room.Connection, Readers: room.Readers(), MaxSeq: room.MaxSeq(),
			})
		}
		return res

	case "stop":
		d.requestStop()
		return ok()

	case "share", "allow-cwd", "held", "drop-held":
		return d.handleHold(ctx, req)
	}
	return fail("unknown op " + strings.TrimSpace(fmt.Sprint(req.Op)))
}

// peek answers for one workspace's terminal reader across every room that has
// one (a checkout with two rooms reports both), never advancing anything.
func (d *Daemon) peek(req Request) Response {
	res := ok()
	for _, room := range d.rooms() {
		name := req.ReaderName
		if name == "" {
			name = TerminalReaderName(req.WorkspaceKey)
		}
		rd, okr := room.Reader(name)
		if !okr {
			// A workspace whose person has never been attached as a reader is
			// attached now, at the room's current position: the hooks are how
			// the person's terminal announces itself.
			if req.WorkspaceKey == "" {
				continue
			}
			rd = room.Attach(Reader{Name: name, Kind: KindTerminal, WorkspaceKey: req.WorkspaceKey})
		}
		untold := room.UntoldFor(name)
		digest := make([]string, 0, len(untold))
		for _, e := range untold {
			line := narrate.FormatEventLine(e)
			if IsAddressed(e) {
				line += "  ← addressed to a person"
			}
			digest = append(digest, line)
		}
		res.Rooms = append(res.Rooms, RoomView{
			RoomKey: room.Key, IncidentID: room.Config.IncidentID, Slug: room.Config.Slug, Connection: room.Connection,
			Count: len(untold), Held: d.heldCount(room.Key), MaxSeq: room.MaxSeq(), Cursor: rd.Cursor, Digest: digest, Events: untold,
		})
	}
	return res
}

// status is the status line's one call.
func (d *Daemon) status(workspaceKey string) Response {
	res := d.peek(Request{Op: "peek", WorkspaceKey: workspaceKey})
	if !res.OK {
		return res
	}
	res.Line = FormatStatusLine(res.Rooms)
	return res
}

// FormatStatusLine renders the status line for the rooms a workspace reads.
// "" when there is nothing to say (no room open here).
func FormatStatusLine(rooms []RoomView) string {
	if len(rooms) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rooms))
	for _, r := range rooms {
		seg := "🔴 landfall #" + r.IncidentID
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
	res := Response{}
	if uerr := json.Unmarshal(line, &req); uerr != nil {
		res = fail("bad request: " + uerr.Error())
	} else {
		res = d.handler.Handle(ctx, req)
	}
	body, _ := json.Marshal(res)
	_, _ = conn.Write(append(body, '\n'))
}

// Send is the client side: one request, one answer, over the daemon socket.
func Send(socketPath string, req Request, timeout time.Duration) (*Response, error) {
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
		return nil, fmt.Errorf("daemon speaks protocol %d, this binary %d", res.V, ProtocolVersion)
	}
	if !res.OK {
		return &res, errors.New(res.Error)
	}
	return &res, nil
}

// isPlumbingEvent is a tiny seam for tests.
var isPlumbingEvent = realtime.IsPlumbing
