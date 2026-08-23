package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func peekAnswer(t *testing.T, socketPath string, res PeekResponse) SocketAnswer {
	t.Helper()
	body, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var wide SocketResponse
	if err := json.Unmarshal(body, &wide); err != nil {
		t.Fatal(err)
	}
	return SocketAnswer{SocketPath: socketPath, Response: wide}
}

func TestStageLivesOutsideTheRepositoryAlongsideTheSockets(t *testing.T) {
	ws := tempWorkspace(t)
	// `.landfall/room_events` is deliberately a contentless doorbell; a staged
	// digest IS room content, so it goes where the sockets already live.
	if !strings.HasPrefix(StagePath(ws), SocketLocationFor(ws).Dir) {
		t.Fatalf("stage %q is not in the socket directory %q", StagePath(ws), SocketLocationFor(ws).Dir)
	}
	if strings.Contains(StagePath(ws), string(filepath.Separator)+DoorbellDir+string(filepath.Separator)) {
		t.Fatal("the stage must never be written into the workspace")
	}
	if filepath.Base(StagePath(ws)) != "pending-digest.json" {
		t.Fatalf("stage file is %q", filepath.Base(StagePath(ws)))
	}
}

func TestStageRoundTripsPerSocketPeekAnswers(t *testing.T) {
	ws := tempWorkspace(t)
	a := peekAnswer(t, "/s/1", PeekResponse{OK: true, V: 1, IncidentID: "inc-a", Count: 2, Cursor: 3, MaxSeq: 5, Digest: []string{"#4 x", "#5 y"}})
	b := peekAnswer(t, "/s/2", PeekResponse{OK: true, V: 1, IncidentID: "inc-b", Count: 1, Cursor: 8, MaxSeq: 9, Digest: []string{"#9 z"}})

	if !WriteStage([]SocketAnswer{a, b}, ws) {
		t.Fatal("WriteStage reported failure")
	}
	got := ReadStage(ws)
	if got == nil || len(got.Peeks) != 2 {
		t.Fatalf("got %+v", got)
	}
	// Per-socket answers, NOT rendered text: the digest is re-rendered at
	// delivery from exactly the sessions that still need it.
	if got.Peeks[0].SocketPath != "/s/1" || got.Peeks[0].Response.CountOr(0) != 2 {
		t.Fatalf("peek 0: %+v", got.Peeks[0])
	}
	if got.Peeks[1].Response.MaxSeqOr(-1) != 9 || len(got.Peeks[1].Response.Digest) != 1 {
		t.Fatalf("peek 1: %+v", got.Peeks[1])
	}
}

func TestStageIsWrittenAt0600InA0700Directory(t *testing.T) {
	ws := tempWorkspace(t)
	dir := filepath.Dir(StagePath(ws))
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil { // pre-existing, wide open
		t.Fatal(err)
	}

	if !WriteStage([]SocketAnswer{peekAnswer(t, "/s/1", PeekResponse{OK: true, Count: 1})}, ws) {
		t.Fatal("WriteStage reported failure")
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("directory mode %o, want 700", perm)
	}
	fileInfo, err := os.Stat(StagePath(ws))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("stage mode %o, want 600", perm)
	}
}

func TestAV1StageIsReadAsNoStageRatherThanMigrated(t *testing.T) {
	ws := tempWorkspace(t)
	if err := os.MkdirAll(filepath.Dir(StagePath(ws)), 0o700); err != nil {
		t.Fatal(err)
	}
	// v1 held RENDERED TEXT, which carries no session boundary — the only honest
	// thing to do with it is the safe direction: one missed nudge, never a
	// duplicate delivery.
	if err := os.WriteFile(StagePath(ws), []byte(`{"v":1,"context":"⚡ 2 update(s)…"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadStage(ws); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

func TestReadStageDropsEntriesWithNoSocketPath(t *testing.T) {
	ws := tempWorkspace(t)
	if err := os.MkdirAll(filepath.Dir(StagePath(ws)), 0o700); err != nil {
		t.Fatal(err)
	}
	// An entry with no socketPath cannot be told apart from a session that
	// answered, so it could only ever be delivered blind.
	body := `{"v":2,"peeks":[{"response":{"ok":true,"count":1}},{"socketPath":"/s/2"},{"socketPath":"/s/3","response":{"ok":true,"count":1}}]}`
	if err := os.WriteFile(StagePath(ws), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ReadStage(ws)
	if got == nil || len(got.Peeks) != 1 || got.Peeks[0].SocketPath != "/s/3" {
		t.Fatalf("got %+v", got)
	}
}

func TestReadStageAnswersNilForAbsentUnreadableOrEmptyStages(t *testing.T) {
	ws := tempWorkspace(t)
	if got := ReadStage(ws); got != nil {
		t.Fatalf("absent: got %+v", got)
	}
	if err := os.MkdirAll(filepath.Dir(StagePath(ws)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StagePath(ws), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadStage(ws); got != nil {
		t.Fatalf("garbage: got %+v", got)
	}
	if err := os.WriteFile(StagePath(ws), []byte(`{"v":2,"peeks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadStage(ws); got != nil {
		t.Fatalf("empty: got %+v", got)
	}
}

func TestClearStageIsIdempotent(t *testing.T) {
	ws := tempWorkspace(t)
	WriteStage([]SocketAnswer{peekAnswer(t, "/s/1", PeekResponse{OK: true, Count: 1})}, ws)
	ClearStage(ws)
	if got := ReadStage(ws); got != nil {
		t.Fatalf("got %+v", got)
	}
	ClearStage(ws)
}
