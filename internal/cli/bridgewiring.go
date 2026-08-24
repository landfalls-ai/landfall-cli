package cli

import (
	"github.com/landfalls-ai/landfall-cli/internal/bridge"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/mirror"
	"github.com/landfalls-ai/landfall-cli/internal/spool"
	"github.com/landfalls-ai/landfall-cli/internal/tools"

	"path/filepath"
)

// startBridge prepares the background bridge worker and the queue that
// share_with_room writes into.
//
// BEST-EFFORT, deliberately. If durable state cannot be opened — an unwritable
// home directory, a full disk — this logs once and returns nils. `serve` then
// runs exactly as it did before this feature: the tool surface omits
// share_with_room (tools.BuildWithAccepter treats a nil Accepter as "do not
// register it") and every other tool behaves identically.
//
// The alternative — refusing to start — would take MCP away from a responder
// mid-incident because a background convenience could not initialize. That
// trade is not close.
//
// Registering the tool with a broken queue would be worse still: the agent
// would be told "shared" about findings that went nowhere. Silence about a
// verb that does not exist is recoverable; a lie about one that does is not.
func startBridge(ui *UI, ws hooks.Workspace) (*bridge.Worker, tools.Accepter) {
	key := hooks.WorkspaceKey(ws.Dir())

	sp, err := spool.Open(ws.Getenv, key)
	if err != nil {
		ui.Log("bridge: no durable queue (%v) — share_with_room is unavailable this session", err)
		return nil, nil
	}

	mi, err := mirror.Open(filepath.Join(sp.Dir(), "mirror"))
	if err != nil {
		// The mirror is what makes crash reconciliation possible. Without it a
		// restart could republish a hand-off the room already has, so the queue
		// is not offered either — a duplicate finding in a live incident is a
		// worse outcome than not having the verb.
		ui.Log("bridge: no local mirror (%v) — share_with_room is unavailable this session", err)
		return nil, nil
	}

	w := bridge.New(sp, mi, func(format string, args ...any) { ui.Log(format, args...) })
	return w, bridge.NewAccepter(sp, w.Nudge)
}
