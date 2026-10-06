// status.go — `landfall status`, feature 20260812-010632 (US5/T048, T051). A Go
// port of `src/status.mjs`.
//
// A short-lived process, invoked repeatedly (a Claude Code statusline runs on the
// host's own render cadence), that answers "what's happening in the war room this
// workspace is joined to?" by querying the hook socket (#225) the SAME way a
// lifecycle hook already does — see internal/hooks/socket.go's header for why a
// socket and not a state file.
//
// Deliberately NOT a new transport: hooks.QueryHookSockets (fan-out, best-effort
// across every socket in this workspace, unreachable sockets skipped) already
// exists for exactly this shape of question. This file adds only the display
// half — formatting one line — and a small last-known cache for the ONE case a
// query answers "the socket didn't answer in time" (SC-012): `landfall serve`
// not running at all is a DIFFERENT case (silent, no cache — see QueryStatus).
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/spool"
	"github.com/spf13/cobra"
)

// statusCacheFile is the last-known-status file, alongside the stage in the same
// 0700 runtime directory.
const statusCacheFile = "status-cache.json"

func statusCachePath(ws hooks.Workspace) string {
	return filepath.Join(hooks.StageDir(ws), statusCacheFile)
}

// WriteStatusCache stores the last answer a socket actually gave.
//
// Best-effort: a cache we cannot write means the next timeout gets nothing to
// fall back to, never a crash. Same 0700/0600 discipline the stage uses.
// Exported so a test can drive the real fallback path in QueryStatus without
// needing a genuinely live socket.
func WriteStatusCache(status hooks.SocketResponse, ws hooks.Workspace) {
	file := statusCachePath(ws)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return
	}
	_ = os.Chmod(filepath.Dir(file), 0o700)
	body, err := json.Marshal(status)
	if err != nil {
		return
	}
	if err := os.WriteFile(file, append(body, '\n'), 0o600); err != nil {
		return
	}
	_ = os.Chmod(file, 0o600)
}

// ReadStatusCache is the last successfully-cached status, or nil when there is
// none (or it is unreadable) — never a failure.
func ReadStatusCache(ws hooks.Workspace) *hooks.SocketResponse {
	body, err := os.ReadFile(statusCachePath(ws))
	if err != nil {
		return nil
	}
	var status hooks.SocketResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return nil
	}
	return &status
}

// QueryStatus queries this workspace's status.
//
// TWO honest outcomes, not one:
//
//   - nil means "landfall serve is not running here" — no socket exists at all.
//     A stale cache would be actively misleading in that case (a closed
//     terminal's old incident bleeding into a statusline that should now be
//     blank), so THIS PATH NEVER READS THE CACHE.
//   - a socket existing but every query to it timing out falls back to the
//     last-known value instead — the session is presumably still there, just
//     momentarily slow, and showing nothing would be a worse answer than showing
//     slightly-stale truth for one render tick.
func QueryStatus(ws hooks.Workspace) *hooks.SocketResponse {
	if len(hooks.ListHookSockets(ws)) == 0 {
		return nil // not running — see the doc comment above
	}

	for _, answer := range hooks.QueryHookSockets(hooks.StatusRequest(), ws, 0) {
		if !answer.Response.OK {
			continue
		}
		WriteStatusCache(answer.Response, ws)
		return &answer.Response
	}
	// Sockets exist but none answered in time — SC-012's fallback case.
	return ReadStatusCache(ws)
}

