package cli

// vote.go — `landfall vote`: a person's position on a staged finding, from
// their own session, as THEM (contracts/cli-json.md §2 as amended by review
// finding 1).
//
//	landfall vote --room <roomKey> --claim <seq> --position corroborate|contest [--reason "<text>"]
//
// WHY NOT THE ROOM'S CLIENT. The room daemon holds an incident-scoped edge
// session whose subject is whoever minted the share link, and its client names
// the agent's instance on every write. A position sent through it is the
// agent's, counted on the agent's side of the quorum, and a teammate's link
// would cast it as the teammate. So this command builds a fresh client:
//
//  1. with the person's own sign-in (`landfall login`) when it is for this
//     room's organization; else
//  2. with the room's session, but only when that session IS the person: a
//     guest who joined under their own name, or a link whose subject matches
//     the subject of the person's cached sign-in; else
//  3. not at all: "Sign in to vote as yourself: run landfall login."
//
// No agentInstanceId rides on the request, so the server records the position
// under the session's human. A guest's position counts as a person's but not a
// member's (the quorum's rule), and the answer says so.
//
// It prints one JSON line and exits 0 whatever happened, so a front end reads
// `ok` rather than an exit code.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/spf13/cobra"
)

// personTimeout bounds one write a person command makes.
const personTimeout = 20 * time.Second

// PersonDeps is what the person commands (vote, lines) need, injectable for
// tests. A nil field takes the real default.
type PersonDeps struct {
	// Room finds the room (by roomKey, incident id or prefix; empty for this
	// folder's first room) and returns its session config.
	Room func(sel string) (client.Config, error)
	// OAuthToken is the person's live sign-in token, "" when not signed in.
	OAuthToken func(ctx context.Context) string
	// OAuthSlug is the organization the sign-in is pinned to.
	OAuthSlug func() string
	// OAuthSubject is the subject of the person's cached sign-in, even an
	// expired one: who the person is, for matching against a room session.
	OAuthSubject func() string
	// Doer is the HTTP transport.
	Doer client.Doer
	// NewKey mints an idempotency key.
	NewKey func() string
}

func (d PersonDeps) withDefaults(ws hooks.Workspace) PersonDeps {
	if d.Room == nil {
		d.Room = func(sel string) (client.Config, error) { return daemonRoomSession(ws, sel) }
	}
	if d.OAuthToken == nil {
		d.OAuthToken = func(ctx context.Context) string { return auth.GetCachedAccessToken(ctx, nil) }
	}
	if d.OAuthSlug == nil {
		d.OAuthSlug = auth.GetCachedOrgSlug
	}
	if d.OAuthSubject == nil {
		d.OAuthSubject = func() string {
			if c := auth.ReadCache(); c != nil {
				return jwtSubject(c.AccessToken)
			}
			return ""
		}
	}
	if d.NewKey == nil {
		d.NewKey = newIdempotencyKey
	}
	return d
}

// personSession is the client a person command acts through, and how it was
// chosen.
type personSession struct {
	cl *client.Client
	// viaRoom is true when the room's session stands in for the person.
	viaRoom bool
	// guest is true when that session is a guest's.
	guest bool
}

// errNotPerson is returned when no session on this machine is the person's.
type errNotPerson struct{ action string }

func (e errNotPerson) Error() string {
	return "Sign in to " + e.action + " as yourself: run landfall login."
}

// actAsPerson picks the session that is the person's for this room (see the
// file header), or says how to get one. action names what they are doing, for
// the sign-in sentence ("vote", "claim a line").
func actAsPerson(ctx context.Context, room client.Config, deps PersonDeps, action string) (personSession, error) {
	cfg := client.Config{BaseURL: room.BaseURL, Slug: room.Slug, IncidentID: room.IncidentID}
	if tok := deps.OAuthToken(ctx); tok != "" {
		if slug := deps.OAuthSlug(); slug == "" || slug == room.Slug {
			cfg.Token = tok
			return personSession{cl: client.New(cfg, deps.Doer)}, nil
		}
	}
	if room.Token != "" && strings.HasPrefix(room.HumanActorID, "guest:") {
		cfg.Token = room.Token
		return personSession{cl: client.New(cfg, deps.Doer), viaRoom: true, guest: true}, nil
	}
	if room.Token != "" && room.HumanActorID != "" && deps.OAuthSubject() == room.HumanActorID {
		cfg.Token = room.Token
		return personSession{cl: client.New(cfg, deps.Doer), viaRoom: true}, nil
	}
	return personSession{}, errNotPerson{action: action}
}

