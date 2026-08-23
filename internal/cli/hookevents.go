package cli

// hookevents.go — `landfall hooks stop | file-changed | user-prompt-submit |
// pre-tool-use` and `landfall hooks policy`, a port of bin/landfall.mjs:367-400's
// `hooks` dispatcher branches.
//
// These are what a REGISTERED HOOK ENTRY itself runs — not commands a human
// types — so they are hidden from `landfall --help` (contracts/cli-commands.md;
// the top-level help text lists only `hooks install|uninstall|policy`). They
// attach through the hooksEventDispatch seam newHooksCommand declares in
// hooksinstall.go, which is exactly what that seam's own comment asks for: "the
// owning track should assign this (from its own file's init) rather than editing
// the dispatch". Nothing in root.go or placeholders.go needs to change — `hooks`
// is already a registered, flag-parsing-disabled command there.
//
// THE THREE CHANNELS, and why this file writes them where it does:
//
//	stdin   read ONCE, here, with one bounded deadline (hooks.ReadHookInput),
//	        and handed to the handler through HookDeps.Input. A handler that
//	        read stdin itself would race this read for the same bytes.
//	stdout  the WIRE. Cursor JSON.parse()s it; UserPromptSubmit's injection
//	        object is parsed by Claude Code. It is written through the package's
//	        own `stdout` writer rather than ui.Out, because ui.Out is set to
//	        io.Discard for these commands (see ui.go: "any incidental write
//	        through Out in those commands is a protocol violation") — the wire
//	        format is not an incidental write and must not share that door.
//	stderr  everything for the model or the human. A handler's PROTOCOL text
//	        (the Stop refusal, the file-changed nudge) goes out raw, byte for
//	        byte as the Node original writes it; a handler's LOG lines go
//	        through ui.Log and carry the "[landfall] " prefix.
//
// The exit code is the whole answer for the exit2 hosts: 0 allows the turn, 2
// blocks it with stderr fed back to the model.

import (
	"context"
	"io"
	"os"

	"github.com/landfalls-ai/landfall-cli/internal/hooks"
)

func init() { hooksEventDispatch = dispatchHookSubcommand }

// hookEventStdin is where a hook event reads its payload from. A package-level
// indirection purely so a test can hand it a payload without a real pipe;
// production always sees the real stream.
var hookEventStdin io.Reader = os.Stdin

// dispatchHookSubcommand handles every `hooks` subcommand that is not
// install/uninstall. Returns handled=false for anything it does not own, so the
// usage line still answers an unrecognized subcommand.
func dispatchHookSubcommand(ui *UI, sub string, argv []string) (bool, error) {
	if sub == "policy" {
		return true, runHooksPolicyCommand(ui, argv)
	}
	if hooks.FindHookEvent(sub) == nil {
		return false, nil
	}
	return true, runHookEventCommand(context.Background(), ui, sub, argv)
}

// hookEmitter writes one protocol output on the channel the protocol names.
func hookEmitter() func(text, channel string) {
	return func(text, channel string) {
		if channel == hooks.ChannelStdout {
			_, _ = io.WriteString(stdout, text)
			return
		}
		// Raw, unprefixed: this is the handler's own text, and the Stop hook's
		// refusal is fed verbatim back to the model.
		_, _ = io.WriteString(stderr, text)
	}
}

// runHookEventCommand is one `landfall hooks <event> [--host <id>]` invocation.
func runHookEventCommand(ctx context.Context, ui *UI, eventID string, argv []string) error {
	f := parseHookFlags(argv)

	// stdout belongs to the protocol from here on. Anything a command
	// accidentally routes through ui.Out would corrupt a stream the host is
	// parsing, so make such a mistake inert.
	ui.Out = io.Discard

	// The ONE bounded stdin read, shared by every handler.
	input := hooks.ReadHookInput(hookEventStdin, 0)

	emit := hookEmitter()
	res := hooks.RunHookEvent(ctx, eventID, hooks.HookDeps{
		Input: input,
		// `--host` is on the command line because WE wrote that command line;
		// inferring the output contract from an unfamiliar payload would be a
		// guess (#228).
		Host: f.host,
		Log:  ui.Log,
		Emit: emit,
	})

	// A handler emits its own output as it decides, in the order the contract
	// requires. These two carry only what a handler-less event answers with —
	// see hooks.renderNoOpFor.
	emit(res.Stdout, hooks.ChannelStdout)
	emit(res.Stderr, hooks.ChannelStderr)
	if res.Error != "" {
		ui.Log("%s", res.Error)
	}
	if res.ExitCode != 0 {
		// The message (if any) has already been written on the channel the host
		// reads, so this carries the code alone.
		return &exitError{code: res.ExitCode}
	}
	return nil
}
