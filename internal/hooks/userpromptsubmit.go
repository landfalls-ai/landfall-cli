// userpromptsubmit.go — where the idle digest is actually delivered (#227,
// operator decision 2026-08-05). A Go port of `src/hooks/user-prompt-submit.mjs`.
//
// `FileChanged` can watch the doorbell but its output is discarded;
// `UserPromptSubmit` can speak to the model but does not know the room changed.
// This is the half that speaks. It fires on every prompt (the event takes no
// matcher), so the common case — nothing owed — must cost nothing.
//
// The honest limit, recorded rather than glossed: there is no way to wake an
// idle Claude Code session with zero human action, because nothing server-side
// can push into one. So the earliest moment an idle session can act on room
// context is the human's next message — which is what this delivers, before the
// model generates anything.
//
// TWO SOURCES, AND THE ANSWER IS A UNION OF BOTH, NEVER A CHOICE BETWEEN THEM:
//  1. a live socket — authoritative, freshest, and the only one whose cursor can
//     actually be advanced;
//  2. the stage — for when `serve` exited between the wake and the prompt, so
//     the socket is gone but the context should still arrive.
//
// WHICH SOURCE SPEAKS IS DECIDED PER SESSION, ON ONE QUESTION: did that session
// answer the peek? A session that answers is telling us what it is still owed,
// and that answer is the truth even when it is "nothing". The stage cannot know
// what happened after it was written, and something usually did: the MCP tool
// wrapper's flushPending drains the very same queue in-band after EVERY tool
// call, with no knowledge of this stage. So "the socket owes nothing"
// overwhelmingly means "the agent already read it" — and re-delivering the stage
// there would hand the agent context it has already seen, captioned "while you
// were idle", breaking the repo's nothing-arrives-twice guarantee. The stage is
// entitled to speak for one kind of session only: one we could not reach at all.
//
// THAT IS A FILTER, NOT A PRECEDENCE ORDER — and treating it as an order is how
// this went wrong twice. One stage can cover several sessions which by delivery
// time need not share a fate: one answering, one dead. Weighing the stage as a
// whole ("is ANY owner unreachable? → deliver all of it") re-delivers the
// answering session's already-read events; ranking the live socket above it
// ("does ANYONE owe something live? → deliver only that") silently DROPS the
// dead session's events, which is worse, because the caller then deletes the
// stage that held the only surviving copy. Both questions are the coarse one. So
// the two sources are sliced by session and unioned: every reachable session
// speaks for itself, every unreachable one is spoken for by the stage, and the
// digest is rendered once over the union.
//
// The cursor advances only AFTER the digest has been emitted, which is where
// `never consume without delivering` is finally paid off.
package hooks

import (
	"context"
	"encoding/json"
)

func init() { RegisterHookHandler("user-prompt-submit", runUserPromptSubmitHandler) }

// PromptOutput is Claude Code's structured output for this event.
type PromptOutput struct {
	HookSpecificOutput PromptHookSpecificOutput `json:"hookSpecificOutput"`
}

// PromptHookSpecificOutput is the inner object. The event name is a literal the
// host matches on, not a value we get to choose.
type PromptHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// PromptPayload builds the object this event writes to stdout.
func PromptPayload(additionalContext string) PromptOutput {
	return PromptOutput{HookSpecificOutput: PromptHookSpecificOutput{
		HookEventName:     "UserPromptSubmit",
		AdditionalContext: additionalContext,
	}}
}

// Delivery is what ChooseDelivery decides.
type Delivery struct {
	Inject   bool
	Context  string
	Consumes []Consume
	// Source is which sources contributed: "socket", "stage", "socket+stage", or
	// "none".
	Source string
	// StaleStage is set when the stage contributed nothing, so its caller should
	// drop it rather than leave it to surface later. When the delivery DOES go
	// out, every staged entry has either been folded into it or been retired by
	// its own session's answer — which is what makes the caller's unconditional
	// unstage safe.
	StaleStage bool
}

// ChooseDelivery assembles what to deliver from both sources. Pure, so the rule
// is testable without a socket or a filesystem.
//
// peeks is the RAW peek answers, not a digest built from them, because the two
// facts this needs are per session and a rendered block has neither: WHICH
// sessions replied (the one thing separating "serve is gone, the stage is the
// only surviving copy" from "serve answered and owes nothing, so the stage is
// spent") and which of them owe anything. Handed only the assembled text, the
// best available question is "does anyone owe something?", and every wrong
// answer this function has given came from asking it.
//
// So the sources are UNIONED per session rather than ranked:
//
//   - a session that replied speaks for itself, and its reply — including
//     "nothing owed" — retires its own staged entry;
//   - a session that did not reply is spoken for by its staged entry, whether or
//     not any OTHER session has live content this turn. That last clause is the
//     fix for the third round of this bug: an early return on live content
//     skipped the stage entirely, and the caller then unstaged it, so a dead
//     session's only surviving events were deleted undelivered.
//
// The union is rendered by the same BuildInjection the live path uses, so one
// digest goes out with one honest total, and staged text can never diverge from
// live text.
//
// DO NOT "simplify" this into a ranking. It has been wrong three times.
func ChooseDelivery(peeks []SocketAnswer, staged *Stage) Delivery {
	reached := make(map[string]bool, len(peeks))
	for _, p := range peeks {
		reached[p.SocketPath] = true
	}

	var stagedPeeks []SocketAnswer
	if staged != nil {
		stagedPeeks = staged.Peeks
	}

	var fromSocket []SocketAnswer
	for _, p := range peeks {
		if OwesUpdates(p) {
			fromSocket = append(fromSocket, p)
		}
	}
	var fromStage []SocketAnswer
	for _, p := range stagedPeeks {
		if reached[p.SocketPath] {
			continue
		}
		if OwesUpdates(p) {
			fromStage = append(fromStage, p)
		}
	}

	union := make([]SocketAnswer, 0, len(fromSocket)+len(fromStage))
	union = append(union, fromSocket...)
	union = append(union, fromStage...)

	built := BuildInjection(union, 0)
	if !built.Inject {
		// Nothing owed anywhere. A stage that exists at this point speaks for
		// nobody — every one of its sessions either answered or turned out to owe
		// nothing — so say so, rather than leaving it to fire on a later prompt
		// once `serve` is gone and can no longer contradict it.
		return Delivery{Inject: false, Context: "", Consumes: nil, Source: "none", StaleStage: len(stagedPeeks) > 0}
	}

	source := "stage"
	if len(fromSocket) > 0 {
		source = "socket"
		if len(fromStage) > 0 {
			source = "socket+stage"
		}
	}
	return Delivery{
		Inject:     true,
		Context:    built.Context,
		Consumes:   built.Consumes,
		Source:     source,
		StaleStage: false,
	}
}

