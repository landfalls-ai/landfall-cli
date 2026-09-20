package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// StateVersion is bumped on an incompatible change to the file's shape.
const StateVersion = 1

// State is everything the daemon must remember across its own restarts.
//
// It INCLUDES each room's redeemed edge token. That token lives today only in
// the `serve` process's client.Config and dies with it, which is why a restarted
// `serve` has always needed a fresh link; a short-link ticket is single-use and
// cannot be re-redeemed. The daemon rejoining a room after a restart (spec
// FR-013) is only possible if the token is written down. It is written at 0600
// in a 0700 directory — the same trust level as the person's login session in
// credentials.json (internal/auth/credentials.go), the CLI having no keychain
// store anywhere (design review 2026-09-21, finding 3).
type State struct {
	V     int                  `json:"v"`
	Rooms map[string]RoomState `json:"rooms"`
}

// RoomState is one room as persisted.
type RoomState struct {
	Config     client.Config      `json:"config"`
	CwdAllowed bool               `json:"cwdAllowed"`
	Readers    map[string]*Reader `json:"readers"`
	SavedAt    time.Time          `json:"savedAt"`
}

// StatePath is <RuntimeDir>/daemon/state.json.
func StatePath(runtimeDir string) string { return filepath.Join(runtimeDir, "daemon", "state.json") }

// LoadState reads the file. A missing file is an empty state. An unreadable or
// wrong-version file is moved aside (state.json.corrupt-<unix>) and reported,
// never fatal: the daemon must start; readers will re-attach and rejoin.
func LoadState(path string) (*State, error) {
	empty := &State{V: StateVersion, Rooms: map[string]RoomState{}}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	var st State
	if uerr := json.Unmarshal(body, &st); uerr != nil || st.V != StateVersion {
		aside := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		_ = os.Rename(path, aside)
		if uerr == nil {
			uerr = fmt.Errorf("state version %d, want %d", st.V, StateVersion)
		}
		return empty, fmt.Errorf("state.json unreadable (%v); moved to %s", uerr, aside)
	}
	if st.Rooms == nil {
		st.Rooms = map[string]RoomState{}
	}
	return &st, nil
}

// SaveState writes atomically: temp file in the same directory, fsync, rename.
// 0600 file, 0700 directory, every time, even if someone loosened them.
func SaveState(path string, st *State) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
