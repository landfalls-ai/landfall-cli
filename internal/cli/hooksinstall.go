package cli

// hooksinstall.go — `landfall hooks install [--only <ids>] [--dry-run]
// [--uninstall] [--host <id>]` and `landfall hooks uninstall [--only <ids>]`,
// a port of `src/hooks/commands.mjs`'s runHooksInstall/runHooksUninstall plus
// bin/landfall.mjs:367-421's `hooks` dispatcher.
//
// Two deliberate differences from `landfall install`, both load-bearing:
//
//   - NO SIGN-IN GATE. `install` gates on a session because MCP registration
//     is about reaching a war room. A hook talks to the local bridge daemon
//     over the machine's own socket; requiring a browser round trip to edit a
//     local config file would be friction with nothing behind it, and would
//     make the command unusable from a fleet provisioning script.
//   - NO INTERACTIVE SELECTION PROMPT. `hooks install` configures every host
//     it detects; `--only` narrows it. The multi-select checklist exists for
//     MCP registration because registering an agent into a war room is a
//     choice per agent. Hooks are the enhancement tier for hosts already
//     registered.

import (
	"context"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/hooks/hosts"
	"github.com/landfalls-ai/landfall-cli/internal/install"
	"github.com/spf13/cobra"
)

// hookFlags is `landfall hooks`'s flag set.
//
// `--host <id>` belongs to a hook INVOCATION rather than to install/uninstall
// — it names the host whose output contract the handler must answer on — but
// it is parsed here with the rest so that `rest[0]` stays the subcommand
// wherever on the line the flag appears.
type hookFlags struct {
	dryRun    bool
	uninstall bool
	host      string
	only      []string
	// rest is argv with every recognized flag (and its value) removed, so
	// rest[0] is the subcommand.
	rest []string
}

func parseHookFlags(argv []string) hookFlags {
	args := append([]string{}, argv...)
	takeFlag := func(name string) bool {
		for i, a := range args {
			if a == name {
				args = append(args[:i], args[i+1:]...)
				return true
			}
		}
		return false
	}
	takeValue := func(name string) string {
		for i, a := range args {
			if a != name {
				continue
			}
			if i+1 >= len(args) {
				args = append(args[:i], args[i+1:]...)
				return ""
			}
			value := args[i+1]
			args = append(args[:i], args[i+2:]...)
			return value
		}
		return ""
	}

	f := hookFlags{}
	f.dryRun = takeFlag("--dry-run")
	f.uninstall = takeFlag("--uninstall")
	f.host = takeValue("--host")
	for i, a := range args {
		if a != "--only" {
			continue
		}
		value := ""
		if i+1 < len(args) {
			value = args[i+1]
		}
		f.only = []string{}
		for _, part := range strings.Split(value, ",") {
			if s := strings.TrimSpace(part); s != "" {
				f.only = append(f.only, s)
			}
		}
		drop := i + 2
		if drop > len(args) {
			drop = len(args)
		}
		args = append(args[:i], args[drop:]...)
		break
	}
	f.rest = args
	return f
}

