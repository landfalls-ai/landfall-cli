package cli

// logout.go — `landfall logout`, a port of bin/landfall.mjs:241-245.
//
// The server-side revoke inside auth.Logout is best-effort and silent (RFC
// 7009's own convention); only a failure to clear the LOCAL file is reported,
// because a sign-out that leaves the credential on disk is a sign-out that
// did not happen.

import (
	"context"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/spf13/cobra"
)

func newLogoutCommand(ui *UI) *cobra.Command {
	c := newCommand(ui, "logout", func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		if err := auth.Logout(ctx, nil); err != nil {
			return err
		}
		ui.Log("signed out — cleared the cached session.")
		return nil
	})
	// No flags today, and no flag parsing: `logout` ignores whatever follows it.
	c.DisableFlagParsing = true
	return c
}
