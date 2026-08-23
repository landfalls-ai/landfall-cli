package cli

// login.go — `landfall login [--url <address>] [--save]`, a port of
// bin/landfall.mjs:224-239.
//
// Both flags are opt-in, and that is the whole point: a customer who passes
// neither reaches the hosted service. `--url` points THIS sign-in at a
// specific Landfall; `--save` also makes it the default for later commands.

import (
	"context"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/instance"
	"github.com/spf13/cobra"
)

func newLoginCommand(ui *UI) *cobra.Command {
	var (
		url  string
		save bool
	)
	c := newCommand(ui, "login", func(cmd *cobra.Command, _ []string) error {
		return runLogin(cmd.Context(), ui, url, save)
	})
	c.Flags().StringVar(&url, "url", "", "sign in to a specific Landfall instance")
	c.Flags().BoolVar(&save, "save", false, "also make that instance the default")
	// bin/landfall.mjs scans `rest` for these two and ignores everything else,
	// so an unrecognized flag is not an error today. Tolerating them keeps the
	// exit code identical (0, not 2) for a script that passes one.
	c.FParseErrWhitelist = cobra.FParseErrWhitelist{UnknownFlags: true}
	return c
}

func runLogin(ctx context.Context, ui *UI, url string, save bool) error {
	if ctx == nil {
		ctx = context.Background()
	}

	// Resolved ONCE and reused for the sign-in and the nomination: resolving
	// twice is how a login could probe one origin and save another.
	inst, err := instance.Resolve(instance.Options{URL: url})
	if err != nil {
		return err
	}

	if _, err := auth.Login(ctx, func(msg string) { ui.Log("%s", msg) }, auth.LoginOptions{
		URL:      url,
		Instance: &inst,
	}); err != nil {
		return err
	}

	if save {
		if _, err := instance.SaveNomination(instance.Instance{
			Name: inst.Name, Web: inst.Web, API: inst.API, Docs: inst.Docs,
		}); err != nil {
			return err
		}
		ui.Log("saved %s as your Landfall. Undo with `landfall instance reset`.", inst.Web)
	}

	ui.Log("signed in — session cached. You can now join a war room with no share link.")
	return nil
}
