package cli

// lines.go — `landfall lines`: claim or hand back a line of investigation, as
// the person (contracts/cli-json.md §7 as amended by review finding 5).
//
//	landfall lines claim   --room <roomKey> --label "<text>"
//	landfall lines release --room <roomKey> --claim <claimId>
//
// A line is held by a person, never an agent (the server refuses an agent
// actor), so both go out on the person's own session with no agent instance:
// the same rule, and the same choice of session, as `landfall vote`.
//
// The server takes {lineKey, label, idempotencyKey} and nothing else. The key
// is derived from the label here (lowercase, every run of other characters one
// "-"), so "eu-west-1 5xx" is the line "eu-west-1-5xx". Two people claiming
// the same words claim the same line, and the second is told who has it.
//
// Who holds what rides on `landfall watch` (`status.lines`), read from the
// server's own list rather than folded from the event ring.
//
// It prints one JSON line and exits 0 whatever happened.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/spf13/cobra"
)

// lineLabelMax is the server's LINE_LABEL_MAX.
const lineLabelMax = 120

// lineKeyMax is the longest line key the server takes.
const lineKeyMax = 128

// LineKeyOf derives a line key from a label: lowercase ASCII letters and
// digits, every other run one "-", no "-" at either end. "" when nothing is
// left.
func LineKeyOf(label string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(label) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	key := strings.TrimRight(b.String(), "-")
	if len(key) > lineKeyMax {
		key = strings.TrimRight(key[:lineKeyMax], "-")
	}
	return key
}

// LinesFlags are `landfall lines`' flags.
type LinesFlags struct {
	Room  string
	Label string
	Claim string
}

// RunLinesClaim claims a line for the person and returns the one JSON answer.
func RunLinesClaim(ctx context.Context, f LinesFlags, deps PersonDeps) map[string]any {
	label := strings.TrimSpace(f.Label)
	if label == "" {
		return failAnswer(`Name the line with --label, for example --label "eu-west-1 5xx".`)
	}
	if strings.ContainsAny(label, "\r\n") {
		return failAnswer("Keep the line's label to one line.")
	}
	if utf8.RuneCountInString(label) > lineLabelMax {
		return failAnswer("Keep the line's label to 120 characters.")
	}
	key := LineKeyOf(label)
	if key == "" {
		return failAnswer("Name the line with some letters or numbers.")
	}
	room, err := deps.Room(f.Room)
	if err != nil {
		return failAnswer(err.Error())
	}
	sess, err := actAsPerson(ctx, room, deps, "claim a line")
	if err != nil {
		return failAnswer(err.Error())
	}
	wctx, cancel := context.WithTimeout(ctx, personTimeout)
	defer cancel()
	res, err := sess.cl.ClaimLine(wctx, key, label, deps.NewKey())
	if err != nil {
		return failAnswer(lineError(err, sess.viaRoom))
	}
	if res.Outcome == "lost" {
		holder := "Someone"
		if res.HeldBy != nil && strings.TrimSpace(res.HeldBy.DisplayName) != "" {
			holder = strings.TrimSpace(res.HeldBy.DisplayName)
		}
		out := failAnswer(holder + " already holds this line. Help them, or claim another.")
		out["heldBy"], out["claimId"], out["lineKey"] = holder, res.ClaimID, res.LineKey
		return out
	}
	lineKey := res.LineKey
	if lineKey == "" {
		lineKey = key
	}
	return map[string]any{"ok": true, "claimId": res.ClaimID, "lineKey": lineKey, "label": label}
}

// RunLinesRelease hands a line back and returns the one JSON answer.
func RunLinesRelease(ctx context.Context, f LinesFlags, deps PersonDeps) map[string]any {
	claimID := strings.TrimSpace(f.Claim)
	if claimID == "" {
		return failAnswer("Name the line with --claim and its claim id.")
	}
	room, err := deps.Room(f.Room)
	if err != nil {
		return failAnswer(err.Error())
	}
	sess, err := actAsPerson(ctx, room, deps, "release a line")
	if err != nil {
		return failAnswer(err.Error())
	}
	wctx, cancel := context.WithTimeout(ctx, personTimeout)
	defer cancel()
	res, err := sess.cl.ReleaseLine(wctx, claimID, deps.NewKey())
	if err != nil {
		return failAnswer(lineError(err, sess.viaRoom))
	}
	out := map[string]any{"ok": true}
	if res != nil && res.Outcome == "not-held" {
		out["note"] = "That line was already released."
	}
	return out
}

// lineError is a failed claim or release in a sentence a person can act on.
func lineError(err error, viaRoom bool) string {
	var he *client.HTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusConflict:
			r := strings.ToLower(he.Reason)
			switch {
			case strings.Contains(r, "held by"):
				return "Someone else holds that line, so only they can release it."
			case strings.Contains(r, "agent"):
				return "A line belongs to a person, and this session speaks for an agent. Run landfall login, then try again."
			case r == "" || strings.Contains(r, "retr"):
				return "The room was busy and that did not land. Try again."
			}
			return plainSentence(he.Reason)
		case http.StatusBadRequest:
			return "Landfall did not accept that line. Keep the label to one line of plain text."
		case http.StatusNotFound:
			return "Lines of investigation are not available on this Landfall yet."
		}
	}
	return personHTTPError(err, viaRoom)
}

// newLinesCommand wires `landfall lines claim|release`. A front end runs it;
// it stays out of the help text like `watch` and `chart`.
func newLinesCommand(ui *UI) *cobra.Command {
	var f LinesFlags
	var host string
	c := newCommand(ui, "lines", func(cmd *cobra.Command, args []string) error {
		ws := hooks.Workspace{Harness: hooks.DetectHookHarness(host, "", os.Getenv)}
		deps := PersonDeps{}.withDefaults(ws)
		verb := ""
		if len(args) > 0 {
			verb = args[0]
		}
		switch verb {
		case "claim":
			return printPersonAnswer(RunLinesClaim(cmd.Context(), f, deps))
		case "release":
			return printPersonAnswer(RunLinesRelease(cmd.Context(), f, deps))
		}
		return printPersonAnswer(failAnswer(`Say claim or release: landfall lines claim --label "<text>", or landfall lines release --claim <claimId>.`))
	})
	c.Flags().StringVar(&host, "host", "", "the agent host this session runs in")
	c.Flags().StringVar(&f.Room, "room", "", "the room (its key or incident id); default: this folder's room")
	c.Flags().StringVar(&f.Label, "label", "", "the line, in a few words (claim)")
	c.Flags().StringVar(&f.Claim, "claim", "", "the claim id to hand back (release)")
	c.Hidden = true
	return c
}
