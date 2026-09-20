package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func TestStateRoundTripsRoomsTokensAndCursorsAt0600(t *testing.T) {
	dir := t.TempDir()
	path := StatePath(dir)
	st := &State{V: StateVersion, Rooms: map[string]RoomState{
		"http://x/o/acme/inc-1": {
			Config:     client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "edge-secret"},
			CwdAllowed: true,
			Readers: map[string]*Reader{
				"terminal:ws": {Name: "terminal:ws", Kind: KindTerminal, Cursor: 52, WorkspaceKey: "ws", LastSeenAt: time.Now()},
			},
		},
	}}
	if err := SaveState(path, st); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state.json mode = %o, want 0600 (it holds the room token)", info.Mode().Perm())
	}
	dinfo, _ := os.Stat(filepath.Dir(path))
	if dinfo.Mode().Perm() != 0o700 {
		t.Fatalf("daemon dir mode = %o, want 0700", dinfo.Mode().Perm())
	}
	back, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	room := back.Rooms["http://x/o/acme/inc-1"]
	if room.Config.Token != "edge-secret" || !room.CwdAllowed || room.Readers["terminal:ws"].Cursor != 52 {
		t.Fatalf("round trip lost data: %+v", room)
	}
}

func TestMissingStateIsEmptyAndCorruptStateIsMovedAsideNotFatal(t *testing.T) {
	dir := t.TempDir()
	path := StatePath(dir)
	st, err := LoadState(path)
	if err != nil || len(st.Rooms) != 0 {
		t.Fatalf("missing file should be an empty state, got %+v %v", st, err)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	st, err = LoadState(path)
	if st == nil || len(st.Rooms) != 0 {
		t.Fatal("a corrupt file must still yield an empty, usable state")
	}
	if err == nil || !strings.Contains(err.Error(), "moved to") {
		t.Fatalf("corrupt file should be reported and moved aside, got %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("the corrupt file should no longer be at the state path")
	}
}
