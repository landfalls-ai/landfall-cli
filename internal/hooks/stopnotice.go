// stopnotice.go — the Stop hook's DEFAULT behaviour since 2026-09-28: tell the
// person, never the agent, and never take a turn they did not ask for.
//
// WHY THE DEFAULT CHANGED. Until v0.8.12 the Stop hook refused the agent's
// conclusion whenever the room held news ("Do not conclude yet — N update(s)
// ..."). Claude Code and Codex answer a refusal by running ANOTHER agent turn,
// which the human at the keyboard did not ask for: they had just watched the
// agent finish, and Landfall started it again. The operator's rule for the
// product is that Landfall must never interrupt a human's interaction with
// their own agent — the human decides what the agent investigates next. So the
// hook now only informs:
//
//   - the person sees ONE line from their host ("Landfall: 2 updates in the
//     room (1 finding from Maya · Codex). They reach your agent with
//     your next message.");
//   - nothing is consumed, so the updates are still owed when the human next
//     speaks, and the UserPromptSubmit hook hands them to the agent as
//     additionalContext on that message (userpromptsubmit.go) — before the
//     model generates anything, and without a turn of its own;
//   - the agent's own next room tool call also flushes them in-band, as it
//     always has.
//
// THE OLD REFUSAL IS STILL AVAILABLE, BUT ONLY WHEN ASKED FOR: LANDFALL_STOP_HOOK
// =block in the host's environment, or `--block` on the registered command
// (`landfall hooks stop --block`). Nothing else turns it on — an unknown value
// of the variable reads as the default, because a typo must never be what
// starts interrupting somebody.
//
// HOW EACH HOST HEARS THE NOTICE:
//
//	Claude Code  exit 0 + `{"systemMessage": "..."}` on stdout. Claude Code
//	             shows systemMessage to the user as a warning line and, with no
//	             `decision`, lets the agent stop.
//	Codex        the same object. Codex 0.146's `stop.command.output` schema
//	             carries `systemMessage` (additionalProperties false, so nothing
//	             else may ride along), and exit 0 without `decision` is an allow.
//	Cursor       `{}`. Cursor's `stop` output vocabulary is `followup_message`
//	             only, and a followup IS a new agent turn — the exact thing this
//	             default exists to avoid — so under inform there is nothing it
//	             can be told. Its agent still gets the news in-band.
package hooks

