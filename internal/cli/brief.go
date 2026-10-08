package cli

// brief.go: `landfall brief --room <k>` (console spec §4.10, §9): the room's
// shared context as the PERSON reads it, for the console's Context tab and
// for the add that puts it in the person's agent's conversation.
//
// GET …/edge/context/frame through the room daemon's read op, with the room's
// own session and NO agentInstanceId. That is the difference from get_brief:
// get_brief is an agent receiving the room's context, so it names its
// instance, which records a context pull and advances that seat's cursor. A
// person reading their own room does neither.
//
// `text` is narrate.RenderFrame's output, byte for byte what get_brief
// returns, so what the console adds to a conversation is what an agent would
// have read. The rest is the same frame as data. Each open item that is a
// claim carries its state, folded from GET …/claims exactly as a person's
// latest contribution is (narrate.ClaimState); that read is best effort, and
// without it the items simply carry no state.

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/spf13/cobra"
)

// The read paths, relative to the incident. No query: never an agent's id.
const (
	briefFramePath  = "/edge/context/frame"
	briefClaimsPath = "/claims"
)

func newBriefCommand(ui *UI) *cobra.Command {
	return newReadCommand(ui, "brief", func(cmd *cobra.Command, ws hooks.Workspace, room string) map[string]any {
		return RunBrief(cmdContext(cmd), room, defaultReadDeps(ws))
	})
}