// FormatStatusLine renders one line, or "" when there is nothing to say
// (QueryStatus returned nil — the caller must print nothing at all, per
// T051/SC-012's "silent absence, never an error line").
func FormatStatusLine(status *hooks.SocketResponse) string {
	if status == nil {
		return ""
	}
	// Named by what the person knows the room as, never its UUID.
	head := narrate.RoomName{DisplayID: status.IncidentDisplayID, Title: status.IncidentTitle}.StatusHead()
	parts := []string{head}
	if status.Pending != 0 {
		parts = append(parts, strconv.Itoa(status.Pending)+" new")
	}
	if status.VotesAwaited != 0 {
		vote := " votes awaited"
		if status.VotesAwaited == 1 {
			vote = " vote awaited"
		}
		parts = append(parts, strconv.Itoa(status.VotesAwaited)+vote)
	}
	if status.Divergence != nil && status.Divergence.Diverging {
		seg := "diverging from established root cause"
		if status.Divergence.EstablishedSubject != "" {
			seg += " (" + status.Divergence.EstablishedSubject + ")"
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, " · ")
}

// RunStatus is the `landfall status` command itself: query, format, print (or
// print nothing), ALWAYS exit 0 — a statusline command's failure mode must never
// be a visible error in someone's prompt.
//
// The line goes to ui.Out, which for this command is the real stdout: the
// statusline IS this command's machine-readable output.
func RunStatus(ui *UI, ws hooks.Workspace) int {
	if line := StatusLine(ws); line != "" {
		_, _ = ui.Out.Write([]byte(line))
	}
	return 0
}

// StatusLine is the one line RunStatus prints, or "" when this workspace reads
// no room. `landfall watch` streams the same line to the Claude Code mod.
func StatusLine(ws hooks.Workspace) string {
	// The room daemon first (feature 20260922-local-room-daemon): one call,
	// the person's own untold count. A daemon that is not running, or has no
	// room for this workspace, falls through to the per-pid sockets a
	// fallback-mode serve still binds.
	if res, err := daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{Op: "status", WorkspaceKey: hooks.WorkspaceKey(ws.Dir()), Harness: ws.Harness}, hooks.SocketTimeout); err == nil && res.Line != "" {
		ids := make([]string, 0, len(res.Rooms))
		for _, r := range res.Rooms {
			ids = append(ids, r.IncidentID)
		}
		return res.Line + spoolSuffix(ws, ids, time.Now())
	}
	status := QueryStatus(ws)
	line := FormatStatusLine(status)
	if line == "" {
		return ""
	}
	return line + spoolSuffix(ws, []string{status.IncidentID}, time.Now())
}

// spoolSuffix is what only this checkout's spool (the worker's, not the
// daemon's) knows about the rooms on the line: a room session that has
// expired (the room answered 401, so shares wait here until the person
// rejoins with a new link), hand-offs held by the working-directory rule, and
// hand-offs the room refused within spool.RefusedWindow. Counted here so the
// person sees them where they are working; "" when there is nothing to add or
// no spool to read.
func spoolSuffix(ws hooks.Workspace, incidentIDs []string, now time.Time) string {
	sp, err := spool.Open(ws.Getenv, hooks.WorkspaceKey(ws.Dir()))
	if err != nil {
		return ""
	}
	held, refused := 0, 0
	expired, waiting := false, 0
	for _, id := range incidentIDs {
		if id == "" {
			continue
		}
		if gone, n, xerr := sp.AwaitingSession(id); xerr == nil && gone {
			expired = true
			waiting += n
		}
		if items, herr := sp.Held(id); herr == nil {
			held += len(items)
		}
		if n, rerr := sp.RecentRefusals(id, now); rerr == nil {
			refused += n
		}
	}
	out := ""
	// First, because it is the one thing only the person can fix and it stops
	// everything else from reaching the room.
	if expired {
		shares := fmt.Sprintf("%d shares waiting", waiting)
		if waiting == 1 {
			shares = "1 share waiting"
		}
		out += " · session expired, " + shares + ": rejoin with a new link"
	}
	if held > 0 {
		out += fmt.Sprintf(" · %d held", held)
	}
	switch {
	case refused == 1:
		out += " · 1 share refused"
	case refused > 1:
		out += fmt.Sprintf(" · %d shares refused", refused)
	}
	return out
}

// newStatusCommand wires RunStatus into the command tree. No flags, no
// subcommands — a bare `landfall status` against the real (zero-value)
// Workspace every time.
func newStatusCommand(ui *UI) *cobra.Command {
	c := newCommand(ui, "status", func(*cobra.Command, []string) error {
		// A session the Claude Code mod speaks for draws the room in its own
		// band and status line; this one stays blank rather than repeat it.
		if os.Getenv(ModEnv) != "" {
			return nil
		}
		// The status line is drawn by one host (Claude Code sets CLAUDECODE
		// for it), so it counts what that harness's person-reader was not told.
		RunStatus(ui, hooks.Workspace{Harness: hooks.DetectHookHarness("", "", os.Getenv)})
		return nil
	})
	c.DisableFlagParsing = true
	return c
}
