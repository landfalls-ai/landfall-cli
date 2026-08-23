package cli

// leave.go — `landfall leave`, a port of bin/landfall.mjs:304-312.
//
// Both calls are swallowed on purpose. Leaving is a best-effort tidy-up: the
// seat expires on its own once the heartbeats stop, so a failed join or a
// failed leave is never worth a non-zero exit. Having no config at all is not
// an error either — there is simply nothing to leave.

import (
	"context"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/spf13/cobra"
)

func newLeaveCommand(ui *UI, link string) *cobra.Command {
	c := newCommand(ui, "leave", func(cmd *cobra.Command, _ []string) error {
		return runLeave(cmdContext(cmd), ui, link)
	})
	c.DisableFlagParsing = true
	return c
}

func runLeave(ctx context.Context, ui *UI, link string) error {
	// NOT swallowed: a share link that will not redeem is a real failure, and
	// the Node original lets it reach the top-level handler too (exit 1).
	cfg, err := resolveConfig(ctx, ui, link)
	if err != nil {
		return err
	}
	if cfg == nil {
		ui.Log("nothing to leave — no link or LANDFALL_* config.")
		return nil
	}

	c := client.New(*cfg, nil)
	// Join first: the server issues the instance id that identifies which seat
	// is being released.
	_, _ = c.Join(ctx)
	_ = c.Leave(ctx)
	ui.Log("left the incident.")
	return nil
}