// UserPromptSubmitOptions is everything RunUserPromptSubmitHook needs.
type UserPromptSubmitOptions struct {
	Query   func(req SocketRequest) ([]SocketAnswer, error)
	Send    func(socketPath string, req SocketRequest) error
	Stage   func() *Stage
	Unstage func()
	Clear   func()
	// Emit writes the injection object to stdout. The text arrives WITHOUT a
	// trailing newline, matching the Node original's `emit`.
	Emit func(text string)
}

// UserPromptSubmitOutcome is what one delivery produces.
type UserPromptSubmitOutcome struct {
	ExitCode int
	Injected bool
	Source   string
	Context  string
}

// RunUserPromptSubmitHook runs the prompt-submit hook. Exit 0 always — this
// event CAN block a prompt, and deliberately never does: the human is
// mid-sentence, and refusing their message to show them a digest would be a
// worse interruption than the one this feature exists to prevent.
func RunUserPromptSubmitHook(opts UserPromptSubmitOptions) UserPromptSubmitOutcome {
	// The raw peeks, not a digest built from them: WHICH sessions replied is what
	// decides, session by session, whether the stage still speaks for any of them.
	var peeks []SocketAnswer
	if opts.Query != nil {
		answers, err := opts.Query(PeekRequest())
		if err == nil {
			peeks = answers
		}
		// asked and got nothing back — indistinguishable from serve being gone
	}

	var staged *Stage
	if opts.Stage != nil {
		staged = opts.Stage()
	}
	delivery := ChooseDelivery(peeks, staged)

	if !delivery.Inject {
		// A stage none of whose sessions still owes anything has been overtaken —
		// most often by flushPending handing those events to the agent in-band on
		// an ordinary tool call. Drop it now: left on disk it would inject on some
		// later prompt, once `serve` is gone and can no longer contradict it.
		if delivery.StaleStage && opts.Unstage != nil {
			opts.Unstage()
		}
		return UserPromptSubmitOutcome{ExitCode: 0, Injected: false, Source: "none", Context: ""}
	}

	if opts.Emit != nil {
		body, err := json.Marshal(PromptPayload(delivery.Context))
		if err == nil {
			opts.Emit(string(body))
		}
	}

	// Only now may cursors move. A consume that fails leaves the events queued —
	// the recoverable direction, and #225's Stop hook still covers them.
	consumedAll := true
	for _, c := range delivery.Consumes {
		if opts.Send == nil {
			consumedAll = false
			continue
		}
		if err := opts.Send(c.SocketPath, ConsumeRequest(c.UpTo)); err != nil {
			consumedAll = false
		}
	}

	// Everything the stage still spoke for is inside the block just emitted —
	// ChooseDelivery unions rather than ranks, so no staged entry can be left
	// undelivered behind a live one — and it went out whether or not a cursor
	// moved. So the stage always goes here: leaving it would re-inject the same
	// block on the next prompt.
	if opts.Unstage != nil {
		opts.Unstage()
	}
	if consumedAll && opts.Clear != nil {
		opts.Clear()
	}

	return UserPromptSubmitOutcome{
		ExitCode: 0,
		Injected: true,
		Source:   delivery.Source,
		Context:  delivery.Context,
	}
}

// runUserPromptSubmitHandler is the registered handler.
func runUserPromptSubmitHandler(_ context.Context, deps HookDeps) HookResult {
	ws := deps.Workspace
	out := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   workspaceQuery(ws),
		Send:    workspaceSend(),
		Stage:   func() *Stage { return ReadStage(ws) },
		Unstage: func() { ClearStage(ws) },
		Clear:   func() { ClearDoorbell(ws.Dir()) },
		// stdout, because the host PARSES this one — it is the whole point of
		// this event being the delivery half.
		Emit: func(text string) { deps.emit(text+"\n", ChannelStdout) },
	})
	result := "nothing-owed"
	if out.Injected {
		result = "injected:" + out.Source
	}
	return HookResult{ExitCode: out.ExitCode, Result: result}
}
