package cli

// join.go — `landfall join [URL]`, a port of bin/landfall.mjs:327-339.
//
// Presence-only keep-alive: join the incident, heartbeat until the terminal is
// interrupted, then leave. No MCP server, no tools — this is the command for a
// teammate who wants the room to see them without wiring up an agent.
//
// It talks to internal/client directly (Join / Heartbeat / Leave), NOT through
// the MCP tool wrappers, exactly as the Node command body does: the bridge
// tools exist for an agent calling in over stdio, and this command has no
// agent.

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/spf13/cobra"
)

// heartbeatInterval is bin/landfall.mjs's HEARTBEAT_MS (line 78).
const heartbeatInterval = 15 * time.Second

func newJoinCommand(ui *UI, link string) *cobra.Command {
	c := newCommand(ui, "join", func(cmd *cobra.Command, _ []string) error {
		return runJoin(cmdContext(cmd), ui, link)
	})
	// `--link` has already been spliced out of argv by parseArgs, and a
	// positional URL has already become the link, so there is nothing here for
	// Cobra to parse — and a value that merely LOOKS like a flag must not be
	// rejected.
	c.DisableFlagParsing = true
	return c
}

func runJoin(ctx context.Context, ui *UI, link string) error {
	cfg, err := resolveConfig(ctx, ui, link)
	if err != nil {
		return err
	}
	if cfg == nil {
		ui.Log(`pass an agent share link: landfall join "<url>" (or set LANDFALL_* env).`)
		return usage()
	}

	c := client.New(*cfg, nil)
	if _, err := c.Join(ctx); err != nil {
		return err
	}
	// Literal quotes rather than %q: %q would escape a non-ASCII agent label,
	// and this line is meant to read back identically to the Node original's.
	ui.Log(`joined incident %s as "%s" (instance %s).`, cfg.IncidentID, cfg.AgentLabel, c.AgentInstanceID())

	// SIGINT/SIGTERM leave the room and exit 0 — the exit code is returned up
	// to main rather than taken here, so nothing can be left unflushed.
	stopCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	ui.Log("presence keep-alive running (Ctrl-C to leave).")
	keepAlive(stopCtx, c)

	// Leaving must still work after the signal cancelled the context above, so
	// it gets a fresh one.
	leaveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Leave(leaveCtx)
	return nil
}

// keepAlive is the heartbeat half of bin/landfall.mjs's keepLive (line 162).
// A failed beat is swallowed (`.catch(() => {})`): one dropped request during
// an incident is not a reason to drop the seat.
//
// The live-watch half — subscribing to the room's event stream and narrating
// it — is deliberately absent here. It belongs to internal/realtime, which
// lands with the serve track (tasks.md T057-T058); `join` has no bridge
// session to park events on, so heartbeat is the whole of its behavior until
// then.
func keepAlive(ctx context.Context, c *client.Client) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			beatCtx, cancel := context.WithTimeout(context.Background(), heartbeatInterval)
			_ = c.Heartbeat(beatCtx, "investigating")
			cancel()
		}
	}
}

// cmdContext is Cobra's context, or a fresh background one when the command
// was invoked without one.
func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
