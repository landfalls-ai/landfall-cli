package cli

// install.go — `landfall install [--yes] [--only <ids>] [--dry-run]`, a port
// of `src/install/commands.mjs`'s runInstall plus bin/landfall.mjs:423-439.
//
// The command BODY is RunInstall, which takes every external effect (the
// cached session, the login flow, the harness registry, the selection prompt,
// PATH probing) as an injectable dependency and never exits the process. It
// returns outcomes plus an exit code; the Cobra wrapper is the only thing that
// prints and exits. That split is what makes the ~13 ported behaviour tests
// possible without spawning a binary or reaching the network.

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/install"
	"github.com/spf13/cobra"
)

// installFlags is `install`/`uninstall`'s own flag set. `--dry-run` is
// install-only; it is parsed for uninstall too and simply unread, exactly as
// the Node parser does, so passing it is not an error there either.
type installFlags struct {
	yes    bool
	dryRun bool
	// only is nil when --only was not given at all, which is a DIFFERENT state
	// from an empty list: `--only ""` narrows to nothing, while no flag at all
	// means every harness is a candidate.
	only []string
}

func parseInstallFlags(rest []string) installFlags {
	args := append([]string{}, rest...)
	take := func(name string) bool {
		for i, a := range args {
			if a == name {
				args = append(args[:i], args[i+1:]...)
				return true
			}
		}
		return false
	}
	f := installFlags{}
	f.yes = take("--yes")
	f.dryRun = take("--dry-run")
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
		break
	}
	return f
}

