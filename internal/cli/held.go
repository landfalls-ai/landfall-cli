package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/spool"
	"github.com/spf13/cobra"
)

// The person's own commands for the working-directory hold (spec FR-008,
// contracts/person-commands.md). None is an MCP tool; an agent cannot call them.
//
//	landfall held [--room <incidentId>]        list what is held for this checkout's room
//	landfall held drop <id> [--room <id>]      discard one held share
//	landfall allow-cwd [--room <incidentId>]   let working-directory content into the room; releases every held share

// workspaceRoom finds the room this checkout reads, through the daemon's
// reader list. --room narrows when the checkout reads more than one.
func workspaceRoom(ws hooks.Workspace, incidentFilter string) (roomKey, incidentID string, err error) {
	res, err := daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{Op: "rooms"}, time.Second)
	if err != nil {
		return "", "", errors.New("no room daemon is running here; holds exist only in daemon mode")
	}
	key := hooks.WorkspaceKey(ws.Dir())
	var matches []daemon.RoomView
	for _, r := range res.Rooms {
		if incidentFilter != "" && !strings.HasPrefix(r.IncidentID, incidentFilter) {
			continue
		}
		for _, rd := range r.Readers {
			if rd.WorkspaceKey == key {
				matches = append(matches, r)
				break
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", "", errors.New("this checkout is not reading any room")
	case 1:
		return matches[0].RoomKey, matches[0].IncidentID, nil
	}
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m.IncidentID)
	}
	return "", "", fmt.Errorf("this checkout reads %d rooms; pick one with --room: %s", len(matches), strings.Join(ids, ", "))
}

func roomFlag(args []string) (string, []string) {
	rest := make([]string, 0, len(args))
	room := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--room" && i+1 < len(args) {
			room = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--room=") {
			room = strings.TrimPrefix(args[i], "--room=")
			continue
		}
		rest = append(rest, args[i])
	}
	return room, rest
}

func newHeldCommand(ui *UI) *cobra.Command {
	c := newCommand(ui, "held", func(_ *cobra.Command, args []string) error {
		ws := hooks.Workspace{}
		roomSel, rest := roomFlag(args)
		_, incidentID, err := workspaceRoom(ws, roomSel)
		if err != nil {
			ui.Outf("%s\n", err.Error())
			return nil
		}
		sp, err := spool.Open(ws.Getenv, hooks.WorkspaceKey(ws.Dir()))
		if err != nil {
			return err
		}
		if len(rest) >= 2 && rest[0] == "drop" {
			if err := sp.DropHeld(incidentID, rest[1]); err != nil {
				ui.Outf("%s\n", err.Error())
				return nil
			}
			ui.Outf("dropped %s\n", rest[1])
			return nil
		}
		held, err := sp.Held(incidentID)
		if err != nil {
			return err
		}
		if len(held) == 0 {
			ui.Outf("nothing held for incident %s\n", incidentID)
			return nil
		}
		for _, e := range held {
			matched := ""
			when := ""
			if e.Held != nil {
				matched = strings.Join(e.Held.Matched, ", ")
				when = e.Held.HeldAt.Local().Format("15:04:05")
			}
			ui.Outf("%s  %s  names: %s\n    %s\n", e.ID, when, matched, truncateLine(e.Text, 120))
		}
		ui.Outf("\n`landfall allow-cwd` lets working-directory content into incident %s and releases these; `landfall held drop <id>` discards one.\n", incidentID)
		return nil
	})
	c.DisableFlagParsing = true
	return c
}

func newAllowCwdCommand(ui *UI) *cobra.Command {
	c := newCommand(ui, "allow-cwd", func(_ *cobra.Command, args []string) error {
		ws := hooks.Workspace{}
		roomSel, _ := roomFlag(args)
		roomKey, incidentID, err := workspaceRoom(ws, roomSel)
		if err != nil {
			ui.Outf("%s\n", err.Error())
			return nil
		}
		if _, err := daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{Op: "allow-cwd", RoomKey: roomKey}, time.Second); err != nil {
			return err
		}
		sp, err := spool.Open(ws.Getenv, hooks.WorkspaceKey(ws.Dir()))
		if err != nil {
			return err
		}
		n, err := sp.ReleaseHeld(incidentID)
		if err != nil {
			return err
		}
		ui.Outf("released %d share(s); working-directory content may now leave this machine for incident %s\n", n, incidentID)
		return nil
	})
	c.DisableFlagParsing = true
	return c
}

func truncateLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
