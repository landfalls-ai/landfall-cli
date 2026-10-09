package cli

// adopt.go — the dual connection (specs/20261008-150000-edge-components/
// live.md, FR-L5): when the person joins a room from the mod (`/incidents`,
// `landfall join --incident`), the agent's own `landfall serve` in the same
// folder ends up in that room too, with no link pasted and no turn taken.
//
// HOW. `landfall join --incident` mints a share link with the person's
// sign-in, redeems it ONCE through the daemon's link book, and attaches this
// folder's terminal reader. The daemon marks that reader PersonJoined, which
// puts the room on offer to an agent of the same workspace and harness
// (internal/daemon/live.go). A `serve` in daemon mode that is in no room:
//
//  1. holds one `await-room` connection to the daemon from its MCP handshake
//     on (the harness is known only then), and is answered the moment the
//     room is on offer, or at once if it already is. It then joins through
//     the daemon exactly as a link join does after its redeem: the session's
//     client factory attaches an AGENT reader for this process, which
//     re-uses the seat the daemon already holds for this harness (EnsureSeat),
//     so there is no second /edge/join and no second redeem. The offer names
//     that seat's label, and the attach uses it even when the person's agent
//     has a custom LANDFALL_AGENT_LABEL: seats are keyed by label, so any
//     other label would be a second seat, and the first (terminal-only) seat
//     would be reaped 60 s later and announced to the room as the person
//     leaving, while the console's reads still named it;
//  2. asks once more (`adoptable`) before any tool call while it is still in
//     no room, in case the connection above was down (a daemon restart).
//     Asking costs one local socket round trip and only happens while the
//     session is unjoined.
//
// WHY THIS IS SAFE WITHOUT A TURN. Adopting writes nothing to the MCP wire and
// moves no cursor: the agent reader starts at the room's current position, as
// every new reader does, and the terminal reader's news is never consumed by
// it (each reader keeps its own cursor). Nothing reaches the model until it
// makes a tool call of its own, which then simply works (get_brief,
// get_updates, share_with_room). It also keeps the room open: a room with only
// a terminal reader is left when the daemon next idles (60 s), so the agent
// has to arrive on its own, not at the person's next question.
//
// WHAT IT NEVER DOES. It never replaces a room the session is already in
// (Session.AdoptRoom checks under the session's lock, and an adoption and a
// join_war_room are serialized here). It never adopts while serve's own
// startup join (--link, environment) is pending: that join is the room. It
// never adopts a room the person did not put this folder in from the mod: a
// terminal reader created beside an agent's link join is not on offer. When
// there is nothing to adopt, a tool call fails exactly as before ("not
// connected — call join_war_room …").

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/mcp"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// adoptAskTimeout bounds the `adoptable` question a tool call asks.
const adoptAskTimeout = time.Second

// adoptRetryMin and adoptRetryMax bound the wait before holding `await-room`
// again after it ended with no room (a daemon that restarted or stopped).
const (
	adoptRetryMin = 2 * time.Second
	adoptRetryMax = 10 * time.Second
)

// adopter is serve's half of the dual connection, in daemon mode only.
type adopter struct {
	fe   *frontEnd
	sess *session.Session
	gate *joinGate
	log  func(string)
	// noWatch leaves adoption to tool calls (tests of that path).
	noWatch bool

	// mu serializes an adoption with join_war_room, so the two can never
	// attach this process to two rooms at once.
	mu        sync.Mutex
	watchOnce sync.Once
}

// pending reports that serve's own startup join owns the room question.
func (a *adopter) pending() bool {
	return a.gate != nil && a.gate.busy()
}

// ask is `adoptable` for this workspace and harness: the room on offer, or
// nil.
func (a *adopter) ask() *client.Config {
	res, err := daemon.Send(a.fe.socketPath, daemon.Request{
		Op: "adoptable", WorkspaceKey: hooks.WorkspaceKey(a.fe.ws.Dir()), Harness: a.fe.snapshot().harness,
	}, adoptAskTimeout)
	if err != nil || res == nil || res.Room == nil {
		return nil
	}
	cfg := *res.Room
	return &cfg
}

