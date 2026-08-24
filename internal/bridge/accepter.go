package bridge

import (
	"errors"

	"github.com/landfalls-ai/landfall-cli/internal/spool"
	"github.com/landfalls-ai/landfall-cli/internal/tools"
)

// Accepter adapts the spool to the narrow interface internal/tools needs.
//
// It exists so internal/tools does not import internal/spool: tools sits on
// session/client/mcp/narrate, and pulling a storage package into it would make
// the tool surface depend on how durability happens to be implemented.
//
// It also translates spool.ErrFull into tools.ErrQueueFull, so share_with_room
// can tell the responder something specific and true ("this was NOT recorded")
// rather than a generic failure. That distinction is FR-012's whole point:
// believing a finding reached the room when it did not is the failure mode
// this feature exists to prevent.
type Accepter struct {
	spool  *spool.Spool
	notify func()
}

// NewAccepter wraps a spool. notify is called after a successful accept so the
// worker can drain immediately instead of waiting out its sweep interval; nil
// is fine (the next sweep picks it up).
func NewAccepter(sp *spool.Spool, notify func()) *Accepter {
	return &Accepter{spool: sp, notify: notify}
}

// Accept records a hand-off durably and returns its id. No network I/O — it is
// on share_with_room's calling path (FR-001).
func (a *Accepter) Accept(incidentID, agentInstanceID, text string, refs []string) (string, bool, error) {
	e, err := a.spool.Accept(incidentID, agentInstanceID, text, refs)
	if err != nil {
		if errors.Is(err, spool.ErrFull) {
			return "", false, tools.ErrQueueFull
		}
		return "", false, err
	}
	if a.notify != nil {
		a.notify()
	}
	return e.ID, e.Redacted, nil
}