// VoteFlags are `landfall vote`'s flags.
type VoteFlags struct {
	Room     string
	Claim    int64
	Position string
	Reason   string
}

// guestVoteNote is what a guest is told with a recorded vote.
const guestVoteNote = "You joined as a guest, so your vote counts as a person's but not as a member's."

// RunVote records the person's position and returns the one JSON answer.
func RunVote(ctx context.Context, f VoteFlags, deps PersonDeps) map[string]any {
	position := strings.ToLower(strings.TrimSpace(f.Position))
	if position != "corroborate" && position != "contest" {
		return failAnswer("Say corroborate or contest with --position.")
	}
	if f.Claim < 0 {
		return failAnswer("Name the finding with --claim and its number.")
	}
	reason := strings.TrimSpace(f.Reason)
	if strings.ContainsAny(reason, "\r\n") {
		reason = strings.Join(strings.Fields(reason), " ")
	}
	room, err := deps.Room(f.Room)
	if err != nil {
		return failAnswer(err.Error())
	}
	sess, err := actAsPerson(ctx, room, deps, "vote")
	if err != nil {
		return failAnswer(err.Error())
	}
	wctx, cancel := context.WithTimeout(ctx, personTimeout)
	defer cancel()
	res, err := sess.cl.PositionAsPerson(wctx, f.Claim, position, reason)
	if err != nil {
		return failAnswer(voteError(err, f.Claim, sess.viaRoom))
	}
	out := map[string]any{"ok": true, "claimSeq": f.Claim, "position": position, "admitted": res != nil && res.State == "admitted"}
	if sess.guest {
		out["note"] = guestVoteNote
	}
	return out
}

// The server's refusals of a vote from the author's side (admission.service.ts
// AUTHOR_VOTE_MESSAGE, OWN_AGENT_VOTE_MESSAGE).
const (
	serverAuthorVote   = "Posting this already counted as your vote"
	serverOwnAgentVote = "Your agent posted this"
)

// voteError is a failed vote in a sentence a person can act on.
func voteError(err error, claim int64, viaRoom bool) string {
	var he *client.HTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusNotFound:
			return fmt.Sprintf("There is no finding #%d in this room.", claim)
		case http.StatusConflict:
			switch {
			case strings.Contains(he.Reason, serverAuthorVote):
				return "You authored this finding, so your position is already counted."
			case strings.Contains(he.Reason, serverOwnAgentVote):
				return "Your agent posted this finding, so your position is already counted."
			case strings.Contains(he.Reason, "closed") || strings.Contains(he.Reason, "frozen"):
				return "The incident is closed, so its findings take no more votes."
			case he.Reason == "" || strings.Contains(strings.ToLower(he.Reason), "retr"):
				return "The room was busy and the vote did not land. Try again."
			}
			return plainSentence(he.Reason)
		case http.StatusBadRequest:
			return "Landfall did not accept that vote. Check the finding number and the position."
		}
	}
	return personHTTPError(err, viaRoom)
}

// personHTTPError is the sentence for a failure every person command shares.
func personHTTPError(err error, viaRoom bool) string {
	var he *client.HTTPError
	if !errors.As(err, &he) {
		return "Landfall did not answer. Check your connection and try again."
	}
	switch {
	case he.Status == http.StatusUnauthorized && viaRoom:
		return "The room session expired. Run landfall login, then try again."
	case he.Status == http.StatusUnauthorized:
		return "Your sign-in expired. Run landfall login."
	case he.Status == http.StatusForbidden:
		return "Landfall refused this for your account. Check you are signed in to the organization this room belongs to."
	case he.Status == http.StatusTooManyRequests:
		return "Too many requests just now. Wait a minute and try again."
	case he.Status >= 500:
		return "Landfall could not record that just now. Try again in a moment."
	}
	if he.Reason != "" {
		return plainSentence(he.Reason)
	}
	return fmt.Sprintf("Landfall refused this (HTTP %d).", he.Status)
}

