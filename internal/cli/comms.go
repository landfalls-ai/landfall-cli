package cli

// comms.go — `landfall comms --room <k>` (contract §8): the room's
// stakeholder updates and whether each was approved and sent. Read only: an
// update is approved and sent in the web app by a person, never from here
// (spec NFR-2).
//
// GET …/comms/messages with the room's own session through the daemon. The
// server folds each message from its comms.* events (`@landfall/comms`
// reduceMessage): {messageId, channel, body, kind, status, approvedByName?,
// sent, simulated?}. It carries no time of its own, so `at` is left out, and
// the approver is named only by the display name the approval recorded,
// never by id.

import (
	"context"
	"encoding/json"

	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/spf13/cobra"
)

const commsTextMax = 600

func newCommsCommand(ui *UI) *cobra.Command {
	return newReadCommand(ui, "comms", func(cmd *cobra.Command, ws hooks.Workspace, room string) map[string]any {
		return RunComms(cmdContext(cmd), room, defaultReadDeps(ws))
	})
}

// commsState maps the server's status to the contract's words.
func commsState(status string) string {
	switch status {
	case "drafted":
		return "draft"
	case "approved", "sent", "failed":
		return status
	}
	return "draft"
}

// RunComms builds the comms answer.
func RunComms(ctx context.Context, roomSel string, d ReadDeps) map[string]any {
	room, why := pickRoom(d, roomSel)
	if why != "" {
		return failure(why)
	}
	raw, err := d.RoomRead(ctx, room.RoomKey, "/comms/messages")
	if err != nil {
		return failure(roomRefusal(err, "the stakeholder updates"))
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return failure("Landfall sent stakeholder updates this CLI could not read.")
	}
	messages := make([]map[string]any, 0, len(body.Items))
	// Newest first: the server lists them in the order they were drafted.
	for i := len(body.Items) - 1; i >= 0; i-- {
		m := body.Items[i]
		out := map[string]any{
			"id":      jStr(m, "messageId"),
			"state":   commsState(jStr(m, "status")),
			"channel": jStr(m, "channel"),
			"kind":    jStr(m, "kind"),
			"text":    oneLineText(jStr(m, "body"), commsTextMax),
		}
		if name := jStr(m, "approvedByName"); name != "" {
			out["approvedBy"] = oneLineText(name, 60)
		}
		if sim, ok := m["simulated"].(bool); ok && sim {
			out["simulated"] = true
		}
		messages = append(messages, out)
	}
	return map[string]any{"ok": true, "messages": messages}
}
