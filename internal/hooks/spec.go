// spec.go — the canonical set of lifecycle hooks `landfall hooks install`
// registers, in ONE place, so the installer, the uninstaller and the hook
// handlers themselves can never disagree about what was written. A Go port of
// `src/hooks/spec.mjs`.
//
// Four events (`EDGE_PUSH_ARCHITECTURE.md` §5/§10.1, §6):
//
//	stop          — refuse a silent conclusion while room events newer than the
//	                session's last consumed seq exist (#225)
//	file-changed  — wake on the doorbell marker and STAGE a digest of room
//	                events for an idle session (#227). Claude Code only: it is
//	                the one host with a file-watch hook. Cannot deliver — the
//	                host discards this event's output.
//	user-prompt-submit
//	              — DELIVER that staged digest as `additionalContext` on the
//	                session's next prompt (#227). The half that can speak.
//	pre-tool-use  — match a command about to run against the local prod
//	                allow-list and, on the engineer's confirmation, declare the
//	                classified intent (#233, story #192). Not registered for
//	                Cursor: mapping our hooks onto its event vocabulary and
//	                `{continue:false}` semantics is #228's job, and an entry
//	                nothing yet honors is worse than no entry.
//
// The command every entry runs starts `landfall hooks <id>`. That prefix is also
// how uninstall recognizes a landfall-authored entry it must NOT delete because
// a human edited it since — see IsLandfallCommand.
package hooks

import "strings"

// HookEvent is one registerable lifecycle event.
type HookEvent struct {
	// ID is the `landfall hooks <id>` sub-command.
	ID string
	// Hosts are the host ids this event is registered for.
	Hosts []string
	// Purpose is the one-line reason it exists.
	Purpose string
	// Matcher is the host-side matcher the entry registers with, or "" when it
	// needs none.
	Matcher string
}

// HookEvents is the canonical registry, in registration order.
var HookEvents = []HookEvent{
	{
		ID:      "stop",
		Hosts:   []string{"claude-code", "codex", "cursor"},
		Purpose: "block a conclusion while unconsumed room context exists",
	},
	{
		ID:      "file-changed",
		Hosts:   []string{"claude-code"},
		Purpose: "inject room events into an idle session",
		// NOT a glob, and not optional. Claude Code's `FileChanged` matcher is a
		// list of LITERAL FILENAMES separated by `|`, watched in any directory
		// under the cwd — "glob patterns and path prefixes are not supported",
		// and "if the matcher is empty or omitted, no files are watched and the
		// hook never fires". So #222's matcher-less registration never fired at
		// all, and a path glob here would not have either. The bare filename is
		// the only form that works; `.landfall/` is where we put it, but the
		// watch is by name alone.
		Matcher: DoorbellFile,
	},
	{
		ID:      "user-prompt-submit",
		Hosts:   []string{"claude-code"},
		Purpose: "deliver the staged room digest on the session's next prompt",
		// No matcher, and none is possible: this event "does not support
		// matchers and always fires on every occurrence". That is exactly why it
		// is the delivery half — it is the one event guaranteed to run when an
		// idle session resumes, and unlike `file-changed` its output is not
		// discarded.
	},
	{
		ID:      "pre-tool-use",
		Hosts:   []string{"claude-code", "codex"},
		Purpose: "declare a confirmed production investigation to the war room",
		// The one event where the matcher is load-bearing rather than
		// meaningless. #233 requires that a non-matching command add zero
		// overhead, and the cheapest way to add none at all is for the host
		// never to spawn us: scoping the registration to the shell tool means an
		// Edit, a Read or a web fetch costs nothing, not even a process.
		Matcher: "Bash",
	},
}

// HookEventIDs is every registered event id, in HookEvents order.
func HookEventIDs() []string {
	ids := make([]string, 0, len(HookEvents))
	for _, e := range HookEvents {
		ids = append(ids, e.ID)
	}
	return ids
}

