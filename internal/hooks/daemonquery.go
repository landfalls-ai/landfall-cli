// daemonquery.go — the hooks read the room daemon directly (feature
// 20260922-local-room-daemon, tasks T014/T016, research R11).
//
// Before this file a hook only knew the per-pid sockets under
// <runtimeDir>/<workspaceKey>/. In daemon mode every one of those is a proxy
// for the same terminal reader (internal/cli/frontend.go daemonSession), so a
// hook made N round trips for one answer, lost the answer when no front end
// happened to be alive, and raced every other hook for the same sockets.
//
// Now a hook asks the daemon first, at <runtimeDir>/daemon.sock, for the
// terminal reader of its workspace. One answer per room. The per-pid sockets
// are still asked, for two reasons:
//
//   - a `serve` in fallback mode (LANDFALL_DAEMON=0, or no daemon could be
//     started) is a real session with its own cursor and no daemon behind it;
//   - the room's attention (quarantined citations, awaited positions) lives in
//     the front end's own session snapshot, which the daemon does not carry.
//
// So the two are MERGED, per incident: a per-pid answer for an incident the
// daemon already answered contributes its attention and nothing else (its
// events are the same terminal reader's untold set, already counted); a
// per-pid answer for any other incident stands on its own. A consume for a
// daemon answer goes to the daemon for the terminal reader; a consume for a
// per-pid answer goes to that socket, exactly as before.
package hooks

import (
	"strings"
	"time"
)

// daemonAnswerPrefix marks a SocketAnswer that came from the daemon. The rest
// of the path is the room key, which is what a consume needs.
const daemonAnswerPrefix = "daemon:"

// IsDaemonAnswer reports whether an answer's SocketPath names the daemon.
func IsDaemonAnswer(socketPath string) bool { return strings.HasPrefix(socketPath, daemonAnswerPrefix) }

// DaemonRoomKeyOf is the room key a daemon answer's SocketPath carries.
func DaemonRoomKeyOf(socketPath string) string {
	return strings.TrimPrefix(socketPath, daemonAnswerPrefix)
}

// DaemonPeek asks the daemon what the terminal reader of this workspace has
// not been told, one SocketAnswer per room. Nil when there is no daemon, it did
// not answer in time, or it has no room this workspace reads — every one of
// which means "ask the per-pid sockets alone", never an error.
func DaemonPeek(ws Workspace, timeout time.Duration) []SocketAnswer {
	res, err := SendToSocket(DaemonSocketPath(ws), SocketRequest{Op: "peek", WorkspaceKey: WorkspaceKey(ws.Dir())}, timeout)
	if err != nil || res == nil || !res.OK {
		return nil
	}
	answers := make([]SocketAnswer, 0, len(res.Rooms))
	for _, r := range res.Rooms {
		count, cursor, maxSeq := r.Count, r.Cursor, r.MaxSeq
		answers = append(answers, SocketAnswer{
			SocketPath: daemonAnswerPrefix + r.RoomKey,
			Response: SocketResponse{
				OK: true, V: res.V,
				IncidentID: r.IncidentID, Slug: r.Slug, Connected: r.Connection == "live",
				Count: &count, Cursor: &cursor, MaxSeq: &maxSeq, Digest: r.Digest,
			},
		})
	}
	return answers
}

// QueryWorkspace is what every hook asks: the daemon first, then the per-pid
// sockets, merged per incident as the file header describes. Only `peek` is
// answered by the daemon; any other verb goes to the per-pid sockets alone.
func QueryWorkspace(req SocketRequest, ws Workspace, timeout time.Duration) []SocketAnswer {
	perPid := QueryHookSockets(req, ws, timeout)
	if req.verb() != "peek" {
		return perPid
	}
	return MergeAnswers(DaemonPeek(ws, timeout), perPid)
}

// MergeAnswers folds per-pid answers into the daemon's, per incident. Pure.
func MergeAnswers(fromDaemon, perPid []SocketAnswer) []SocketAnswer {
	if len(fromDaemon) == 0 {
		return perPid
	}
	byIncident := make(map[string]int, len(fromDaemon))
	for i, a := range fromDaemon {
		byIncident[a.Response.IncidentID] = i
	}
	out := append([]SocketAnswer(nil), fromDaemon...)
	for _, p := range perPid {
		i, covered := byIncident[p.Response.IncidentID]
		if !covered || p.Response.IncidentID == "" {
			out = append(out, p)
			continue
		}
		if out[i].Response.Attention == nil && p.Response.Attention != nil {
			out[i].Response.Attention = p.Response.Attention
		}
	}
	return out
}

// SendToAnswer sends a follow-up (a consume) to wherever an answer came from:
// the daemon, for the terminal reader of this workspace and the answer's room,
// or the per-pid socket named by the path.
func SendToAnswer(ws Workspace, socketPath string, req SocketRequest, timeout time.Duration) error {
	if !IsDaemonAnswer(socketPath) {
		_, err := SendToSocket(socketPath, req, timeout)
		return err
	}
	req.RoomKey = DaemonRoomKeyOf(socketPath)
	req.ReaderName = TerminalReaderName(WorkspaceKey(ws.Dir()))
	res, err := SendToSocket(DaemonSocketPath(ws), req, timeout)
	if err != nil {
		return err
	}
	if !res.OK {
		return &daemonError{res.Error}
	}
	return nil
}

type daemonError struct{ msg string }

func (e *daemonError) Error() string { return "daemon: " + e.msg }
