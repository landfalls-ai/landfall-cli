package cli

// readcommon.go — what the edge-component read commands share
// (`incidents`, `timeline`, `wall`, `lb`, `comms`, `brain`; contract
// specs/20261008-150000-edge-components/contracts/cli-json.md §3-§9 as amended
// by its review.md).
//
// Every one of them prints ONE JSON object on one stdout line and exits 0,
// failure included ({"ok":false,"error":"<a sentence a person can act on>"}),
// because the Claude Code mod reads the last line with core.js parseAnswer.
//
// Two sessions, never mixed up:
//
//   - the ROOM's session lives in the room daemon (an edge token bound to one
//     incident). Reads that need it go through the daemon's `read`, `query`
//     and `peek` ops, so the token never leaves the daemon;
//   - the PERSON's session is their `landfall login` token, used for the
//     organization-level routes an edge token is refused on, and for the
//     wall's data, which the server resolves per viewer by the caller's own
//     identity.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/instance"
	"github.com/spf13/cobra"
)

// The sentences the read commands answer with. Plain, short, no dashes.
const (
	msgNoRoom       = "This folder is not in a war room. Open a share link from the room to join it."
	msgRoomExpired  = "The room's session expired. Join the room again with a fresh share link."
	msgLoginExpired = "Your sign-in expired. Run landfall login."
	msgUnreachable  = "Landfall could not be reached. Check your connection and try again."
)

// ReadDeps is every effect the read commands have, injectable for tests.
type ReadDeps struct {
	// WorkspaceKey is this checkout's key: a room is "this folder's" when a
	// reader of this workspace reads it.
	WorkspaceKey string
	// Harness is the agent host this command runs for (--host), so the room
	// this session's own agent is in can be told from another one's.
	Harness string
	// Rooms is the daemon's `rooms` op.
	Rooms func() ([]daemon.RoomView, error)
	// Peek is the daemon's `peek` op for this workspace: the rooms this
	// checkout reads, each with its status at a glance.
	Peek func() ([]daemon.RoomView, error)
	// RoomRead is the daemon's `read` op: one GET of a room route with the
	// room's own session. A refusal is a *client.HTTPError.
	RoomRead func(ctx context.Context, roomKey, path string) (json.RawMessage, error)
	// Query is the daemon's `query` op: one signal read with the room's session.
	Query func(ctx context.Context, roomKey string, q chartQuery) (map[string]any, error)
	// Org is the person's own session, or ("", errNotSignedIn).
	Org func(ctx context.Context) (*client.OrgReader, error)
	// WebBase is the web app's address, for links a person opens.
	WebBase string
	// Now is the clock.
	Now func() time.Time
}

// errNotSignedIn is Org's answer when there is no usable `landfall login`.
var errNotSignedIn = errors.New("not signed in")

// defaultReadDeps is the production wiring for one workspace.
func defaultReadDeps(ws hooks.Workspace) ReadDeps {
	sock := hooks.DaemonSocketPath(ws)
	return ReadDeps{
		WorkspaceKey: hooks.WorkspaceKey(ws.Dir()),
		Harness:      ws.Harness,
		Rooms: func() ([]daemon.RoomView, error) {
			res, err := daemon.Send(sock, daemon.Request{Op: "rooms"}, time.Second)
			var ve *daemon.VersionError
			if errors.As(err, &ve) && res != nil {
				err = nil
			}
			if err != nil {
				return nil, err
			}
			return res.Rooms, nil
		},
		Peek: func() ([]daemon.RoomView, error) {
			res, err := daemon.Send(sock, daemon.Request{Op: "peek", WorkspaceKey: hooks.WorkspaceKey(ws.Dir()), Harness: ws.Harness}, 3*time.Second)
			if err != nil {
				return nil, err
			}
			return res.Rooms, nil
		},
		RoomRead: func(_ context.Context, roomKey, path string) (json.RawMessage, error) {
			res, err := daemon.Send(sock, daemon.Request{Op: "read", RoomKey: roomKey, Path: path}, daemon.ReadTimeout)
			if err != nil {
				if res != nil && res.HTTPStatus != 0 {
					return nil, &client.HTTPError{Path: path, Status: res.HTTPStatus}
				}
				if res != nil && res.Error != "" {
					return nil, errors.New(res.Error)
				}
				return nil, fmt.Errorf("the room daemon did not answer: %w", err)
			}
			return res.Body, nil
		},
		Query: func(_ context.Context, roomKey string, q chartQuery) (map[string]any, error) {
			res, err := daemon.Send(sock, daemon.Request{
				Op: "query", RoomKey: roomKey, Source: q.Source, Operation: q.Operation,
				Params: q.Params, Connection: q.Connection, Account: q.Account,
			}, daemon.QueryTimeout)
			if err != nil {
				if res != nil && res.Error != "" {
					return nil, errors.New(res.Error)
				}
				return nil, fmt.Errorf("the room daemon did not answer: %w", err)
			}
			return res.Signals, nil
		},
		Org: func(ctx context.Context) (*client.OrgReader, error) {
			t := hooks.ResolveTarget(ctx, hooks.TargetDeps{Env: ws.Getenv})
			if !t.OK {
				return nil, errNotSignedIn
			}
			return client.NewOrgReader(t.BaseURL, t.Slug, t.Token, nil), nil
		},
		WebBase: webBase(),
		Now:     time.Now,
	}
}

