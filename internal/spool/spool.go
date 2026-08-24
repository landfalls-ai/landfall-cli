package spool

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Max is the bound on queued entries per incident (FR-012).
//
// Deliberately NOT the same policy as session.PendingMax, even though the
// number matches. That queue holds INCOMING events, where the oldest is the
// stalest and dropping it is right. This one holds OUTGOING findings, where the
// oldest is the responder's earliest work — dropping it destroys something a
// human wrote and cannot get back. So a full spool REFUSES the new entry
// instead, and says so.
const Max = 50

// ErrFull is returned by Accept when the queue is at Max. The caller is
// expected to surface it to the responder rather than swallow it: believing a
// finding reached the room when it did not is the failure this package exists
// to prevent.
var ErrFull = errors.New("spool: outbound queue is full")

// Spool is one workspace's durable outbound queue. Safe for concurrent use:
// Accept arrives on an MCP handler goroutine while the worker drains on its own.
type Spool struct {
	dir string

	mu sync.Mutex
}

// stateDir is where durable state lives — NOT where the hook socket lives.
//
// internal/hooks.runtimeDir uses XDG_RUNTIME_DIR, which on Linux is tmpfs and
// is wiped on reboot. That is correct for a socket (an artifact of a running
// process) and catastrophic for a queue whose entire job is surviving one. So
// this resolves the STATE dir instead: XDG_STATE_HOME, falling back to the
// ~/.local/state that the XDG spec names and that hooks already falls back to
// on macOS.
func stateDir(getenv func(string) string) string {
	if base := getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "landfall")
	}
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".local", "state", "landfall")
}

// Open prepares the spool for one workspace. workspaceKey scopes the directory
// the same way the hook socket is scoped, so two `serve` processes in different
// repos never share a file — the drain loop assumes a single owner per file and
// takes no cross-process lock.
func Open(getenv func(string) string, workspaceKey string) (*Spool, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	dir := filepath.Join(stateDir(getenv), "spool", workspaceKey)
	// 0700: same posture as the socket directory. These entries contain the
	// responder's findings, which may quote local source.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("spool: create %s: %w", dir, err)
	}
	return &Spool{dir: dir}, nil
}

// Dir is where this spool keeps its files. Exposed for diagnostics and tests.
func (s *Spool) Dir() string { return s.dir }

func (s *Spool) path(incidentID string) string {
	return filepath.Join(s.dir, safeName(incidentID)+".jsonl")
}

// Accept durably records a hand-off and returns its id.
//
// It performs NO network I/O — it is on share_with_room's calling path and
// FR-001 requires that call to return without a round trip.
//
// It fsyncs before returning. Without that, FR-008 is a comment rather than a
// guarantee: the process can die between the write and the flush, and the
// responder is told their finding was accepted when it is gone.
func (s *Spool) Accept(incidentID, agentInstanceID, text string, refs []string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.load(incidentID)
	if err != nil {
		return nil, err
	}
	queued := 0
	for _, e := range existing {
		if e.State == Queued || e.State == Publishing {
			queued++
		}
	}
	if queued >= Max {
		return nil, ErrFull
	}

	// FR-010: redact BEFORE the entry touches disk. An entry can sit through a
	// crash, a restart and a reboot before it publishes; redacting on the way
	// out would mean the raw credential was durably stored the whole time.
	e := &Entry{
		ID:              newID(),
		IncidentID:      incidentID,
		AgentInstanceID: agentInstanceID,
		Text:            Redact(text),
		Refs:            RedactAll(refs),
		State:           Queued,
		CreatedAt:       time.Now().UTC(),
	}
	e.Redacted = looksRedacted(text, e.Text)

	if err := s.appendSync(incidentID, e); err != nil {
		return nil, err
	}
	return e, nil
}

