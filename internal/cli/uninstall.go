package cli

// uninstall.go — `landfall uninstall [--yes] [--only <ids>]`, a port of
// `src/install/commands.mjs`'s runUninstall.
//
// NO SIGN-IN GATE, deliberately and unlike `install`: removing a registration
// from a local config file is not an authenticated action, and requiring a
// browser round trip to undo a local edit would strand anyone whose session
// has expired — precisely the person most likely to want to undo it.

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/install"
	"github.com/spf13/cobra"
)

// UninstallDeps are RunUninstall's injectable effects. Deliberately a smaller
// set than InstallDeps: there is no login, no cached-token read and no PATH
// probe here, because none of those is part of removing a registration.
type UninstallDeps struct {
	Harnesses       []install.Harness
	PromptSelection func(items []install.SelectItem, skipPrompt bool) []string
	Stdin           io.Reader
	Stdout          io.Writer
}

func (d *UninstallDeps) fill() {
	if d.Harnesses == nil {
		d.Harnesses = install.Harnesses()
	}
	if d.Stdin == nil {
		d.Stdin = os.Stdin
	}
	if d.Stdout == nil {
		d.Stdout = stdout
	}
	if d.PromptSelection == nil {
		d.PromptSelection = func(items []install.SelectItem, skipPrompt bool) []string {
			return install.PromptSelection(items, skipPrompt, d.Stdin, d.Stdout)
		}
	}
}

// RunUninstall is `landfall uninstall`'s body.
//
// The candidate list is built from HasEntry() — a non-mutating peek — rather
// than from Detect(): a harness that has since been deleted from the machine
// can still have a landfall entry sitting in its config file, and that entry
// is exactly what this command exists to remove.
func RunUninstall(_ context.Context, rest []string, deps UninstallDeps) InstallResult {
	deps.fill()
	f := parseInstallFlags(rest)
	candidates, unknown := resolveOnly(deps.Harnesses, f.only)
	if len(unknown) > 0 {
		return InstallResult{UsageError: "unknown harness id(s): " + strings.Join(unknown, ", "), ExitCode: 2}
	}

	var withEntry []install.Harness
	hasEntry := map[string]bool{}
	for _, h := range candidates {
		if h.HasEntry() {
			withEntry = append(withEntry, h)
			hasEntry[h.ID()] = true
		}
	}

	var selected map[string]bool
	if len(withEntry) > 0 {
		items := make([]install.SelectItem, 0, len(withEntry))
		for _, h := range withEntry {
			items = append(items, install.SelectItem{ID: h.ID(), Label: h.DisplayName()})
		}
		selected = setOf(deps.PromptSelection(items, f.yes))
	}

	outcomes := make([]install.Outcome, 0, len(candidates))
	for _, h := range candidates {
		switch {
		case !hasEntry[h.ID()]:
			outcomes = append(outcomes, install.Outcome{DisplayName: h.DisplayName(), Status: "not-installed"})
		case !selected[h.ID()]:
			outcomes = append(outcomes, install.Outcome{DisplayName: h.DisplayName(), Status: "skipped"})
		default:
			o := h.Uninstall()
			o.DisplayName = h.DisplayName()
			outcomes = append(outcomes, o)
		}
	}

	return InstallResult{Outcomes: outcomes, ExitCode: install.ExitCodeForOutcomes(outcomes)}
}

func newUninstallCommand(ui *UI) *cobra.Command {
	c := newCommand(ui, "uninstall", func(cmd *cobra.Command, args []string) error {
		return reportInstallResult(ui, RunUninstall(cmd.Context(), args, UninstallDeps{Stdout: ui.Out}))
	})
	c.DisableFlagParsing = true
	return c
}
