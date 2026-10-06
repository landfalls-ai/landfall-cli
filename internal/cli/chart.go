package cli

// chart.go — `landfall chart --query <json>`: a signal read on the room's
// canvas as a chart, for the person who asked for it with one key.
//
// The Claude Code mod's key 4 ("add it to the room") runs this. It used to call
// share_with_room through Claude Code's own MCP connection, and Claude Code
// puts a plugin's MCP call made from a key press to the permission dialog
// without asking any mod, so the key press was always followed by "Do you want
// to proceed?" for the same share. Running a local command instead takes the
// share out of Claude Code's tool permissions: the person pressed the key, and
// nothing else is asked.
//
// What it does, all on this machine:
//
//  1. finds the room this checkout reads (the room daemon's `rooms`);
//  2. asks the daemon to make the read with the room's own session (`query`,
//     the same read query_signals makes), so no token leaves the daemon;
//  3. builds the chart from the read (narrate.ChartFromSignalRead) and checks
//     it against the room's widget shapes (tools.ValidateWidget);
//  4. queues it on this workspace's spool as a widget, where the session's
//     background worker publishes it within seconds, as every share is.
//
// It prints one JSON line: {"ok":true,"title":...} or {"ok":false,"error":...},
// and exits 1 when nothing was queued.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/spool"
	"github.com/landfalls-ai/landfall-cli/internal/tools"
	"github.com/spf13/cobra"
)

// chartQuery is `--query`: the arguments query_signals took, plus a title.
type chartQuery struct {
	Source     string         `json:"source"`
	Operation  string         `json:"operation"`
	Params     map[string]any `json:"params,omitempty"`
	Connection string         `json:"connection,omitempty"`
	Account    string         `json:"account,omitempty"`
	Title      string         `json:"title,omitempty"`
}

// ChartDeps is what RunChart needs, injectable for tests.
type ChartDeps struct {
	// Room finds the room this checkout reads: its daemon key and incident.
	Room func() (roomKey, incidentID string, err error)
	// Query makes the read through the daemon.
	Query func(roomKey string, q chartQuery) (result map[string]any, agentInstanceID string, err error)
	// Queue puts the widget on this workspace's spool.
	Queue func(incidentID, agentInstanceID, text string, w *spool.WidgetPayload) error
}

// RunChart builds and queues the chart, returning its title.
func RunChart(q chartQuery, deps ChartDeps) (string, error) {
	if strings.TrimSpace(q.Source) == "" || strings.TrimSpace(q.Operation) == "" {
		return "", errors.New("the query needs a source and an operation")
	}
	roomKey, incidentID, err := deps.Room()
	if err != nil {
		return "", err
	}
	result, instance, err := deps.Query(roomKey, q)
	if err != nil {
		return "", err
	}
	data, label, ok := narrate.ChartFromSignalRead(result)
	if !ok {
		return "", errors.New("that read has no time series to chart")
	}
	title := strings.TrimSpace(q.Title)
	if title == "" {
		title = label
	}
	payload := &tools.WidgetPayload{WidgetType: "chart", Title: title, Data: data}
	if problems := tools.ValidateWidget(payload); len(problems) > 0 {
		return "", fmt.Errorf("the chart does not fit the room's shape: %s", strings.Join(problems, "; "))
	}
	w := &spool.WidgetPayload{WidgetType: payload.WidgetType, Title: payload.Title, Data: payload.Data}
	if err := deps.Queue(incidentID, instance, "Chart: "+title, w); err != nil {
		return "", err
	}
	return title, nil
}

func newChartCommand(ui *UI) *cobra.Command {
	var host, queryJSON, roomSel string
	c := newCommand(ui, "chart", func(*cobra.Command, []string) error {
		ws := hooks.Workspace{Harness: hooks.DetectHookHarness(host, "", os.Getenv)}
		var q chartQuery
		if err := json.Unmarshal([]byte(queryJSON), &q); err != nil {
			return chartAnswer(ui, "", fmt.Errorf("--query is not a JSON object: %w", err))
		}
		title, err := RunChart(q, ChartDeps{
			Room: func() (string, string, error) { return workspaceRoom(ws, roomSel) },
			Query: func(roomKey string, q chartQuery) (map[string]any, string, error) {
				res, err := daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{
					Op: "query", RoomKey: roomKey, Source: q.Source, Operation: q.Operation,
					Params: q.Params, Connection: q.Connection, Account: q.Account,
				}, daemon.QueryTimeout)
				if err != nil {
					return nil, "", fmt.Errorf("the room daemon did not answer: %w", err)
				}
				if !res.OK {
					return nil, "", errors.New(res.Error)
				}
				return res.Signals, res.AgentInstanceID, nil
			},
			Queue: func(incidentID, instance, text string, w *spool.WidgetPayload) error {
				sp, err := spool.Open(ws.Getenv, hooks.WorkspaceKey(ws.Dir()))
				if err != nil {
					return err
				}
				_, err = sp.AcceptKind(incidentID, instance, text, nil, w, false, "widget")
				return err
			},
		})
		return chartAnswer(ui, title, err)
	})
	c.Flags().StringVar(&host, "host", "", "the agent host this session runs in")
	c.Flags().StringVar(&queryJSON, "query", "", `the read, as query_signals took it: {"source","operation","params","connection","account","title"}`)
	c.Flags().StringVar(&roomSel, "room", "", "the incident, when this checkout reads more than one room")
	c.Hidden = true
	return c
}

// chartAnswer prints the one JSON line, and exits 1 when nothing was queued.
func chartAnswer(ui *UI, title string, err error) error {
	if err != nil {
		body, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error()})
		_, _ = fmt.Fprintln(stdout, string(body))
		return &exitError{code: 1}
	}
	body, _ := json.Marshal(map[string]any{"ok": true, "title": title})
	_, _ = fmt.Fprintln(stdout, string(body))
	return nil
}
