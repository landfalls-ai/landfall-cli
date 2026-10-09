package cli

// joinincident.go — `landfall join --incident <incidentId>` (contract §3,
// review #12): attach this folder to an incident's room the way a share link
// does, for the mod's switchboard.
//
// It does not put the person's ~59-minute login token into the room (that is
// what resolveConfig's no-link path does, and nothing refreshes it there).
// Instead it mints a share link with that token (POST edge/share-link, the
// web app's own share dialog) and redeems it exactly as `landfall join <link>`
// and join_war_room do: through the room daemon's link book
// (frontEnd.redeemLink), so the room holds an 8-hour edge session and the link
// is recorded for this machine. The same link handed to this session's agent
// (join_war_room) then joins with that session instead of redeeming it again.
//
// The folder is attached as the person's TERMINAL reader for this harness,
// the same reader an agent's attach creates beside itself: the band, status
// line and hooks see the room at once. A terminal reader never keeps a room
// open on its own, so if no agent session of this checkout joins, the daemon
// leaves the room when it next idles, as it does for any room nobody reads.

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

// JoinIncidentDeps are RunJoinIncident's effects.
type JoinIncidentDeps struct {
	// Org is the person's own session.
	Org func(ctx context.Context) (*client.OrgReader, error)
	// EnsureDaemon starts the room daemon if it is not running.
	EnsureDaemon func() bool
	// Redeem turns the link into a room config, through the daemon's link
	// book in production.
	Redeem func(ctx context.Context, link string) (*client.Config, error)
	// Attach attaches this folder's terminal reader to the room.
	Attach func(cfg client.Config, linkHash string) (*daemon.Response, error)
	// Rooms names the room when the attach could not read its frame in time.
	Rooms func() ([]daemon.RoomView, error)
}

// RunJoinIncident joins the incident's room and answers the contract's JSON.
func RunJoinIncident(ctx context.Context, incidentID string, d JoinIncidentDeps) map[string]any {
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return failure("Name the incident to join: landfall join --incident <incidentId>.")
	}
	org, err := d.Org(ctx)
	if err != nil || org == nil {
		return failure("Sign in to join an incident from here: run landfall login.")
	}
	mint, err := org.MintShareLink(ctx, incidentID)
	if err != nil {
		var he *client.HTTPError
		if errors.As(err, &he) && he.Status == 404 {
			return failure("Landfall has no incident " + incidentID + " in " + org.Slug + ".")
		}
		if errors.As(err, &he) && he.Status == 403 {
			return failure("Landfall would not let you join that incident. Your sign-in may be for another organization.")
		}
		return failure(personRefusal(err, "a join link for that incident"))
	}
	if !d.EnsureDaemon() {
		return failure("The room daemon could not start here, so this folder cannot hold the room. Paste the share link into your agent instead.")
	}
	cfg, err := d.Redeem(ctx, mint.ShareURL)
	if err != nil || cfg == nil {
		return failure("The join link would not redeem. Try again, or open the room from the web app.")
	}
	res, err := d.Attach(*cfg, daemon.LinkHash(mint.ShareURL))
	if err != nil || res == nil {
		return failure("The room daemon could not join the room. Try again shortly.")
	}
	name := narrate.RoomName{}
	if res.Frame != nil {
		name = narrate.RoomName{DisplayID: res.Frame.Incident.DisplayID, Title: res.Frame.Incident.Title}.Clean()
	}
	if name.DisplayID == "" && name.Title == "" && d.Rooms != nil {
		if rooms, err := d.Rooms(); err == nil {
			for _, r := range rooms {
				if r.RoomKey == res.RoomKey {
					name = narrate.RoomName{DisplayID: r.DisplayID, Title: r.Title}.Clean()
				}
			}
		}
	}
	return map[string]any{
		"ok":         true,
		"roomKey":    res.RoomKey,
		"incidentId": cfg.IncidentID,
		"displayId":  name.DisplayID,
		"title":      name.Title,
		// The link this machine redeemed: handed to this session's agent
		// (join_war_room), it joins with the same session, not a second redeem.
		"link": mint.ShareURL,
	}
}

// joinIncidentFlags reads `--incident <id>` and `--host <name>` out of join's
// unparsed arguments (join takes no Cobra flags: a URL that merely looks like
// a flag must not be rejected).
func joinIncidentFlags(args []string) (incident string, host string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--incident" && i+1 < len(args):
			incident, ok = args[i+1], true
			i++
		case strings.HasPrefix(a, "--incident="):
			incident, ok = strings.TrimPrefix(a, "--incident="), true
		case a == "--incident":
			ok = true
		case a == "--host" && i+1 < len(args):
			host = args[i+1]
			i++
		case strings.HasPrefix(a, "--host="):
			host = strings.TrimPrefix(a, "--host=")
		}
	}
	return incident, host, ok
}

// runJoinIncident is the production wiring.
func runJoinIncident(ctx context.Context, ui *UI, incidentID, host string) error {
	harness := hooks.HarnessFromClientName(hooks.DetectHookHarness(host, "", os.Getenv))
	ws := hooks.Workspace{Harness: harness.Key}
	log := func(msg string) { ui.Log("%s", msg) }
	read := defaultReadDeps(ws)
	deps := joinIncidentRoomDeps(ws, harness, log, func(ctx context.Context, link string) (*client.Config, error) {
		cfg, err := client.RedeemShareLink(ctx, link, client.RedeemOptions{BaseURL: os.Getenv("LANDFALL_BASE_URL")})
		if err != nil {
			return nil, err
		}
		return &cfg, nil
	})
	deps.Org, deps.Rooms = read.Org, read.Rooms
	return printAnswer(stdout, RunJoinIncident(ctx, incidentID, deps))
}

// joinIncidentRoomDeps is the daemon half of the production wiring: the redeem
// through the daemon's link book and the terminal reader's attach. The attach
// is what puts the room on offer to this folder's agent (adopt.go): a
// `landfall serve` of the same workspace and harness joins it with this
// session, without redeeming the link again.
func joinIncidentRoomDeps(ws hooks.Workspace, harness hooks.Harness, log func(string), redeem func(context.Context, string) (*client.Config, error)) JoinIncidentDeps {
	fe := newFrontEnd(ws, log)
	return JoinIncidentDeps{
		EnsureDaemon: func() bool { return daemon.EnsureRunning(ws, nil, log) },
		Redeem: func(ctx context.Context, link string) (*client.Config, error) {
			return fe.redeemLink(ctx, link, redeem)
		},
		Attach: func(cfg client.Config, linkHash string) (*daemon.Response, error) {
			label := harness.Label
			if env := os.Getenv("LANDFALL_AGENT_LABEL"); env != "" {
				label = env
			}
			if label == "" {
				label = agentLabel()
			}
			cfg.AgentLabel = label
			key := hooks.WorkspaceKey(ws.Dir())
			return daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{
				Op:   "attach",
				Room: &cfg,
				Link: linkHash,
				Reader: &daemon.ReaderSpec{
					Name: daemon.TerminalReaderNameFor(key, harness.Key), Kind: string(daemon.KindTerminal),
					WorkspaceKey: key, Workspace: ws.Dir(), Harness: harness.Key,
				},
				Fingerprints: computeFingerprints(ws.Dir()),
			}, daemon.AttachTimeout+5*time.Second)
		},
	}
}
