package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/session"
	"github.com/spf13/cobra"
)

// newDaemonCommand is `landfall daemon` (run in the foreground; `serve` spawns
// it detached) and `landfall daemon stop`.
func newDaemonCommand(ui *UI) *cobra.Command {
	c := newCommand(ui, "daemon", func(cmd *cobra.Command, args []string) error {
		ws := hooks.Workspace{}
		if len(args) > 0 && args[0] == "stop" {
			if _, err := daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{Op: "stop"}, time.Second); err != nil {
				ui.Outf("no room daemon is running\n")
				return nil
			}
			ui.Outf("stopped\n")
			return nil
		}
		ctx, stop := signal.NotifyContext(cmdContext(cmd), os.Interrupt, syscall.SIGTERM)
		defer stop()
		d := daemon.New(daemonOptions(ui, ws))
		err := d.Run(ctx)
		if err == daemon.ErrAlreadyRunning {
			ui.Log("a room daemon is already running")
			return nil
		}
		return err
	})
	c.DisableFlagParsing = true
	return c
}

// daemonOptions is the production wiring: the real HTTP client, the real
// Socket.IO watcher, the OS notifier for addressed chat, and the spool as the
// hold store (Phase US3 supplies it; nil until then).
func daemonOptions(ui *UI, ws hooks.Workspace) daemon.Options {
	watcher := realtimeWatcher{ui: ui}
	notify := newNotifier()
	return daemon.Options{
		Workspace: ws,
		Log:       func(msg string) { ui.Log("%s", msg) },
		Deps: daemon.Deps{
			NewClient: func(cfg client.Config) session.EdgeClient { return client.New(cfg, nil) },
			Watch: func(ctx context.Context, cfg client.Config, own func() string, onEvent func(client.Event)) func() {
				return watcher.Watch(ctx, cfg, own, onEvent)
			},
			Heartbeat: heartbeatInterval,
			OnEvent: func(_ *daemon.Room, evt client.Event) {
				notify.Maybe(evt)
			},
		},
	}
}

// newRoomsCommand is `landfall rooms [--json]`: which rooms this machine has
// open and who is reading each (spec FR-015).
func newRoomsCommand(ui *UI) *cobra.Command {
	c := newCommand(ui, "rooms", func(_ *cobra.Command, args []string) error {
		res, err := daemon.Send(hooks.DaemonSocketPath(hooks.Workspace{}), daemon.Request{Op: "rooms"}, time.Second)
		if err != nil {
			ui.Outf("no room daemon is running; nothing is open on this machine\n")
			return nil
		}
		if len(args) > 0 && args[0] == "--json" {
			return writeJSON(ui, res.Rooms)
		}
		if len(res.Rooms) == 0 {
			ui.Outf("no rooms open\n")
			return nil
		}
		for _, r := range res.Rooms {
			ui.Outf("%s/%s  %s\n", r.Slug, r.IncidentID, r.Connection)
			for _, rd := range r.Readers {
				state := "detached"
				if rd.Connected {
					state = "connected"
				}
				ui.Outf("  %-40s %-9s %-12s cursor %-6d %s\n", rd.Name, rd.Kind, rd.Host, rd.Cursor, state)
			}
		}
		return nil
	})
	c.DisableFlagParsing = true
	return c
}

func writeJSON(ui *UI, v any) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = ui.Out.Write(append(body, '\n'))
	return err
}
