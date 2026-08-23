package cli

// root.go — the Cobra root command and the argv grammar that must survive the
// port unchanged (T025).
//
// Three things happen BEFORE Cobra ever sees an argument, because Cobra's own
// defaults are observably different from what `bin/landfall.mjs` does today
// (contracts/cli-commands.md, "Argv grammar — three Cobra parity traps"):
//
//  1. `--help` / `-h` anywhere in argv, or `help` as argv[0], prints the ONE
//     top-level usage text and exits 0 (bin/landfall.mjs:217-220). Cobra would
//     give per-command help instead; every route to Cobra's own help is
//     disabled below so `landfall connect aws --help` cannot print anything
//     else.
//  2. `--link URL` is spliced out of argv GLOBALLY, whatever command follows
//     (bin/landfall.mjs:84-85), so `landfall install --link X` still installs
//     rather than failing on an unknown flag.
//  3. A bare `https?://…` first argument means `serve`, with the URL as the
//     link (bin/landfall.mjs:86-87), and `LANDFALL_LINK` is the fallback for
//     both forms.
//
// Only after that is a command name resolved and handed to Cobra.

import (
	"errors"
	"os"
	"regexp"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/instance"
	"github.com/spf13/cobra"
)

// helpText is bin/landfall.mjs's HELP_TEXT constant (line 181), byte for byte.
// test/help.test.mjs — ported to help_test.go — asserts against it through a
// real spawned binary, so it is deliberately a literal rather than something
// generated from the command tree: a Cobra-generated usage block would drift
// from this the first time a command's flags change.
const helpText = `landfall — join a Landfall war room from your terminal

Usage: landfall <command> [options]

Commands:
  login [--url <address>] [--save]                       sign in (browser); caches the session
  logout                                                 clear the cached session
  instance [set <address> | reset]                       show or change which Landfall you use
  serve [--link URL]                                     (default) join + expose incident MCP tools over stdio
  status                                                 one-line room status (incident, new events, votes
                                                          awaited) — for a Claude Code statusLine; prints
                                                          nothing when no session is running
  join [URL]                                             join + keep presence alive (no MCP) — Ctrl-C to leave
  note "<text>"                                          post a one-off finding, then exit
  leave                                                   leave the incident
  install [--yes] [--only <ids>] [--dry-run]              register this machine's coding agents
  uninstall [--yes] [--only <ids>]                        remove that registration
  connect aws [--terraform] [--name <id>] [--region <r>]  connect your AWS account: creates a
              [--management --member <acct>[,...]]        read-only role with YOUR own aws CLI
                                                          (or prints Terraform), registers and
                                                          health-checks the connection
  remediation approve <id> --incident <incidentId>        approve a proposed remediation (your
              [--org <slug>] [--override "<reason>"]      own landfall login session, never an
                                                          MCP tool); --override is for a genuinely
                                                          solo responder, audited, one-time-only
  hooks install [--only <ids>] [--dry-run] [--uninstall]  register lifecycle hooks so room context
                                                          reaches a local session it can't ignore
  hooks uninstall [--only <ids>]                          remove only landfall's hook entries
  hooks policy [--init]                                   print the local production allow-list and
                                                          exactly what each rule would report

Run 'landfall <command>' with no further arguments for command-specific behavior.
Docs: https://github.com/landfalls-ai/landfall-cli`

// defaultCommand is what a bare `landfall`, a bare URL, or an unrecognized
// command name runs. The last of those is not an accident: today's dispatcher
// is a chain of `if (cmd === …)` checks that FALLS THROUGH to serve
// (bin/landfall.mjs:441), so `landfall bogus` serves rather than erroring.
// Cobra would answer "unknown command" and exit non-zero, which is an
// observable exit-code change on an input a script may already pass.
const defaultCommand = "serve"

// Execute runs the CLI with the given arguments (os.Args[1:]) and returns the
// process exit code: 0 success, 1 fatal error, 2 usage/config error or an
// unreachable instance — see contracts/cli-commands.md's Global conventions.
//
// It never calls os.Exit itself. main owns the single exit, after every writer
// has been flushed — the Go shape of the hazard bin/landfall.mjs:432-437
// documents (a hard exit right after a stdout write can truncate it).
func Execute(args []string) int {
	return run(New(), args, os.Getenv)
}

// run is Execute with its environment injected, so the grammar can be tested
// without touching the developer's own exports.
func run(ui *UI, args []string, getenv func(string) string) int {
	// Strict parity: help wins over everything, including a command that would
	// otherwise consume the flag as an argument. `landfall note "--help"`
	// prints usage today and must keep doing so (OD-2).
	if wantsHelp(args) {
		ui.Outf("%s\n", helpText)
		return 0
	}

	name, link, rest := parseArgs(args, getenv)
	root := newRootCommand(ui, link)
	if !hasCommand(root, name) {
		name = defaultCommand
	}
	root.SetArgs(append([]string{name}, rest...))

	if err := root.Execute(); err != nil {
		return report(ui, err)
	}
	return 0
}