// plainSentence is server text made fit for a person's screen: no em dashes,
// one line, a capital first letter and a full stop.
func plainSentence(s string) string {
	s = strings.ReplaceAll(s, " — ", ": ")
	s = strings.ReplaceAll(s, "—", "-")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "Landfall refused this."
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func failAnswer(msg string) map[string]any { return map[string]any{"ok": false, "error": msg} }

// printPersonAnswer writes the one JSON line. The command exits 0 either way.
func printPersonAnswer(answer map[string]any) error {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // "<claimId>" reads as written
	if err := enc.Encode(answer); err != nil {
		buf.Reset()
		buf.WriteString(`{"ok":false,"error":"The answer could not be written."}` + "\n")
	}
	_, _ = fmt.Fprint(stdout, buf.String())
	return nil
}

// daemonRoomSession finds the room through the room daemon and asks it for
// the room's session config.
func daemonRoomSession(ws hooks.Workspace, sel string) (client.Config, error) {
	sock := hooks.DaemonSocketPath(ws)
	res, err := daemon.Send(sock, daemon.Request{Op: "rooms"}, time.Second)
	if err != nil || res == nil {
		return client.Config{}, errors.New("No room is open on this machine. Join one first with landfall join and a share link.")
	}
	roomKey := pickPersonRoom(res.Rooms, hooks.WorkspaceKey(ws.Dir()), sel)
	if roomKey == "" {
		if sel != "" {
			return client.Config{}, errors.New("This machine is not in that room. Join it first with landfall join and a share link.")
		}
		return client.Config{}, errors.New("This folder is not in a room. Pass --room, or join one first.")
	}
	got, err := daemon.Send(sock, daemon.Request{Op: "session", RoomKey: roomKey}, time.Second)
	if err != nil || got == nil || got.Room == nil {
		return client.Config{}, errors.New("The room daemon could not hand over the room's session. Update landfall, then try again.")
	}
	return *got.Room, nil
}

// pickPersonRoom is the room a selector names: a roomKey, an incident id or its
// prefix, or a display id. With no selector, the first room this folder reads.
func pickPersonRoom(rooms []daemon.RoomView, workspaceKey, sel string) string {
	sel = strings.TrimSpace(sel)
	for _, r := range rooms {
		if sel == "" {
			for _, rd := range r.Readers {
				if rd.WorkspaceKey == workspaceKey {
					return r.RoomKey
				}
			}
			continue
		}
		if r.RoomKey == sel || r.IncidentID == sel || (len(sel) >= 4 && strings.HasPrefix(r.IncidentID, sel)) || (r.DisplayID != "" && r.DisplayID == sel) {
			return r.RoomKey
		}
	}
	return ""
}

// jwtSubject is the `sub` of a JWT, read without verifying it. It only decides
// which of the person's own tokens to send; the server verifies whichever is
// sent.
func jwtSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	return claims.Sub
}

// newIdempotencyKey is 32 hex characters: a bare identifier the server takes.
func newIdempotencyKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("k%031x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// newVoteCommand wires `landfall vote`. A front end (the Claude Code mod's
// vote card) runs it; it stays out of the help text like `watch` and `chart`.
func newVoteCommand(ui *UI) *cobra.Command {
	var f VoteFlags
	var host string
	claim := int64(-1)
	c := newCommand(ui, "vote", func(cmd *cobra.Command, _ []string) error {
		ws := hooks.Workspace{Harness: hooks.DetectHookHarness(host, "", os.Getenv)}
		f.Claim = claim
		if !cmd.Flags().Changed("claim") {
			f.Claim = -1
		}
		return printPersonAnswer(RunVote(cmd.Context(), f, PersonDeps{}.withDefaults(ws)))
	})
	c.Flags().StringVar(&host, "host", "", "the agent host this session runs in")
	c.Flags().StringVar(&f.Room, "room", "", "the room (its key or incident id); default: this folder's room")
	c.Flags().Int64Var(&claim, "claim", -1, "the finding's number (its seq)")
	c.Flags().StringVar(&f.Position, "position", "", "corroborate or contest")
	c.Flags().StringVar(&f.Reason, "reason", "", "why, in one line")
	c.Hidden = true
	return c
}