// resolveOnly narrows the registry to the named ids, and reports any id that
// names nothing — a usage error, not a silently-empty run.
func resolveOnly(harnesses []install.Harness, only []string) (candidates []install.Harness, unknown []string) {
	if only == nil {
		return harnesses, nil
	}
	known := map[string]bool{}
	for _, h := range harnesses {
		known[h.ID()] = true
	}
	for _, id := range only {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	wanted := map[string]bool{}
	for _, id := range only {
		wanted[id] = true
	}
	for _, h := range harnesses {
		if wanted[h.ID()] {
			candidates = append(candidates, h)
		}
	}
	return candidates, unknown
}

// InstallDeps are RunInstall's injectable effects. A zero value is never
// usable on its own — RunInstall fills in the real implementations for any
// field left nil, so production callers pass an empty struct.
type InstallDeps struct {
	Log                  func(format string, args ...any)
	Harnesses            []install.Harness
	Login                func(log func(string)) error
	GetCachedAccessToken func() string
	IsOnPath             func(string) bool
	PromptSelection      func(items []install.SelectItem, skipPrompt bool) []string
	Stdin                io.Reader
	Stdout               io.Writer
}

// InstallResult is what a command body returns instead of exiting.
type InstallResult struct {
	Outcomes []install.Outcome
	ExitCode int
	// UsageError is set for the exit-2 class (an unknown --only id, an
	// abandoned sign-in). When it is set, Outcomes is empty — nothing was
	// inspected, let alone written.
	UsageError string
}

func (d *InstallDeps) fill(ctx context.Context) {
	if d.Log == nil {
		d.Log = func(string, ...any) {}
	}
	if d.Harnesses == nil {
		d.Harnesses = install.Harnesses()
	}
	if d.GetCachedAccessToken == nil {
		d.GetCachedAccessToken = func() string { return auth.GetCachedAccessToken(ctx, nil) }
	}
	if d.Login == nil {
		d.Login = func(log func(string)) error {
			_, err := auth.Login(ctx, log, auth.LoginOptions{})
			return err
		}
	}
	if d.IsOnPath == nil {
		d.IsOnPath = install.IsOnPath
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

// RunInstall is `landfall install`'s body.
func RunInstall(ctx context.Context, rest []string, deps InstallDeps) InstallResult {
	deps.fill(ctx)
	f := parseInstallFlags(rest)
	candidates, unknown := resolveOnly(deps.Harnesses, f.only)
	if len(unknown) > 0 {
		return InstallResult{UsageError: "unknown harness id(s): " + strings.Join(unknown, ", "), ExitCode: 2}
	}

	// Gate on sign-in BEFORE any harness is touched — before detection, not
	// just before writing. `landfall install` registers the user's own machine
	// against their own account, so the session is a precondition of the whole
	// command, not of its side effects.
	token := deps.GetCachedAccessToken()
	if token == "" {
		deps.Log("signing in — landfall install registers your own machine, so it needs your session first.")
		if err := deps.Login(func(msg string) { deps.Log("%s", msg) }); err != nil {
			deps.Log("sign-in failed: %s", err.Error())
		}
		token = deps.GetCachedAccessToken()
	}
	if token == "" {
		return InstallResult{UsageError: "sign-in did not complete — aborting.", ExitCode: 2}
	}

	var detected []install.Harness
	for _, h := range candidates {
		if h.Detect() {
			detected = append(detected, h)
		}
	}

	var selected map[string]bool
	if len(detected) > 0 {
		items := make([]install.SelectItem, 0, len(detected))
		for _, h := range detected {
			items = append(items, install.SelectItem{ID: h.ID(), Label: h.DisplayName()})
		}
		selected = setOf(deps.PromptSelection(items, f.yes))
	}
	isDetected := map[string]bool{}
	for _, h := range detected {
		isDetected[h.ID()] = true
	}

	outcomes := make([]install.Outcome, 0, len(candidates))
	for _, h := range candidates {
		switch {
		case !isDetected[h.ID()]:
			outcomes = append(outcomes, install.Outcome{DisplayName: h.DisplayName(), Status: "not-detected"})
		case !selected[h.ID()]:
			outcomes = append(outcomes, install.Outcome{DisplayName: h.DisplayName(), Status: "skipped"})
		case f.dryRun:
			outcomes = append(outcomes, install.Outcome{DisplayName: h.DisplayName(), Status: "would-configure"})
		default:
			o := h.Install()
			o.DisplayName = h.DisplayName()
			outcomes = append(outcomes, o)
		}
	}

	// A harness registered with a `landfall` command that never resolves is a
	// silent dead end — warn as early as possible, and only when something was
	// actually configured (a dry run has registered nothing to be broken).
	if !f.dryRun && hasStatus(outcomes, "configured") && !deps.IsOnPath("landfall") {
		deps.Log("warning: `landfall` is not resolvable on PATH from this shell — a harness launching it may fail until it is.")
	}

	return InstallResult{Outcomes: outcomes, ExitCode: install.ExitCodeForOutcomes(outcomes)}
}

func setOf(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func hasStatus(outcomes []install.Outcome, status string) bool {
	for _, o := range outcomes {
		if o.Status == status {
			return true
		}
	}
	return false
}

// reportInstallResult prints the Installation Report and turns the result into
// the command's error/exit code.
//
// Report lines go to STDOUT (they are the command's machine-readable output —
// one line per harness, in a fixed order, which scripts parse); the usage
// error and the PATH warning go to stderr through ui.Log.
func reportInstallResult(ui *UI, r InstallResult) error {
	if r.UsageError != "" {
		ui.Log("%s", r.UsageError)
		return &exitError{code: r.ExitCode}
	}
	for _, o := range r.Outcomes {
		ui.Outf("%s\n", install.FormatOutcomeLine(o))
	}
	if r.ExitCode != 0 {
		return &exitError{code: r.ExitCode}
	}
	return nil
}

func newInstallCommand(ui *UI) *cobra.Command {
	c := newCommand(ui, "install", func(cmd *cobra.Command, args []string) error {
		return reportInstallResult(ui, RunInstall(cmd.Context(), args, InstallDeps{
			Log:    ui.Log,
			Stdout: ui.Out,
		}))
	})
	// Flags are parsed by parseInstallFlags out of the raw argv, exactly as
	// bin/landfall.mjs does, so that an unrecognized flag is tolerated rather
	// than turned into a usage error Cobra would raise but Node never did.
	c.DisableFlagParsing = true
	return c
}
