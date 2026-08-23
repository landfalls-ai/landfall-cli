package cli

// placeholders.go — TEMPORARY STUBS. Delete each one as its owning task lands.
//
// These exist only so the command tree in root.go is structurally complete:
// the bare-URL/default-command routing needs a `serve` to dispatch to, and
// `hasCommand` needs every real command name registered or an unrecognized
// name would silently fall through to serve (root.go's defaultCommand rule).
// None of them implements any behavior.
//
// Currently empty — every command has a real implementation wired into
// root.go's AddCommand. Kept as a file (rather than deleted) because
// placeholderCommands is still called from root.go and future work may need
// this seam again; if nothing has used it in a while, it's fine to delete
// both this file and its call site together.
//
// LANDED, no longer stubbed: serve (T027, internal/cli/serve.go — which is
// also the command the defaultCommand rule dispatches to, so it must stay
// registered for a bare `landfall`, a bare URL, and an unrecognized name to
// keep behaving as they do today).
//
// LANDED, no longer stubbed: install/uninstall (T051, internal/cli/{install,
// uninstall}.go) and hooks (T052, internal/cli/hooksinstall.go — which owns
// the `hooks` parent and its install/uninstall subcommands, and delegates the
// EVENT subcommands to the hooksEventDispatch seam declared there).
//
// LANDED, no longer stubbed: status (T033, internal/cli/status.go),
// connect (T054, internal/cli/connect_aws.go), remediation (T055,
// internal/cli/remediation.go). All three had real, tested implementations
// for a while before anyone actually wired their Cobra constructors into
// root.go's AddCommand — caught by a real end-to-end binary check
// (`landfall status`/`connect`/`remediation` all silently fell through to
// the placeholder, invisibly, because every existing test called the
// exported Run*/Parse* functions directly and never exercised the actual
// command tree). Worth remembering: `go test ./...` passing gives no signal
// about whether a command is actually reachable from the built binary —
// only running the real binary does.

import "github.com/spf13/cobra"

// placeholderCommands returns every not-yet-ported command as a stub that
// explains itself and exits 1.
func placeholderCommands(ui *UI) []*cobra.Command {
	names := []struct{ use, task string }{}
	cmds := make([]*cobra.Command, 0, len(names))
	for _, n := range names {
		use, task := n.use, n.task
		c := newCommand(ui, use, func(*cobra.Command, []string) error {
			ui.Log("`landfall %s` is not implemented in the Go build yet (%s).", use, task)
			return &exitError{code: 1}
		})
		// Flags are the owning task's business; parsing them here would reject
		// a flag the real command will accept.
		c.DisableFlagParsing = true
		cmds = append(cmds, c)
	}
	return cmds
}