// FindHookEvent returns the event with this id, or nil.
func FindHookEvent(eventID string) *HookEvent {
	for i := range HookEvents {
		if HookEvents[i].ID == eventID {
			return &HookEvents[i]
		}
	}
	return nil
}

// hookCommandNeedsHostFlag reports whether a host needs `--host <id>` appended
// to be told which output contract to answer on.
//
// This is `protocolForHost(hostId) !== EXIT2` from `src/hooks/protocol.mjs`,
// and it is now literally that call: protocol.go (T045) owns the one host
// table. The temporary local duplicate that stood here until it landed is gone
// on purpose — the split must have exactly one answer, or a registered command
// string and the handler that answers it will drift.
func hookCommandNeedsHostFlag(hostID string) bool {
	return ProtocolForHost(hostID) != EXIT2
}

// HookCommand is the exact shell command a registered hook entry runs.
//
// A host whose block semantics are the exit-2 convention gets the bare command;
// a host that needs another output shape gets `--host <id>` appended so the
// handler is TOLD which contract to answer on rather than inferring it from a
// payload we did not write (#228).
//
// Passing the flag only where it changes behaviour is deliberate: the two hosts
// already registered with the bare form keep byte-identical entries, so an
// upgrade churns nobody's config into a conflict.
func HookCommand(eventID, hostID string) string {
	base := "landfall hooks " + eventID
	if hookCommandNeedsHostFlag(hostID) {
		return base + " --host " + hostID
	}
	return base
}

// MatcherFor is the matcher an event registers with, or "" when it needs none.
func MatcherFor(eventID string) string {
	if e := FindHookEvent(eventID); e != nil {
		return e.Matcher
	}
	return ""
}

// SupersededHookCommands is the exact commands a PREVIOUS version of this
// installer wrote for the same (event, host) — entries that are ours and
// unmodified, just stale.
//
// This is what keeps an upgrade from stranding people. Without it, changing a
// registered command turns every existing install into a `conflict`: never
// overwritten (correct — we cannot tell "stale" from "hand-edited" without this
// list) and never removable either, since `hooks uninstall` only deletes a
// byte-exact match of the CURRENT command. Enumerating the old forms here makes
// "stale" a fourth, distinguishable state that install replaces in place and
// uninstall cleans up, while an entry matching neither the current nor any past
// form stays a conflict — a human edited it, and it is not ours to touch.
//
// AUDIT NOTE (inventory.md §E.7 item 7, T038). The single entry below is
// Cursor's bare `landfall hooks stop` as shipped in v0.2.0, before the `--host`
// flag existed; the handler answered it with exit 2 + stderr, which Cursor does
// not read. It is carried forward verbatim because the cost of a stale-but-ours
// entry that nothing can clean up is much higher than the cost of one extra
// string comparison, and because a v0.2.0 install is cheap to still support and
// impossible to detect after the fact. Whether any such install remains in the
// field is an OPERATOR question, not one this port can answer — see the task
// report. If the Go binary ever changes a registered command string, THAT
// new-versus-old pair becomes the entry that matters and belongs here too.
func SupersededHookCommands(eventID, hostID string) []string {
	if hostID == "cursor" && eventID == "stop" {
		return []string{"landfall hooks stop"}
	}
	return nil
}

// IsLandfallCommand is true for any command string this installer could
// plausibly have written — including one a human has since edited. Used to
// distinguish "landfall's entry, modified" (never overwritten, never deleted,
// never duplicated) from "somebody else's hook" (invisible to us) and from a
// byte-exact match.
func IsLandfallCommand(command string) bool {
	return strings.HasPrefix(strings.TrimSpace(command), "landfall hooks")
}

// EventsForHost is the event ids registered for one host id, in HookEvents
// order.
func EventsForHost(hostID string) []string {
	ids := make([]string, 0, len(HookEvents))
	for _, e := range HookEvents {
		for _, h := range e.Hosts {
			if h == hostID {
				ids = append(ids, e.ID)
				break
			}
		}
	}
	return ids
}