// wantsHelp mirrors bin/landfall.mjs:217 exactly — `--help`/`-h` matched
// ANYWHERE in argv (never positionally, never stopping at `--`), plus `help`
// as the very first argument.
func wantsHelp(argv []string) bool {
	for _, a := range argv {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return len(argv) > 0 && argv[0] == "help"
}

var httpURL = regexp.MustCompile(`^https?://`)

// parseArgs is bin/landfall.mjs's parseArgs (lines 81-89).
//
// Returns the command name, the resolved link, and the remaining arguments the
// command itself sees.
func parseArgs(argv []string, getenv func(string) string) (cmd, link string, rest []string) {
	args := append([]string(nil), argv...)

	// `args.indexOf('--link')` + `splice(i, 2)`: the FIRST occurrence only, and
	// a trailing `--link` with no value removes just itself and leaves the link
	// undefined (JS reads args[i+1] as undefined there).
	linkGiven := false
	for i, a := range args {
		if a != "--link" {
			continue
		}
		if i+1 < len(args) {
			link = args[i+1]
			linkGiven = true
			args = append(args[:i], args[i+2:]...)
		} else {
			args = args[:i]
		}
		break
	}

	cmd = defaultCommand
	if len(args) > 0 && !httpURL.MatchString(args[0]) {
		cmd = args[0]
		args = args[1:]
	}
	// `if (!link && …)`: an explicitly EMPTY --link is falsy in JS, so a
	// positional URL still wins over it.
	if link == "" && len(args) > 0 && httpURL.MatchString(args[0]) {
		link = args[0]
		linkGiven = true
		args = args[1:]
	}
	// `link ?? process.env.LANDFALL_LINK` is nullish-coalescing, not `||`: a
	// link that was given as an empty string stays empty rather than falling
	// back to the environment.
	if !linkGiven {
		link = getenv("LANDFALL_LINK")
	}
	return cmd, link, args
}

// newRootCommand builds the whole command tree with the UI wired onto every
// command (T024/T025), and with every native Cobra help route closed off.
func newRootCommand(ui *UI, link string) *cobra.Command {
	root := &cobra.Command{
		Use:           "landfall",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(ui.Out)
	root.SetErr(ui.Err)

	// Three separate doors into Cobra's own help, all closed:
	//  - the auto-registered `help` subcommand (replaced by a hidden no-op, so
	//    the name `help` also stays UNregistered and keeps falling through to
	//    serve the way bin/landfall.mjs's dispatcher does);
	//  - the auto-added --help/-h flag on every command (pre-empted by
	//    registering our own hidden one, which InitDefaultHelpFlag then skips);
	//  - the help/usage renderers themselves, replaced by the one top-level
	//    text so no path can print a per-command variant.
	root.SetHelpCommand(&cobra.Command{Use: "no-help", Hidden: true})
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().BoolP("help", "h", false, "")
	_ = root.PersistentFlags().MarkHidden("help")
	root.SetHelpFunc(func(*cobra.Command, []string) { ui.Outf("%s\n", helpText) })
	root.SetUsageFunc(func(*cobra.Command) error { ui.Outf("%s\n", helpText); return nil })

	// A flag Cobra itself rejects is a usage error (exit 2), not a fatal one.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &exitError{code: 2, msg: err.Error()}
	})

	root.AddCommand(
		newLoginCommand(ui),
		newLogoutCommand(ui),
		newInstanceCommand(ui),
		newServeCommand(ui, link),
		newJoinCommand(ui, link),
		newNoteCommand(ui, link),
		newLeaveCommand(ui, link),
		newInstallCommand(ui),
		newUninstallCommand(ui),
		newHooksCommand(ui),
	)
	root.AddCommand(placeholderCommands(ui)...)
	return root
}

// newCommand is the single constructor every command file uses, so the UI
// wiring and the help suppression cannot be forgotten on a new command.
func newCommand(ui *UI, use string, run func(*cobra.Command, []string) error) *cobra.Command {
	c := &cobra.Command{
		Use:           use,
		Args:          cobra.ArbitraryArgs,
		RunE:          run,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	c.SetOut(ui.Out)
	c.SetErr(ui.Err)
	c.SetHelpFunc(func(*cobra.Command, []string) { ui.Outf("%s\n", helpText) })
	return c
}

func hasCommand(root *cobra.Command, name string) bool {
	for _, c := range root.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return true
		}
	}
	return false
}

// exitError carries an exit code out of a command.
//
// A command that has already written its own message (the `log(…);
// process.exitCode = 2; return` shape all over bin/landfall.mjs) returns one
// with an empty msg, and report stays silent rather than printing an empty
// "fatal:" line.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// usage returns the exit-2 error for a command that has already explained
// itself to the user.
func usage() error { return &exitError{code: 2} }

// report is bin/landfall.mjs's top-level catch (lines 492-500).
//
// The unreachable-instance class exits with the code carried ON THE ERROR
// (`e.exitCode ?? 2`), not a hardcoded 2, and its message is printed as-is:
// it already names the address and the fix, so prefixing it with "fatal:"
// would only bury that.
func report(ui *UI, err error) int {
	var exit *exitError
	if errors.As(err, &exit) {
		if exit.msg != "" {
			ui.Log("%s", exit.msg)
		}
		return exit.code
	}

	var unreachable *instance.UnreachableError
	if errors.As(err, &unreachable) {
		ui.Log("%s", unreachable.Msg)
		if unreachable.ExitCode != 0 {
			return unreachable.ExitCode
		}
		return instance.ExitUnreachable
	}
	// Any other error that identifies itself as the same class, so a future
	// package can mint one without this dispatcher knowing its type.
	var tagged interface{ Unreachable() bool }
	if errors.As(err, &tagged) && tagged.Unreachable() {
		ui.Log("%s", err.Error())
		return instance.ExitUnreachable
	}

	ui.Log("fatal: %s", err.Error())
	return 1
}

// Log writes one human-readable line to stderr, prefixed "[landfall] " — the
// Go equivalent of bin/landfall.mjs's `log()` (line 76). Every command logs
// through this; stdout belongs to machine-readable output only (FR-003).
func (u *UI) Log(format string, args ...any) {
	u.Printf("[landfall] "+strings.TrimSuffix(format, "\n")+"\n", args...)
}