// appendSync writes one entry and fsyncs the file. Every state change is an
// append of the whole entry rather than an in-place edit: an append that is
// torn by a crash loses only that line (readers drop it), while a rewrite that
// is torn can lose entries that were already safe.
func (s *Spool) appendSync(incidentID string, e *Entry) error {
	f, err := os.OpenFile(s.path(incidentID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("spool: open: %w", err)
	}
	defer func() { _ = f.Close() }()

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("spool: marshal: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("spool: write: %w", err)
	}
	return f.Sync()
}

// load reads the file and folds it into the current state of each entry: later
// records for the same id win. A torn trailing line is DISCARDED rather than
// erroring — a half-written line is by definition a write that never completed,
// so it was never an accepted entry.
func (s *Spool) load(incidentID string) ([]*Entry, error) {
	b, err := os.ReadFile(s.path(incidentID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("spool: read: %w", err)
	}

	byID := map[string]*Entry{}
	var order []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// Only the final line can legitimately be torn; anything else is
			// corruption we cannot silently repair, but dropping one record is
			// still better than refusing to open the queue at all.
			continue
		}
		if _, seen := byID[e.ID]; !seen {
			order = append(order, e.ID)
		}
		copyOf := e
		byID[e.ID] = &copyOf
	}

	out := make([]*Entry, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

// Recover prepares the spool for use after a restart.
//
// Entries left in Publishing belonged to a process that no longer exists, so
// their outcome is unknown: the publish may have landed before the crash. They
// are returned rather than blindly reset, because deciding is not this
// package's job — internal/bridge asks the local mirror whether each one
// already reached the room (D9) and only then acks or republishes. Resetting
// them to Queued here would be the duplicate this design exists to avoid.
func (s *Spool) Recover(incidentID string) (unknown []*Entry, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.load(incidentID)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.State == Publishing {
			unknown = append(unknown, e)
		}
	}
	return unknown, nil
}

// Next returns the oldest entry ready to publish, or nil.
func (s *Spool) Next(incidentID string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.load(incidentID)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.State == Queued {
			return e, nil
		}
	}
	return nil, nil
}

// mark records a state transition for one entry.
func (s *Spool) mark(incidentID, id string, fn func(*Entry)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.load(incidentID)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.ID == id {
			fn(e)
			return s.appendSync(incidentID, e)
		}
	}
	return fmt.Errorf("spool: no entry %s", id)
}

// Claim moves an entry to Publishing before the network call, so a crash
// mid-publish is recognizable afterwards as "outcome unknown" rather than
// looking like it was never attempted.
func (s *Spool) Claim(incidentID, id string) error {
	return s.mark(incidentID, id, func(e *Entry) { e.State = Publishing })
}

// Ack marks an entry published. Called only after the server confirms, or
// after reconciliation proves it landed before a crash.
func (s *Spool) Ack(incidentID, id string) error {
	return s.mark(incidentID, id, func(e *Entry) { e.State = Published })
}

// Fail returns an entry to the queue and records why, for backoff.
func (s *Spool) Fail(incidentID, id string, cause error) error {
	return s.mark(incidentID, id, func(e *Entry) {
		e.State = Queued
		e.Attempts++
		if cause != nil {
			e.LastError = cause.Error()
		}
	})
}

// Abandon marks every unpublished entry for a room the responder has left, and
// returns how many. The count exists so the caller can SAY so: silently
// dropping a responder's findings because they switched rooms is precisely the
// kind of quiet failure FR-012 forbids.
func (s *Spool) Abandon(incidentID string) (int, error) {
	s.mu.Lock()
	entries, err := s.load(incidentID)
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}

	n := 0
	for _, e := range entries {
		if e.State == Queued || e.State == Publishing {
			if err := s.mark(incidentID, e.ID, func(x *Entry) { x.State = Abandoned }); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// Compact rewrites the file without Published or Abandoned entries. Writes to a
// temp file and renames, so an interrupted compaction leaves the original
// intact rather than a half-written queue.
func (s *Spool) Compact(incidentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.load(incidentID)
	if err != nil {
		return err
	}

	var buf []byte
	for _, e := range entries {
		if e.State == Published || e.State == Abandoned {
			continue
		}
		line, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("spool: marshal: %w", err)
		}
		buf = append(buf, append(line, '\n')...)
	}

	tmp := s.path(incidentID) + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return fmt.Errorf("spool: compact write: %w", err)
	}
	return os.Rename(tmp, s.path(incidentID))
}

// Pending returns every entry not yet resolved, oldest first. For diagnostics
// and for the drop/abandon counters the caller surfaces.
func (s *Spool) Pending(incidentID string) ([]*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.load(incidentID)
	if err != nil {
		return nil, err
	}
	out := make([]*Entry, 0, len(entries))
	for _, e := range entries {
		if e.State == Queued || e.State == Publishing {
			out = append(out, e)
		}
	}
	return out, nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not a condition this package can paper over:
		// a colliding id would make D9's reconciliation ack the wrong entry.
		panic("spool: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// safeName keeps an incident id usable as a filename without inventing an
// escaping scheme: anything outside the allowed set becomes '_'.
func safeName(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