// RunBrief builds the brief answer:
//
//	{"ok":true,"text":"<RenderFrame>","asOfSeq":214,"freshnessMs":4000,
//	 "counts":{"established":2,"open":3,"participants":4,"scope":1,"focus":true},
//	 "established":[{seq,statement,by,admission?}],
//	 "open":[{seq,statement,by,state?,theory?}],
//	 "participants":[{humanActorId,displayName,edgeAgentLabel,active}],
//	 "scope":[{kind,label,by}],"focus":{text,by}|null,
//	 "instructionsVersion":3|null,
//	 "incident":{displayId?,title,severity?,status?},"listening":false?}
func RunBrief(ctx context.Context, roomSel string, d ReadDeps) map[string]any {
	room, why := pickRoom(d, roomSel)
	if why != "" {
		return failure(why)
	}
	raw, err := d.RoomRead(ctx, room.RoomKey, briefFramePath)
	if err != nil {
		return failure(roomRefusal(err, "the shared context"))
	}
	var frame client.ContextFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return failure("Landfall sent a shared context this CLI could not read.")
	}

	// Open items that are claims carry their state. Only asked when there is
	// an open item to fold it onto.
	states := map[int64]string{}
	if len(frame.Brief.Open) > 0 {
		if cr, err := d.RoomRead(ctx, room.RoomKey, briefClaimsPath); err == nil {
			var claims client.ClaimsProjection
			if json.Unmarshal(cr, &claims) == nil {
				for _, c := range claims.Claims {
					states[c.Seq] = narrate.ClaimState(c)
				}
			}
		}
	}

	established := make([]map[string]any, 0, len(frame.Brief.Established))
	for _, it := range frame.Brief.Established {
		row := briefItemRow(it)
		if a := narrate.AdmissionLine(it.Admission); a != "" {
			row["admission"] = narrate.Printable(a)
		}
		established = append(established, row)
	}
	// The working theory first, then the open items, as RenderFrame lists
	// them. A theory carries no state: it is the room's leading explanation,
	// not a claim waiting at the gate.
	open := make([]map[string]any, 0, len(frame.Brief.WorkingTheory)+len(frame.Brief.Open))
	for _, it := range frame.Brief.WorkingTheory {
		row := briefItemRow(it)
		row["theory"] = true
		open = append(open, row)
	}
	for _, it := range frame.Brief.Open {
		row := briefItemRow(it)
		if s := states[it.Seq]; s != "" {
			row["state"] = s
		}
		open = append(open, row)
	}

	participants := make([]map[string]any, 0, len(frame.Participants))
	for _, p := range frame.Participants {
		row := map[string]any{
			"humanActorId":   p.HumanActorID,
			"displayName":    oneLineText(narrate.Printable(p.DisplayName), 80),
			"edgeAgentLabel": oneLineText(narrate.Printable(p.EdgeAgentLabel), 80),
			"active":         nil,
		}
		if p.Active != nil {
			row["active"] = *p.Active
		}
		participants = append(participants, row)
	}

	scope := make([]map[string]any, 0, len(frame.Attachments))
	for _, a := range frame.Attachments {
		label := oneLineText(narrate.Printable(a.Label), 200)
		if label == "" {
			label = oneLineText(narrate.Printable(briefRefLabel(a.Ref)), 200)
		}
		scope = append(scope, map[string]any{
			"kind":  oneLineText(narrate.Printable(a.Kind), 40),
			"label": label,
			"by":    actorName(a.By),
		})
	}

	var focus any
	if frame.Focus != nil && strings.TrimSpace(frame.Focus.Focus) != "" {
		focus = map[string]any{
			"text": oneLineText(narrate.Printable(frame.Focus.Focus), 600),
			"by":   actorName(frame.Focus.By),
		}
	}
	var instructionsVersion any
	if frame.Instructions != nil && (strings.TrimSpace(frame.Instructions.Body) != "" || len(frame.Instructions.ComponentSections) > 0) {
		instructionsVersion = frame.Instructions.Version
	}
	var asOfSeq any
	if frame.AsOfSeq != nil {
		asOfSeq = *frame.AsOfSeq
	}
	var freshnessMs any
	if frame.FreshnessMs != nil {
		freshnessMs = int64(math.Max(0, math.Round(*frame.FreshnessMs)))
	}

	incident := map[string]any{"title": oneLineText(narrate.Printable(frame.Incident.Title), 200)}
	for key, v := range map[string]string{
		"displayId": frame.Incident.DisplayID,
		"severity":  frame.Incident.Severity,
		"status":    frame.Incident.Status,
	} {
		if v = oneLineText(narrate.Printable(v), 80); v != "" {
			incident[key] = v
		}
	}

	ans := map[string]any{
		"ok":          true,
		"text":        narrate.RenderFrame(&frame),
		"asOfSeq":     asOfSeq,
		"freshnessMs": freshnessMs,
		"counts": map[string]any{
			"established":  len(established),
			"open":         len(open),
			"participants": len(participants),
			"scope":        len(scope),
			"focus":        focus != nil,
		},
		"established":         established,
		"open":                open,
		"participants":        participants,
		"scope":               scope,
		"focus":               focus,
		"instructionsVersion": instructionsVersion,
		"incident":            incident,
	}
	// Only the explicit false is news, as RenderFrame says it: listening is
	// the default, and an absent field is an older server.
	if frame.Listening != nil && !*frame.Listening {
		ans["listening"] = false
	}
	return ans
}

// briefItemRow is one brief item as data. Statements are not clipped: the
// console clips as it draws, and the text the person adds is RenderFrame's.
func briefItemRow(it client.BriefItem) map[string]any {
	return map[string]any{
		"seq":       it.Seq,
		"statement": narrate.Printable(strings.TrimSpace(it.Statement)),
		"by":        oneLineText(narrate.Printable(it.By), 80),
	}
}

// actorName is who pinned or steered, by name, as RenderFrame says it.
func actorName(by client.ActorRef) string {
	switch {
	case by.DisplayName != "":
		return oneLineText(narrate.Printable(by.DisplayName), 80)
	case by.HumanActorID != "":
		return oneLineText(narrate.Printable(by.HumanActorID), 80)
	}
	return "someone"
}

// briefRefLabel is an unlabelled attachment's fallback: its ref, keys sorted.
func briefRefLabel(ref map[string]string) string {
	if len(ref) == 0 {
		return "(unlabelled)"
	}
	keys := make([]string, 0, len(ref))
	for k := range ref {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+ref[k])
	}
	return strings.Join(parts, " ")
}
