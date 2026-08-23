// stage.go — where a digest waits between the doorbell wake and the next
// prompt (#227, operator decision 2026-08-05). A Go port of
// `src/hooks/stage.mjs`.
//
// WHY A STAGE EXISTS AT ALL. Claude Code's `FileChanged` "does not support
// decision control. Exit code and JSON output are ignored." — so the event that
// can WATCH the doorbell cannot DELIVER anything, and the event that can
// deliver (`UserPromptSubmit`) does not know the room changed. The stage is the
// join between them.
//
// WHY NOT IN THE WORKSPACE. `.landfall/room_events` is deliberately a
// contentless doorbell: a timestamp, a pid and a count, because a file in
// someone's repository gets grepped, backed up and occasionally committed. A
// staged digest IS room content, so it goes where the sockets already live — a
// 0700 directory outside the repo, at 0600 — and the doorbell stays
// contentless. Same staging behaviour, one directory to the left.
//
// WHAT IT BUYS over having `UserPromptSubmit` simply re-query the sockets: if
// the serve process exits between the wake and the next prompt, the socket is
// gone but the digest survives. That is the case the stage is for; a live socket
// is always preferred when one answers.
//
// WHAT IS STORED, AND WHY IT IS NOT THE RENDERED TEXT. Per-socket `peek`
// answers, not the assembled block. A stage can cover several sessions, and by
// the time it is read they may no longer share a fate: one still answering (so
// its part is spent — the tool-result flush handed those events to that agent
// in-band) while another has died (so its part is the only surviving copy).
// Delivering such a stage is therefore a PARTIAL question, and prose cannot
// answer it — BuildInjection flattens every contributing session into one
// undifferentiated string with no per-session boundary to cut on, so storing the
// text left only "all of it or none of it", and all-of-it re-delivered the
// answering session's own already-read events (#10 review). Keeping the peeks
// means the digest is re-rendered at delivery from exactly the sessions that
// still need it, by the same renderer, so the text can never diverge from the
// live path.
package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StageVersion was bumped to 2 when the stage stopped holding rendered text and
// started holding the per-socket peeks behind it. A v1 stage left by an older
// install is read as "NO STAGE" rather than migrated: its text carries no
// session boundary, so the only honest thing to do with it is the safe
// direction — one missed nudge, never a duplicate delivery.
const StageVersion = 2

// StageFile is the stage's filename inside the runtime directory.
const StageFile = "pending-digest.json"

// StageDir is the runtime directory this workspace's stage lives in — the SAME
// 0700 directory as the sockets, not the repository.
func StageDir(ws Workspace) string {
	return filepath.Join(runtimeDir(ws), WorkspaceKey(ws.Dir()))
}

// StagePath is the stage file for one workspace.
func StagePath(ws Workspace) string {
	return filepath.Join(StageDir(ws), StageFile)
}

// Stage is what is parked between the doorbell wake and the next prompt.
type Stage struct {
	V     int            `json:"v"`
	Peeks []SocketAnswer `json:"peeks"`
}

// WriteStage parks the owed peeks for the next prompt, reporting whether it
// landed. Best-effort: a stage we cannot write means the nudge is late, not that
// the room event is lost — the socket still holds it and #225's Stop hook still
// refuses a conclusion over it.
func WriteStage(peeks []SocketAnswer, ws Workspace) bool {
	file := StagePath(ws)
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	// MkdirAll's mode only applies to a directory it actually creates, so an
	// existing one keeps whatever mode it had. Same belt-and-braces chmod
	// socket.go does for this identical path — the directory mode is the whole
	// access-control story for everything that lives in here.
	_ = os.Chmod(dir, 0o700)

	body, err := json.Marshal(Stage{V: StageVersion, Peeks: peeks})
	if err != nil {
		return false
	}
	if err := os.WriteFile(file, append(body, '\n'), 0o600); err != nil {
		return false
	}
	// WriteFile's mode only applies to a file it creates, the same way it does
	// on MkdirAll — an existing stage keeps whatever mode it had.
	_ = os.Chmod(file, 0o600)
	return true
}

// ReadStage is the staged peeks, or nil when there are none (or the stage is
// unreadable, malformed, or from an older/foreign version).
func ReadStage(ws Workspace) *Stage {
	body, err := os.ReadFile(StagePath(ws))
	if err != nil {
		return nil
	}
	var parsed Stage
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	if parsed.V != StageVersion || parsed.Peeks == nil {
		return nil
	}
	// An entry with no socketPath cannot be told apart from a session that
	// answered, so it could only ever be delivered blind. Dropped rather than
	// guessed at.
	kept := make([]SocketAnswer, 0, len(parsed.Peeks))
	for _, p := range parsed.Peeks {
		// `len(Raw) == 0` is an absent `response` key; `"null"` is an explicit
		// null. Both are JS's falsy `p.response`.
		if p.SocketPath == "" || len(p.Response.Raw) == 0 || string(p.Response.Raw) == "null" {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return nil
	}
	return &Stage{V: parsed.V, Peeks: kept}
}

// ClearStage drops the stage once it has been delivered. Idempotent.
func ClearStage(ws Workspace) {
	_ = os.Remove(StagePath(ws))
}