// adopt joins cfg's room if the session is still in none. Caller holds mu.
func (a *adopter) adoptLocked(ctx context.Context, cfg client.Config) bool {
	if a.sess.Client() != nil || a.pending() {
		return false
	}
	// Join under the seat the person's terminal join holds (cfg.AgentLabel on
	// an offer), never the agent's own label: see frontEnd.seatLabel.
	defer a.fe.pinSeat(cfg.AgentLabel)()
	jctx, cancel := context.WithTimeout(ctx, daemon.AttachTimeout)
	defer cancel()
	joined, err := a.sess.AdoptRoom(jctx, cfg)
	if err != nil {
		a.log("could not join the room this folder was put in (incident " + cfg.IncidentID + "): " + err.Error())
		return false
	}
	if joined {
		a.log("joined incident " + cfg.IncidentID + ": the person put this folder in the room from the mod; no link was needed.")
	}
	return joined
}

// tryAdopt is a tool call's question: when the session is in no room, adopt
// the room on offer, if any.
func (a *adopter) tryAdopt(ctx context.Context) {
	if a == nil || a.sess.Client() != nil || a.pending() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sess.Client() != nil {
		return
	}
	if cfg := a.ask(); cfg != nil {
		a.adoptLocked(ctx, *cfg)
	}
}

// startWatch begins the `await-room` loop once (MCP may initialize twice).
// A serve with a startup join of its own never needs it.
func (a *adopter) startWatch(ctx context.Context) {
	if a == nil || a.gate != nil || a.noWatch {
		return
	}
	a.watchOnce.Do(func() { go a.watch(ctx) })
}

// watch holds `await-room` while the session is in no room, and adopts what
// it is answered with.
func (a *adopter) watch(ctx context.Context) {
	wait := adoptRetryMin
	for ctx.Err() == nil && a.sess.Client() == nil {
		cfg, connected, unsupported := a.awaitOnce(ctx)
		if unsupported {
			return // an older daemon: tool calls still ask `adoptable`
		}
		if cfg != nil {
			a.mu.Lock()
			joined := a.adoptLocked(ctx, *cfg)
			a.mu.Unlock()
			if joined || a.sess.Client() != nil {
				return
			}
		}
		if connected {
			wait = adoptRetryMin
		} else if wait *= 2; wait > adoptRetryMax {
			wait = adoptRetryMax
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// awaitOnce holds one `await-room` connection until it is answered or ends.
// connected says the daemon was reached at all; unsupported that it answered
// with a refusal (a daemon of an older build, which knows no such op).
func (a *adopter) awaitOnce(ctx context.Context) (cfg *client.Config, connected, unsupported bool) {
	conn, err := net.DialTimeout("unix", a.fe.socketPath, hooks.SocketTimeout)
	if err != nil {
		return nil, false, false
	}
	defer func() { _ = conn.Close() }()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
	req, _ := json.Marshal(daemon.Request{
		Op: "await-room", V: daemon.ProtocolVersion,
		WorkspaceKey: hooks.WorkspaceKey(a.fe.ws.Dir()), Harness: a.fe.snapshot().harness,
	})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return nil, true, false
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, true, false
	}
	var res daemon.Response
	if json.Unmarshal(line, &res) != nil {
		return nil, true, false
	}
	if !res.OK {
		// An older daemon answers "unknown op" at once: there is nothing to
		// wait for there, and a retry loop against it would only spin.
		return nil, true, true
	}
	if res.Room == nil {
		return nil, true, false
	}
	got := *res.Room
	return &got, true, false
}

// wrap puts the adoption in front of every tool: join_war_room waits out an
// adoption in flight (and holds one off while it joins), every other tool
// adopts first when the session is in no room.
func (a *adopter) wrap(list []mcp.Tool) []mcp.Tool {
	if a == nil {
		return list
	}
	out := make([]mcp.Tool, len(list))
	for i, t := range list {
		t := t
		inner := t.Handler
		if inner != nil {
			if t.Name == "join_war_room" {
				t.Handler = func(ctx context.Context, args map[string]any) (string, error) {
					a.mu.Lock()
					defer a.mu.Unlock()
					return inner(ctx, args)
				}
			} else {
				t.Handler = func(ctx context.Context, args map[string]any) (string, error) {
					a.tryAdopt(ctx)
					return inner(ctx, args)
				}
			}
		}
		out[i] = t
	}
	return out
}
