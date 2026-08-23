package cli

import (
	"fmt"
	"io"
	"os"
)

// stdout/stderr are package-level indirections (not direct os.Stdout/
// os.Stderr references) purely so a future test can swap them; production
// code always sees the real process streams.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// UI carries the two output channels every command must use instead of
// fmt.Println/fmt.Print directly, so the stdout/stderr split (FR-003) is
// enforced structurally rather than by convention across every command file.
// Out is stdout — reserved for machine-readable output: serve's MCP
// JSON-RPC, the hook exit-2/Cursor-JSON protocols, status's statusline, and
// install/uninstall report lines. Err is stderr — everything else,
// human-readable logging, prefixed "[landfall] " by convention.
//
// serve and every `hooks <event>` command MUST set Out = io.Discard, since
// their protocols reserve stdout for the wire format itself; any incidental
// write through Out in those commands is a protocol violation, not a log
// line, so Discard makes such a mistake inert rather than corrupting a
// stream a caller is parsing.
type UI struct {
	Out io.Writer
	Err io.Writer
}

// Printf writes a formatted, human-readable line to Err. This is the only
// sanctioned way a command logs — never fmt.Println/fmt.Print/log.Printf
// directly (enforced by convention here, and worth a golangci-lint
// forbidigo rule once every command is ported — see .golangci.yml).
func (u *UI) Printf(format string, args ...any) {
	fmt.Fprintf(u.Err, format, args...)
}

// Outf writes formatted machine-readable output to Out.
func (u *UI) Outf(format string, args ...any) {
	fmt.Fprintf(u.Out, format, args...)
}

// New returns a UI wired to the real stdout/stderr.
func New() *UI {
	return &UI{Out: stdout, Err: stderr}
}
