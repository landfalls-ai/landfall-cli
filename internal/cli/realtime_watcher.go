package cli

import (
	"context"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/realtime"
)

// realtimeWatcher is the production liveWatcher: the adapter between serve's
// seam and internal/realtime. It is deliberately thin — every real decision
// (echo suppression, quiet-type filtering, degrade-silently on a dead socket)
// already lives in internal/realtime and is tested there.
//
// This is bin/landfall.mjs:162's keepLive half that watchIncident supplied.
// Until this existed, serve defaulted to noopWatcher and internal/realtime was
// imported by nothing — a complete, tested Socket.IO client that no shipped
// binary ever called, so the push half of the bridge was dead in v0.6.0/v0.6.1:
// a joined session only learned about room activity at its next get_updates.
type realtimeWatcher struct {
	// ui is where the best-effort one-liners go. Never nil in production;
	// a nil-safe log func is built in Watch so tests can leave it unset.
	ui *UI
}

// Watch dials the incident's Socket.IO room and feeds surviving events to
// onEvent. It never returns an error and never blocks: a connection that
// cannot come up is logged to stderr and nothing else changes, which is the
// contract test/edge-bridge.test.mjs:679 pinned ("with realtime unavailable
// the tools behave exactly as they do today, and say nothing about it").
func (w realtimeWatcher) Watch(
	ctx context.Context,
	cfg client.Config,
	ownInstanceID func() string,
	onEvent func(client.Event),
) func() {
	logf := func(string) {}
	if w.ui != nil {
		logf = func(msg string) { w.ui.Log("%s", msg) }
	}

	c := realtime.New(realtime.Options{
		BaseURL:    cfg.BaseURL,
		Slug:       cfg.Slug,
		Token:      cfg.Token,
		IncidentID: cfg.IncidentID,
		OnEvent:    onEvent,
		// A func, not a value: the server issues the instance id at
		// POST /edge/join, which can land after the socket is already up
		// (src/live.mjs:38 allowed the same). Reading it per-event keeps
		// echo suppression correct across that window instead of pinning
		// an empty id that suppresses nothing.
		OwnInstanceID: ownInstanceID,
		Log:           logf,
	})

	if err := c.Connect(ctx); err != nil {
		// Best-effort BY DESIGN: log and carry on with pull-only behaviour.
		// Returning an error here would surface a dead socket to a tool
		// caller, which is exactly what the degrade-silently contract forbids.
		logf("live: realtime unavailable (" + err.Error() + ") — falling back to get_updates pulls")
		return func() {}
	}
	return c.Stop
}