// webBase is the web app this CLI points at, for links a person opens.
func webBase() string {
	if inst, err := instance.Resolve(instance.Options{}); err == nil && inst.Web != "" {
		return strings.TrimRight(inst.Web, "/")
	}
	return strings.TrimRight(instance.DefaultInstance().Web, "/")
}

func (d ReadDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// pickRoom finds the room a command is about. With sel (`--room`), any room
// open on this machine whose key matches, or whose incident id or display id
// does; without it, the room this checkout is in now (currentRooms first).
func pickRoom(d ReadDeps, sel string) (daemon.RoomView, string) {
	if d.Rooms == nil {
		return daemon.RoomView{}, msgNoRoom
	}
	rooms, err := d.Rooms()
	if err != nil {
		return daemon.RoomView{}, msgNoRoom
	}
	sel = strings.TrimSpace(sel)
	if sel != "" {
		for _, r := range rooms {
			if r.RoomKey == sel {
				return r, ""
			}
		}
		for _, r := range rooms {
			if r.IncidentID == sel || (len(sel) >= 6 && strings.HasPrefix(r.IncidentID, sel)) || (r.DisplayID != "" && r.DisplayID == sel) {
				return r, ""
			}
		}
		return daemon.RoomView{}, "No room " + sel + " is open on this machine. Join it first."
	}
	var mine []daemon.RoomView
	for _, r := range rooms {
		if readsWorkspace(r, d.WorkspaceKey) {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return daemon.RoomView{}, msgNoRoom
	}
	if len(mine) > 1 && d.Peek != nil {
		// The `rooms` op carries no status; the peek does (from the cached
		// frame, never a network wait). Only asked when there is a choice.
		if peeked, err := d.Peek(); err == nil {
			byKey := map[string]daemon.RoomView{}
			for _, p := range peeked {
				byKey[p.RoomKey] = p
			}
			for i, r := range mine {
				if p, ok := byKey[r.RoomKey]; ok {
					mine[i].Status, mine[i].Agent = p.Status, p.Agent
				}
			}
		}
	}
	currentRooms(mine, d.WorkspaceKey, d.Harness)
	return mine[0], ""
}

// currentRooms orders a checkout's rooms so the one it is in NOW comes
// first. A daemon keeps a room after its incident is over (a reader keeps its
// cursor for an hour), so "the first room" was often an older, resolved one.
// In order:
//
//  1. the room this session's own agent is in: a connected agent reader of
//     this checkout and harness (or the peek's agent.inRoom);
//  2. an open incident before a resolved one;
//  3. the newest activity (maxSeq) first.
//
// The sort is stable, so rooms equal on all three keep the daemon's order.
func currentRooms(rooms []daemon.RoomView, workspaceKey, harness string) {
	sort.SliceStable(rooms, func(i, j int) bool {
		ai, aj := agentIsIn(rooms[i], workspaceKey, harness), agentIsIn(rooms[j], workspaceKey, harness)
		if ai != aj {
			return ai
		}
		oi, oj := !roomIsOver(rooms[i]), !roomIsOver(rooms[j])
		if oi != oj {
			return oi
		}
		return rooms[i].MaxSeq > rooms[j].MaxSeq
	})
}

// agentIsIn reports whether this checkout's agent (of this harness, when one
// is named) is in the room.
func agentIsIn(r daemon.RoomView, workspaceKey, harness string) bool {
	if r.Agent != nil && r.Agent.InRoom {
		return true
	}
	if workspaceKey == "" {
		return false
	}
	for _, rd := range r.Readers {
		if rd == nil || rd.Kind != daemon.KindAgent || !rd.Connected || rd.WorkspaceKey != workspaceKey {
			continue
		}
		if harness != "" && rd.Harness != "" && rd.Harness != harness {
			continue
		}
		return true
	}
	return false
}

// roomIsOver reports a room whose incident is resolved, closed or in
// postmortem, as far as the daemon knows.
func roomIsOver(r daemon.RoomView) bool {
	return r.Status != nil && closedStatus(r.Status.Status)
}

// readsWorkspace reports whether a reader of this checkout reads the room.
func readsWorkspace(r daemon.RoomView, key string) bool {
	if key == "" {
		return false
	}
	for _, rd := range r.Readers {
		if rd.WorkspaceKey == key {
			return true
		}
	}
	return false
}

// roomRefusal is the sentence for a read the room's session could not make.
func roomRefusal(err error, what string) string {
	var he *client.HTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusUnauthorized:
			return msgRoomExpired
		case http.StatusForbidden:
			return "Landfall refused to show " + what + " to this room's session."
		case http.StatusNotFound:
			return "Landfall has no " + what + " for this room."
		case http.StatusTooManyRequests:
			return "Landfall is busy. Try again in a few seconds."
		}
		return fmt.Sprintf("Landfall could not read %s (HTTP %d).", what, he.Status)
	}
	if err != nil && strings.Contains(err.Error(), "did not answer") {
		return msgNoRoom
	}
	return "Landfall could not read " + what + ". Try again shortly."
}

