package daemon

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/realtime"
)

// Kind is who a reader is, and therefore which cursor rule applies to it.
type Kind string

const (
	KindAgent    Kind = "agent"    // an MCP front end (`landfall serve`)
	KindTerminal Kind = "terminal" // the person at their keyboard, in one workspace
	KindPanel    Kind = "panel"    // the Edge desktop panel
	KindPush     Kind = "push"     // a future host-side push consumer
)

// ParseKind accepts the wire spelling; anything else is an error, never a guess.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case KindAgent, KindTerminal, KindPanel, KindPush:
		return Kind(s), nil
	}
	return "", errors.New("unknown reader kind " + s)
}

// Reader is a named consumer of one room with its own cursor.
type Reader struct {
	Name         string    `json:"name"`
	Kind         Kind      `json:"kind"`
	Host         string    `json:"host"`
	WorkspaceKey string    `json:"workspaceKey"`
	Workspace    string    `json:"workspace,omitempty"`
	Cursor       int64     `json:"cursor"`
	AttachedAt   time.Time `json:"attachedAt"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
	Connected    bool      `json:"connected"`
}

// SilentReaderTTL is how long a reader that detached (or vanished) keeps its
// cursor, so a restarted agent in the same place resumes where it was.
const SilentReaderTTL = time.Hour

// TerminalReaderName is the one reader per workspace that stands for the
// person. Hooks and the status line read and advance this reader only; the
// name is defined once, in internal/hooks, because a hook process builds it
// without this package.
func TerminalReaderName(workspaceKey string) string { return hooks.TerminalReaderName(workspaceKey) }

// Untold is what this reader has not been shown: events past its cursor that
// are not room machinery, addressed messages first, then by seq. Derived on
// every call, never stored.
func Untold(events []client.Event, cursor int64) []client.Event {
	var addressed, rest []client.Event
	for _, e := range events {
		if e.Seq == nil || *e.Seq <= cursor || realtime.IsPlumbing(e.Type) {
			continue
		}
		if IsAddressed(e) {
			addressed = append(addressed, e)
		} else {
			rest = append(rest, e)
		}
	}
	bySeq := func(list []client.Event) {
		sort.SliceStable(list, func(i, j int) bool { return list[i].SeqOr(0) < list[j].SeqOr(0) })
	}
	bySeq(addressed)
	bySeq(rest)
	return append(addressed, rest...)
}

// IsAddressed: a human's chat message that names someone. The daemon does not
// know the person's own display name, so any @-mention counts.
func IsAddressed(e client.Event) bool {
	return e.Type == "chat.message" && strings.Contains(narrate.EventText(e.Payload), "@")
}

// ErrWrongKind is returned when a caller of one kind tries to move a reader of
// another kind's cursor. The rule is the point of the daemon: what a helper
// reads must never count as the person having been told.
var ErrWrongKind = errors.New("a reader's cursor is moved only by a caller of its own kind")

// Advance moves the reader's cursor forward to upTo, never backwards, if the
// caller's kind is allowed to. Returns the cursor after the call.
func (r *Reader) Advance(caller Kind, upTo int64) (int64, error) {
	if caller != r.Kind {
		return r.Cursor, ErrWrongKind
	}
	if upTo > r.Cursor {
		r.Cursor = upTo
	}
	r.LastSeenAt = time.Now()
	return r.Cursor, nil
}
