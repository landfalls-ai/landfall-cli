package cli

// instance.go — `landfall instance [set <address> | reset]`, a port of
// bin/landfall.mjs:250-271.
//
// Which Landfall am I talking to, and WHY? The "why" is not decoration:
// seeing an unexpected address is only actionable if you can also see which
// setting produced it.
//
// The subcommands are dispatched by hand rather than registered as Cobra
// children, because today an unrecognized subcommand (`landfall instance
// wat`) falls through to the show branch instead of erroring, and Cobra would
// answer "unknown command" with a non-zero exit.

import (
	"github.com/landfalls-ai/landfall-cli/internal/instance"
	"github.com/spf13/cobra"
)

// sourceLabels explains each precedence level internal/instance can report.
// An unmapped source falls back to its own name, exactly as the Node
// original's trailing `?? instance.source` does, so a precedence level added
// later degrades to something honest instead of blank.
var sourceLabels = map[string]string{
	"default": "the built-in default",
	"config":  "your saved setting (`landfall instance reset` to clear)",
	"flag":    "the --url flag",
}

func newInstanceCommand(ui *UI) *cobra.Command {
	c := newCommand(ui, "instance", func(_ *cobra.Command, args []string) error {
		return runInstance(ui, args)
	})
	// bin/landfall.mjs reads rest[0]/rest[1] positionally and parses no flags
	// at all here; disabling flag parsing keeps args identical to that `rest`.
	c.DisableFlagParsing = true
	return c
}

func runInstance(ui *UI, args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "set":
		value := ""
		if len(args) > 1 {
			value = args[1]
		}
		address, err := instance.ParseAddress(value, "instance address")
		if err != nil {
			return err
		}
		// One address nominates both surfaces; docs are not per-deployment, so
		// they stay pointed at the built-in ones.
		if _, err := instance.SaveNomination(instance.Instance{
			Name: "custom", Web: address, API: address, Docs: instance.DefaultInstance().Docs,
		}); err != nil {
			return err
		}
		ui.Log("Landfall instance set to %s. Sign in with `landfall login`.", address)
		return nil

	case "reset":
		if instance.ClearNomination() {
			ui.Log("reset to the default (%s).", instance.DefaultInstance().Web)
		} else {
			ui.Log("already using the default.")
		}
		return nil
	}

	inst, err := instance.Resolve(instance.Options{})
	if err != nil {
		return err
	}
	from, ok := sourceLabels[inst.Source]
	if !ok {
		from = inst.Source
	}
	// Deliberately stderr, not stdout: this is a human-readable explanation,
	// and stdout is reserved for machine-readable output (FR-003).
	ui.Log("web:  %s\napi:  %s\ndocs: %s\nfrom: %s", inst.Web, inst.API, inst.Docs, from)
	return nil
}