// personRefusal is the sentence for a read the person's own session could
// not make.
func personRefusal(err error, what string) string {
	var he *client.HTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusUnauthorized:
			return msgLoginExpired
		case http.StatusForbidden:
			return "Landfall refused to show you " + what + ". Your sign-in may be for another organization."
		case http.StatusNotFound:
			return "Landfall has no " + what + " here."
		case http.StatusTooManyRequests:
			return "Landfall is busy. Try again in a few seconds."
		}
		return fmt.Sprintf("Landfall could not read %s (HTTP %d).", what, he.Status)
	}
	return msgUnreachable
}

// failure is the contract's {"ok":false,"error":...}.
func failure(msg string) map[string]any {
	return map[string]any{"ok": false, "error": msg}
}

// printAnswer writes the one JSON line. Every read command exits 0, failure
// included: the answer itself says what happened.
func printAnswer(out io.Writer, v map[string]any) error {
	if out == nil {
		out = stdout
	}
	body, err := json.Marshal(v)
	if err != nil {
		body = []byte(`{"ok":false,"error":"The answer could not be written."}`)
	}
	_, _ = fmt.Fprintln(out, string(body))
	return nil
}

// newReadCommand is one read command: --host and --room taken and tolerated,
// unknown flags ignored rather than turned into a usage error, so the mod
// always gets a JSON line back.
func newReadCommand(ui *UI, use string, run func(cmd *cobra.Command, ws hooks.Workspace, room string) map[string]any) *cobra.Command {
	var host, room string
	c := newCommand(ui, use, func(cmd *cobra.Command, _ []string) error {
		ws := hooks.Workspace{Harness: hooks.DetectHookHarness(host, "", os.Getenv)}
		return printAnswer(stdout, run(cmd, ws, room))
	})
	c.Flags().StringVar(&host, "host", "", "the agent host this session runs in")
	c.Flags().StringVar(&room, "room", "", "the room, when this checkout reads more than one")
	c.FParseErrWhitelist.UnknownFlags = true
	c.Hidden = true
	return c
}

// readCommands are the edge-component read commands, registered from root.go.
func readCommands(ui *UI) []*cobra.Command {
	return []*cobra.Command{
		newIncidentsCommand(ui),
		newTimelineCommand(ui),
		newWallCommand(ui),
		newLBCommand(ui),
		newCommsCommand(ui),
		newBrainCommand(ui),
	}
}

// --- small JSON readers -----------------------------------------------------

func jStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func jNum(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func jObj(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	o, _ := m[key].(map[string]any)
	return o
}

func jList(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	l, _ := m[key].([]any)
	return l
}

// oneLineText trims s to one line of at most n runes.
func oneLineText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if n > 0 && len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

// parseTime reads an RFC 3339 time, or zero.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
