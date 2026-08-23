package cli

// placeholders.go — TEMPORARY STUBS. Delete each one as its owning task lands.
//
// These exist only so the command tree in root.go is structurally complete:
// the bare-URL/default-command routing needs a `serve` to dispatch to, and
// `hasCommand` needs every real command name registered or an unrecognized
// name would silently fall through to serve (root.go's defaultCommand rule).
// None of them implements any behavior.
//
//	serve                 → tasks.md T027  (internal/cli/serve.go)
//	status                → tasks.md T033  (internal/cli/status.go)
//	install / uninstall   → tasks.md T049-T050
//	connect               → tasks.md T053
//	remediation           → tasks.md T055
//	hooks                 → tasks.md T041-T046
//
// Replacing one means deleting its entry from placeholderCommands below and
// adding a real constructor in its own file — nothing else in root.go needs to
// change (root.go's AddCommand call takes whatever placeholderCommands still
// returns, plus each real constructor).
//
// Two of them are already half-built by their own tracks and need only the
// Cobra wiring: connect_aws.go exports ParseConnectAWSFlags/RunConnectAWS and
// remediation.go exports ParseRemediationApproveFlags/RunRemediationApprove,
// both returning the exit code their command should produce.

import "github.com/spf13/cobra"

// placeholderCommands returns every not-yet-ported command as a stub that
// explains itself and exits 1.
func placeholderCommands(ui *UI) []*cobra.Command {
	names := []struct{ use, task string }{
		{"serve", "T027"},
		{"status", "T033"},
		{"install", "T049"},
		{"uninstall", "T050"},
		{"connect", "T053"},
		{"remediation", "T055"},
		{"hooks", "T041"},
	}
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
