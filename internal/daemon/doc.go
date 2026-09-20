// Package daemon is the per-user room daemon (feature 20260922-local-room-daemon
// in landfalls-ai/landfall): one long-lived process per machine that owns every
// war room this user has joined — its realtime connection, presence, event ring
// and ranked frame — and knows each READER of a room by name with its own
// delivery cursor. `landfall serve` is a thin front end that attaches to it;
// the lifecycle hooks and `landfall status` read it as the person's own reader.
//
// WHY, IN ONE MEASUREMENT. On 2026-09-21 a real interactive Claude Code session
// spawned a helper that shared its `landfall serve` process; the helper's
// get_updates calls advanced the one cursor, and a teammate's direct question and
// a corroborated finding were never shown to the person, whose status line read
// zero and whose "anything new in the room?" was answered "nothing since seq 52"
// (landfall #2253). One process, one cursor, two consumers.
//
// READER KINDS AND WHO MAY MOVE WHICH CURSOR (data-model.md):
//
//	agent     — an MCP front end. Its cursor moves on its own tool-result flush
//	            (`delta`) or on a hook injection into its session.
//	terminal  — the person at their keyboard in one workspace. Its cursor moves
//	            ONLY on `consume` from a hook process in that workspace, i.e.
//	            only when something was actually put in front of the person.
//	panel     — the Edge desktop panel; moves when it acknowledges a render.
//	push      — a future host-side push consumer; moves on its own `consume`.
//
// No verb moves another reader's cursor. The untold set for a reader is derived
// (events past its cursor that are not room machinery, realtime.IsPlumbing) and
// never stored.
//
// WHAT THIS REVERSES, ON PURPOSE (research R10). internal/hooks/socket.go's own
// design notes say the hook socket is "deliberately NOT a new daemon", has "NO
// verb that writes to the room", and avoids a long-lived subscription. This
// package is that daemon; `share` feeds the spool (how the room is written to
// today, from the worker); `subscribe` is the one long-lived stream, for the
// panel and a push host. The per-pid hook socket keeps existing and keeps its
// guarantees: `serve` in daemon mode binds it as a proxy (research R11).
//
// LIFECYCLE. Spawned on demand by `serve` (detached, Setsid), single instance
// under a flock on <RuntimeDir>/daemon.lock, exits 60 s after its last reader
// detaches with an empty spool. When it cannot run, `serve` falls back to the
// per-process behaviour with one log line; nothing ever asks the person to
// start or manage it.
package daemon
