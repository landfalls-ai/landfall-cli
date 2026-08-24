// Package mirror is `serve`'s durable local materialization of the incident
// (spec D9).
//
// WHY IT EXISTS — two jobs, and they need different feeds.
//
//  1. RECONCILIATION after a crash. A hand-off can die in one narrow window:
//     the server accepted the publish, but the process died before the spool
//     ack was written. On restart the outcome is unknown. Republishing blind
//     duplicates the finding in the room; acking blind loses it. The mirror
//     lets the worker ASK — "is there already an event of mine matching this?"
//     — which is what makes SC-004 achievable without the server accepting a
//     client idempotency key — which, verified against the live server, it
//     does not: the edge write endpoints accept no client-supplied key, and
//     the key the server generates is scoped to its own internal retries.
//
//  2. LOCAL READS for the harness. Room state is materialized here so the
//     coding agent reads locally instead of pulling remote.
//
// THE FEED DISTINCTION, which is easy to get wrong and silently fatal to (1):
//
//	GetContextDelta  server-CLASSIFIED, per-viewer. Drops the viewer's OWN
//	                 events unconditionally (the server classifies a viewer's
//	                 own events as routine, and drops routine entirely). Perfect for deciding what
//	                 deserves the agent's attention; USELESS for reconciliation,
//	                 because our own publishes are exactly what it removes.
//
//	GetUpdates       raw events with payloads, including the `agentInstanceId`
//	                 the server stamps on every event.
//	                 This is the feed reconciliation must use.
//
// A mirror built only on the delta would pass every test that does not kill
// the process at the wrong moment, and then republish duplicates in
// production. Hence both feeds.
//
// RANKS, NEVER REASONS. Ordering and salience come from the server's own
// classification. There is no model anywhere in this CLI and spec D5 keeps it
// that way; local inference would be a separate decision with its own
// trade-offs.
package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// Record is one observed timeline event, reduced to what the two jobs need.
type Record struct {
	Seq int64  `json:"seq"`
	Typ string `json:"type"`
	// AgentInstanceID is lifted out of the payload because reconciliation
	// keys on it. Empty for events with no agent author (a human action).
	AgentInstanceID string `json:"agent_instance_id,omitempty"`
	// Text is the publishable content, when the event carries one.
	Text string `json:"text,omitempty"`
}

// Mirror is one workspace's local materialization. Safe for concurrent use.
type Mirror struct {
	dir string
	mu  sync.Mutex
}

// Open prepares the mirror. dir should be durable storage — see internal/spool's
// stateDir for why the hook socket's runtime dir is the wrong place.
func Open(dir string) (*Mirror, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mirror: create %s: %w", dir, err)
	}
	return &Mirror{dir: dir}, nil
}

func (m *Mirror) path(incidentID string) string {
	return filepath.Join(m.dir, safeName(incidentID)+".jsonl")
}

// Record durably appends observed events. Idempotent by seq: re-recording an
// event already present is a no-op, so a replayed GetUpdates window after a
// reconnect does not corrupt the mirror.
func (m *Mirror) Record(incidentID string, events []client.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, err := m.load(incidentID)
	if err != nil {
		return err
	}
	seen := make(map[int64]struct{}, len(existing))
	for _, r := range existing {
		seen[r.Seq] = struct{}{}
	}

	var buf []byte
	for _, e := range events {
		if e.Seq == nil {
			// An event with no seq has no place in the timeline and cannot be
			// reconciled against; recording it would only add noise.
			continue
		}
		if _, dup := seen[*e.Seq]; dup {
			continue
		}
		seen[*e.Seq] = struct{}{}

		line, err := json.Marshal(recordOf(e))
		if err != nil {
			return fmt.Errorf("mirror: marshal: %w", err)
		}
		buf = append(buf, append(line, '\n')...)
	}
	if len(buf) == 0 {
		return nil
	}

	f, err := os.OpenFile(m.path(incidentID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("mirror: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(buf); err != nil {
		return fmt.Errorf("mirror: write: %w", err)
	}
	// fsync: reconciliation after a crash can only trust what actually reached
	// the disk before it.
	return f.Sync()
}

func recordOf(e client.Event) Record {
	r := Record{Typ: e.Type}
	if e.Seq != nil {
		r.Seq = *e.Seq
	}
	if s, ok := e.Payload["agentInstanceId"].(string); ok {
		r.AgentInstanceID = s
	}
	if s, ok := e.Payload["text"].(string); ok {
		r.Text = s
	}
	return r
}

// LastSeq is the highest seq recorded, or 0. Used as the GetUpdates cursor.
func (m *Mirror) LastSeq(incidentID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	records, err := m.load(incidentID)
	if err != nil {
		return 0, err
	}
	var max int64
	for _, r := range records {
		if r.Seq > max {
			max = r.Seq
		}
	}
	return max, nil
}

// FindOwn answers the reconciliation question: did an event authored by this
// agent instance, carrying this text, already land at or after minSeq?
//
// Matching is on (agentInstanceID, text) rather than an idempotency key
// because the edge write endpoints accept no client-supplied key. The residual
// false positive is narrow and stated plainly: the responder shares byte-identical
// text twice, and the process crashes between the two publishes. Adding an entry-id
// passthrough field server-side (spec task T003a) would make this exact.
func (m *Mirror) FindOwn(incidentID, agentInstanceID, text string, minSeq int64) (bool, error) {
	if agentInstanceID == "" || text == "" {
		// Refuse to guess. An empty key would match the first event of the
		// right shape and ack an entry that never published.
		return false, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	records, err := m.load(incidentID)
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if r.Seq >= minSeq && r.AgentInstanceID == agentInstanceID && r.Text == text {
			return true, nil
		}
	}
	return false, nil
}

// Records returns everything recorded for an incident, oldest first.
func (m *Mirror) Records(incidentID string) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load(incidentID)
}

// Forget drops an incident's mirror. Called when the responder leaves the room
// for good, so a stale room's events cannot satisfy a later reconciliation.
func (m *Mirror) Forget(incidentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	err := os.Remove(m.path(incidentID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("mirror: forget: %w", err)
	}
	return nil
}

func (m *Mirror) load(incidentID string) ([]Record, error) {
	b, err := os.ReadFile(m.path(incidentID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("mirror: read: %w", err)
	}

	var out []Record
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			// A torn trailing line is a write that never completed. Dropping it
			// is right; refusing to open the mirror would block reconciliation
			// entirely and force a blind republish.
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

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
