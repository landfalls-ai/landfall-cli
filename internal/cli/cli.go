// Package cli implements the Landfall CLI's command tree.
//
// Execute is the sole entrypoint main.go calls. It is a scaffold placeholder
// as of T002/T003 — the real Cobra root command, argv pre-processing, and the
// shared ui (stdout/stderr) abstraction land in T024/T025. Every command
// package added after that MUST route output through the ui abstraction
// rather than fmt.Println directly (FR-003 — see T024's rationale).
package cli

import "fmt"

// Execute runs the CLI with the given arguments (os.Args[1:]) and returns the
// process exit code: 0 success, 1 fatal error, 2 usage/config error or an
// unreachable instance — see contracts/cli-commands.md's Global conventions.
func Execute(args []string) int {
	fmt.Println("landfall: scaffold — command tree not yet implemented (T025)")
	return 0
}