func resolveHookOnly(all []hosts.Host, only []string) (candidates []hosts.Host, unknown []string) {
	if only == nil {
		return all, nil
	}
	known := map[string]bool{}
	for _, h := range all {
		known[h.ID()] = true
	}
	for _, id := range only {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	wanted := setOf(only)
	for _, h := range all {
		if wanted[h.ID()] {
			candidates = append(candidates, h)
		}
	}
	return candidates, unknown
}

// installStatusFor maps a merge-core action onto the report vocabulary shared
// with `landfall install`. Anything that is neither `configured` nor
// `conflict` — `write` on a plan, `already-installed` — reports as
// already-installed; the dry-run caller special-cases `write` before getting
// here.
func installStatusFor(action string) string {
	switch action {
	case install.ActionConfigured:
		return "configured"
	case install.ActionConflict:
		return "conflict"
	default:
		return "already-installed"
	}
}

const hookConflictDetail = "an existing landfall hook entry differs from what this installer would write — left untouched"

// HooksDeps are the hook commands' injectable effects.
type HooksDeps struct {
	Log      func(format string, args ...any)
	Hosts    []hosts.Host
	IsOnPath func(string) bool
}

func (d *HooksDeps) fill() {
	if d.Log == nil {
		d.Log = func(string, ...any) {}
	}
	if d.Hosts == nil {
		d.Hosts = hosts.Hosts()
	}
	if d.IsOnPath == nil {
		d.IsOnPath = install.IsOnPath
	}
}

// RunHooksInstall is `landfall hooks install`'s body.
func RunHooksInstall(_ context.Context, argv []string, deps HooksDeps) InstallResult {
	deps.fill()
	f := parseHookFlags(argv)
	candidates, unknown := resolveHookOnly(deps.Hosts, f.only)
	if len(unknown) > 0 {
		return InstallResult{UsageError: "unknown hook host id(s): " + strings.Join(unknown, ", "), ExitCode: 2}
	}

	outcomes := make([]install.Outcome, 0, len(candidates))
	for _, h := range candidates {
		if !h.Detect() {
			outcomes = append(outcomes, install.Outcome{DisplayName: h.DisplayName(), Status: "not-detected"})
			continue
		}
		var (
			result hosts.Result
			err    error
		)
		if f.dryRun {
			result, err = h.Plan()
		} else {
			result, err = h.Install()
		}
		if err != nil {
			// An unparseable config file lands here, and MUST: it is reported
			// with the path and never overwritten.
			outcomes = append(outcomes, install.Outcome{
				DisplayName: h.DisplayName(), Status: "failed",
				Detail: err.Error(), ConfigPath: h.ConfigPath(),
			})
			continue
		}
		status := installStatusFor(result.Action)
		if f.dryRun && result.Action == install.ActionWrite {
			status = "would-configure"
		}
		detail := result.Detail
		if result.Action == install.ActionConflict {
			detail = hookConflictDetail
		}
		outcomes = append(outcomes, install.Outcome{
			DisplayName: h.DisplayName(), Status: status,
			Detail: detail, ConfigPath: h.ConfigPath(),
		})
	}

	// The same warning `landfall install` raises, for the same reason: a hook
	// whose command does not resolve fires on every turn and fails on every turn.
	if !f.dryRun && hasStatus(outcomes, "configured") && !deps.IsOnPath("landfall") {
		deps.Log("warning: `landfall` is not resolvable on PATH from this shell — the hooks just registered will fail until it is.")
	}

	return InstallResult{Outcomes: outcomes, ExitCode: install.ExitCodeForOutcomes(outcomes)}
}

// RunHooksUninstall is `landfall hooks uninstall`'s body (also reachable as
// `landfall hooks install --uninstall`).
func RunHooksUninstall(_ context.Context, argv []string, deps HooksDeps) InstallResult {
	deps.fill()
	f := parseHookFlags(argv)
	candidates, unknown := resolveHookOnly(deps.Hosts, f.only)
	if len(unknown) > 0 {
		return InstallResult{UsageError: "unknown hook host id(s): " + strings.Join(unknown, ", "), ExitCode: 2}
	}

	outcomes := make([]install.Outcome, 0, len(candidates))
	for _, h := range candidates {
		// Unlike install, this does NOT gate on Detect(): a host uninstalled
		// from the machine can still have landfall entries in its config, and
		// those entries are what this command exists to remove.
		if !h.HasEntry() {
			outcomes = append(outcomes, install.Outcome{
				DisplayName: h.DisplayName(), Status: "not-installed", ConfigPath: h.ConfigPath(),
			})
			continue
		}
		result, err := h.Uninstall()
		if err != nil {
			outcomes = append(outcomes, install.Outcome{
				DisplayName: h.DisplayName(), Status: "failed",
				Detail: err.Error(), ConfigPath: h.ConfigPath(),
			})
			continue
		}
		status := "left-in-place"
		switch result.Action {
		case install.ActionRemoved:
			status = "removed"
		case install.ActionNotInstalled:
			status = "not-installed"
		}
		outcomes = append(outcomes, install.Outcome{
			DisplayName: h.DisplayName(), Status: status, ConfigPath: h.ConfigPath(),
		})
	}

	return InstallResult{Outcomes: outcomes, ExitCode: install.ExitCodeForOutcomes(outcomes)}
}

// hooksUsage is bin/landfall.mjs:404-406's usage line, built from the live
// event registry so it can never list an event that is not registered.
func hooksUsage() string {
	return "usage: landfall hooks <install|uninstall|policy|" +
		strings.Join(hooks.HookEventIDs(), "|") +
		"> [--only <ids>] [--dry-run] [--uninstall] [--host <id>]"
}

// hooksEventDispatch is a TEMPORARY SEAM for the tracks that own the hook
// EVENT handlers (`landfall hooks stop|file-changed|user-prompt-submit|
// pre-tool-use`, tasks T039-T045) and `hooks policy` (T043). Neither is in
// this file's scope.
//
// Until one of those tracks assigns it, any `hooks` subcommand that is not
// install/uninstall prints the usage line and exits 2 — which is exactly what
// bin/landfall.mjs does for an unrecognized subcommand, and is the safe answer
// for a not-yet-ported one: a hook that silently exits 0 would tell its host
// "nothing to report" on every turn, which is a wrong answer rather than a
// missing one.
//
// The owning track should assign this (from its own file's init, or by
// replacing newHooksCommand outright) rather than editing the dispatch below.
var hooksEventDispatch func(ui *UI, sub string, argv []string) (handled bool, err error)

func newHooksCommand(ui *UI) *cobra.Command {
	c := newCommand(ui, "hooks", func(cmd *cobra.Command, argv []string) error {
		f := parseHookFlags(argv)
		sub := ""
		if len(f.rest) > 0 {
			sub = f.rest[0]
		}

		switch {
		case sub == "install" && !f.uninstall:
			return reportInstallResult(ui, RunHooksInstall(cmd.Context(), argv, HooksDeps{Log: ui.Log}))
		case sub == "install" || sub == "uninstall":
			return reportInstallResult(ui, RunHooksUninstall(cmd.Context(), argv, HooksDeps{Log: ui.Log}))
		}

		if hooksEventDispatch != nil {
			if handled, err := hooksEventDispatch(ui, sub, argv); handled {
				return err
			}
		}
		ui.Log("%s", hooksUsage())
		return usage()
	})
	c.DisableFlagParsing = true
	return c
}