import (
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// StopModeEnv is the environment variable that opts a host back into the
// blocking Stop hook.
const StopModeEnv = "LANDFALL_STOP_HOOK"

// The two Stop hook modes.
const (
	// StopModeInform is the default: a one-line notice to the person, no
	// block, nothing consumed.
	StopModeInform = "inform"
	// StopModeBlock is the pre-2026-09-28 behaviour, opt-in only: refuse the
	// conclusion and consume what was reported.
	StopModeBlock = "block"
)

// ResolveStopMode is which mode one Stop invocation runs in. `--block` on the
// command line wins; then LANDFALL_STOP_HOOK=block; anything else, including
// an unset or misspelled variable, is inform.
func ResolveStopMode(blockFlag bool, getenv func(string) string) string {
	if blockFlag {
		return StopModeBlock
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.EqualFold(strings.TrimSpace(getenv(StopModeEnv)), StopModeBlock) {
		return StopModeBlock
	}
	return StopModeInform
}

// stopNoticeMax bounds the notice. It is one line in somebody's terminal, not a
// digest; the digest itself reaches the agent on the next message.
const stopNoticeMax = 300

// stopNoticeGroups is how many "N <kind> from <who>" groups are spelled out
// before the rest become "+N more".
const stopNoticeGroups = 3

// BuildStopNotice is the one line the person sees when their agent stops while
// the room holds news for it, or "" when there is nothing to say. Pure.
func BuildStopNotice(peeks []SocketAnswer) string {
	total := 0
	var lines []string
	seen := map[string]bool{}
	for _, p := range peeks {
		if !OwesUpdates(p) {
			continue
		}
		total += p.Response.CountOr(0) + p.Response.Dropped
		for _, l := range p.Response.Digest {
			if seen[l] { // two sockets for one reader, see stop.go
				continue
			}
			seen[l] = true
			lines = append(lines, l)
		}
	}
	blockers := mergeBlockers(peeks)

	var parts []string
	if total > 0 {
		part := strconv.Itoa(total) + " " + plural(total, "update", "updates") + " in the room"
		if groups := describeDigestGroups(lines); groups != "" {
			part += " (" + groups + ")"
		}
		parts = append(parts, part)
	}
	if q := len(blockers.Quarantined); q > 0 {
		parts = append(parts, "the room quarantined "+strconv.Itoa(q)+" "+
			plural(q, "item", "items")+" your agent relied on")
	}
	if c := len(blockers.Contradictions); c > 0 {
		parts = append(parts, strconv.Itoa(c)+" "+plural(c, "claim contradicting", "claims contradicting")+
			" the admitted record "+plural(c, "awaits", "await")+" your agent's position")
	}
	if len(parts) == 0 {
		return ""
	}

	notice := "Landfall: " + strings.Join(parts, "; ") + "."
	if total > 0 {
		notice += " " + plural(total, "It reaches", "They reach") + " your agent with your next message."
	}
	notice = strings.Join(strings.Fields(notice), " ") // one line, whatever a display name held
	if utf8.RuneCountInString(notice) > stopNoticeMax {
		notice = string([]rune(notice)[:stopNoticeMax-1]) + "…"
	}
	return notice
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// digestGroup is one "N <kind> from <who>" entry, in first-seen order.
type digestGroup struct {
	kind  digestKind
	who   string
	count int
}

// describeDigestGroups turns digest lines into "1 finding from Maya ·
// Codex, 2 messages from Jhonny", capped at stopNoticeGroups groups.
func describeDigestGroups(lines []string) string {
	var groups []*digestGroup
	index := map[string]*digestGroup{}
	for _, l := range lines {
		typ, who := ParseDigestLine(l)
		kind := kindOf(typ)
		key := kind.one + "\x00" + who
		g, ok := index[key]
		if !ok {
			g = &digestGroup{kind: kind, who: who}
			index[key] = g
			groups = append(groups, g)
		}
		g.count++
	}
	shown := groups
	if len(shown) > stopNoticeGroups {
		shown = shown[:stopNoticeGroups]
	}
	out := make([]string, 0, len(shown)+1)
	for _, g := range shown {
		s := strconv.Itoa(g.count) + " " + plural(g.count, g.kind.one, g.kind.many)
		if g.who != "" {
			s += " from " + g.who
		}
		out = append(out, s)
	}
	if rest := len(groups) - len(shown); rest > 0 {
		out = append(out, "+"+strconv.Itoa(rest)+" more")
	}
	return strings.Join(out, ", ")
}

// ParseDigestLine reads back the event type and the attribution from one line
// narrate.FormatEventLine rendered: `#<seq> <type> [<who>] — <text>`. Either
// may come back "" (a line in some other shape reads as an unattributed
// update rather than failing). The daemon and the per-pid socket both render
// their digests with FormatEventLine, and stopnotice_test.go round-trips it, so
// the two cannot drift apart unnoticed.
func ParseDigestLine(line string) (eventType, who string) {
	if !strings.HasPrefix(line, "#") {
		return "", ""
	}
	sp := strings.IndexByte(line, ' ')
	if sp < 0 {
		return "", ""
	}
	rest := line[sp+1:]
	end := strings.IndexByte(rest, ' ')
	if end < 0 {
		return rest, ""
	}
	eventType, rest = rest[:end], rest[end:]
	if !strings.HasPrefix(rest, " [") {
		return eventType, ""
	}
	close := strings.Index(rest, "]")
	if close < 0 {
		return eventType, ""
	}
	return eventType, strings.TrimSpace(rest[2:close])
}

// digestKind is how one event type reads in a sentence.
type digestKind struct{ one, many string }

var genericKind = digestKind{"update", "updates"}

var digestKinds = map[string]digestKind{
	"edge.finding":            {"finding", "findings"},
	"agent.finding":           {"finding", "findings"},
	"claim.staged":            {"staged claim", "staged claims"},
	"claim.admitted":          {"admitted claim", "admitted claims"},
	"claim.demoted":           {"demoted claim", "demoted claims"},
	"claim.corroborated":      {"corroboration", "corroborations"},
	"claim.contested":         {"contested claim", "contested claims"},
	"agent.hypothesis.raised": {"hypothesis", "hypotheses"},
	"artifact.shared":         {"artifact", "artifacts"},
	"edge.capture.summarized": {"capture", "captures"},
	"chat.message":            {"message", "messages"},
	"context.attached":        {"pinned context", "pinned contexts"},
	"context.detached":        {"unpinned context", "unpinned contexts"},
	"context.flagged":         {"flag", "flags"},
	"steer.recorded":          {"steering note", "steering notes"},
	"memory.proposed":         {"memory proposal", "memory proposals"},
	"memory.proposal.decided": {"memory decision", "memory decisions"},
	"related.suggested":       {"related incident", "related incidents"},
	"incident.triggered":      {"trigger", "triggers"},
	"status.changed":          {"status change", "status changes"},
	"severity.changed":        {"severity change", "severity changes"},
	"voice.segment":           {"voice note", "voice notes"},
}

func kindOf(eventType string) digestKind {
	if k, ok := digestKinds[eventType]; ok {
		return k
	}
	switch {
	case strings.HasPrefix(eventType, "line."):
		return digestKind{"line-of-inquiry change", "line-of-inquiry changes"}
	case strings.HasPrefix(eventType, "proposal."):
		return digestKind{"proposal update", "proposal updates"}
	}
	return genericKind
}
