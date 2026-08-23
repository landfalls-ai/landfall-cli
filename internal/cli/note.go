package cli

// note.go — `landfall note "<text>"`, a port of bin/landfall.mjs:314-325.
//
// Join, heartbeat what is being noted, post ONE finding, leave. It is the
// smallest useful thing a human at a terminal can contribute to a room, and it
// deliberately goes straight through internal/client rather than the MCP tool
// wrappers: there is no agent and no session here, just one contribution.

import (
	"context"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/spf13/cobra"
)

func newNoteCommand(ui *UI, link string) *cobra.Command {
	c := newCommand(ui, "note", func(cmd *cobra.Command, args []string) error {
		return runNote(cmdContext(cmd), ui, link, args)
	})
	// The note's text is whatever remains of argv, joined with spaces — and it
	// may legitimately begin with a dash, so nothing here may be read as a flag.
	c.DisableFlagParsing = true
	return c
}

func runNote(ctx context.Context, ui *UI, link string, args []string) error {
	cfg, err := resolveConfig(ctx, ui, link)
	if err != nil {
		return err
	}
	if cfg == nil {
		ui.Log("set LANDFALL_* env or pass --link to post a note.")
		return usage()
	}

	c := client.New(*cfg, nil)
	if _, err := c.Join(ctx); err != nil {
		return err
	}

	text := strings.Join(args, " ")
	if err := c.Heartbeat(ctx, "noting: "+text); err != nil {
		return err
	}
	if err := c.Contribute(ctx, "finding", map[string]any{"text": text}); err != nil {
		return err
	}
	ui.Log("note posted.")

	// Best-effort, like the Node original's `.catch(() => {})`: the note is
	// already on the timeline, so a failed leave must not turn a successful
	// contribution into a non-zero exit.
	leaveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Leave(leaveCtx)
	return nil
}
