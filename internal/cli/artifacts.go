package cli

// artifacts.go — `landfall artifacts --room <k>` and `landfall artifact <id>
// --room <k> [--max-chars N] [--bytes]` (CONSOLE-SPEC §9, §4.10): what the
// room has shared, and one shared file's contents, for the console's Context
// tab and a person's investigation.
//
// The list is folded from the timeline's `artifact.shared` rows (read with the
// room's session through the daemon's read op), the way the web's
// projectArtifacts folds them: oldest first by seq, identical bytes (the same
// sha256) collapsed to their first sharing, then answered newest first.
//
// One file is read through the daemon's read-raw op (GET …/artifacts/:id/open
// with the room's session and no agentInstanceId: the person reading their
// own room). A text-like file (read_artifact's set) comes back as text, cut at
// --max-chars characters (default 20,000, read_artifact's own default); any
// other file is described as binary. --bytes adds the file base64 when it is
// 256 KB or less, for the desktop console's image preview.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/tools"
	"github.com/spf13/cobra"
)

const (
	// artifactDefaultChars is read_artifact's own default.
	artifactDefaultChars = 20000
	// artifactBytesMax is the largest file --bytes hands over.
	artifactBytesMax = 256 * 1024
	// artifactsMax bounds the list; a room rarely holds more than a handful.
	artifactsMax = 200
)

// ArtifactDeps is the read commands' wiring plus the daemon's read-raw op.
type ArtifactDeps struct {
	ReadDeps
	// RawRead is the daemon's read-raw op: one artifact's bytes and content
	// type, read with the room's own session. A refusal is a *client.HTTPError.
	RawRead func(ctx context.Context, roomKey, path string) ([]byte, string, error)
}

// errDaemonTooOld is RawRead's answer from a daemon that predates read-raw.
var errDaemonTooOld = errors.New("daemon too old")

// defaultArtifactDeps is the production wiring for one workspace.
func defaultArtifactDeps(ws hooks.Workspace) ArtifactDeps {
	sock := hooks.DaemonSocketPath(ws)
	return ArtifactDeps{
		ReadDeps: defaultReadDeps(ws),
		RawRead: func(_ context.Context, roomKey, path string) ([]byte, string, error) {
			res, err := daemon.Send(sock, daemon.Request{Op: "read-raw", RoomKey: roomKey, Path: path}, daemon.ReadTimeout)
			if err != nil {
				if res != nil && res.HTTPStatus != 0 {
					return nil, "", &client.HTTPError{Path: path, Status: res.HTTPStatus}
				}
				if res != nil && strings.HasPrefix(res.Error, "unknown op") {
					return nil, "", errDaemonTooOld
				}
				if res != nil && res.Error != "" {
					return nil, "", errors.New(res.Error)
				}
				return nil, "", fmt.Errorf("the room daemon did not answer: %w", err)
			}
			return daemon.RawBody(res.Body)
		},
	}
}

func newArtifactsCommand(ui *UI) *cobra.Command {
	return newReadCommand(ui, "artifacts", func(cmd *cobra.Command, ws hooks.Workspace, room string) map[string]any {
		return RunArtifacts(cmdContext(cmd), room, defaultReadDeps(ws))
	})
}

// ArtifactOptions are `landfall artifact`'s arguments.
type ArtifactOptions struct {
	Room       string
	ArtifactID string
	MaxChars   int
	Bytes      bool
}

func newArtifactCommand(ui *UI) *cobra.Command {
	var maxChars int
	var bytes bool
	c := newReadCommand(ui, "artifact", func(cmd *cobra.Command, ws hooks.Workspace, room string) map[string]any {
		id := ""
		if args := cmd.Flags().Args(); len(args) > 0 {
			id = args[0]
		}
		return RunArtifact(cmdContext(cmd), ArtifactOptions{Room: room, ArtifactID: id, MaxChars: maxChars, Bytes: bytes}, defaultArtifactDeps(ws))
	})
	c.Flags().IntVar(&maxChars, "max-chars", artifactDefaultChars, "how many characters of a text file")
	c.Flags().BoolVar(&bytes, "bytes", false, "also hand over the file base64, when it is 256 KB or less")
	return c
}

// sharedArtifact is one artifact.shared row, folded.
type sharedArtifact struct {
	seq                                              int64
	artifactID, filename, contentType, sharedAt, sha string
	size                                             int64
	displayName, kind, edgeAgentLabel, humanActorID  string
}

// row is the contract's artifacts row.
func (a sharedArtifact) row() map[string]any {
	return map[string]any{
		"artifactId":  a.artifactID,
		"filename":    a.filename,
		"contentType": a.contentType,
		"size":        a.size,
		"sharedAt":    a.sharedAt,
		"sharer": map[string]any{
			"displayName":    a.displayName,
			"kind":           a.kind,
			"edgeAgentLabel": a.edgeAgentLabel,
			"humanActorId":   a.humanActorID,
		},
	}
}

// projectArtifacts is the web's projectArtifacts (warroom-ui
// widgets/projectArtifacts.ts): artifact.shared rows, optionally one person's,
// oldest first by seq, identical bytes collapsed to their first sharing.
func projectArtifacts(events []timelineEvent, humanActorID string) []sharedArtifact {
	var rows []timelineEvent
	for _, e := range events {
		if e.Type != "artifact.shared" {
			continue
		}
		if humanActorID != "" && jStr(e.Payload, "humanActorId") != humanActorID {
			continue
		}
		rows = append(rows, e)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].SeqOrZero() < rows[j].SeqOrZero() })
	seen := map[string]bool{}
	out := make([]sharedArtifact, 0, len(rows))
	for _, e := range rows {
		p := e.Payload
		sha := jStr(p, "sha256")
		if sha != "" && seen[sha] {
			continue
		}
		if sha != "" {
			seen[sha] = true
		}
		a := sharedArtifact{
			seq:            e.SeqOrZero(),
			artifactID:     jStr(p, "artifactId"),
			filename:       oneLineText(narrate.Printable(firstStr(p, "filename")), 200),
			contentType:    firstStr(p, "contentType"),
			sharedAt:       firstStr(p, "at"),
			sha:            sha,
			displayName:    oneLineText(narrate.Printable(firstStr(p, "displayName")), 60),
			kind:           firstStr(p, "kind"),
			edgeAgentLabel: oneLineText(narrate.Printable(firstStr(p, "edgeAgentLabel")), 60),
			humanActorID:   jStr(p, "humanActorId"),
		}
		if a.filename == "" {
			a.filename = "artifact"
		}
		if a.contentType == "" {
			a.contentType = "application/octet-stream"
		}
		if a.sharedAt == "" {
			a.sharedAt = e.OccurredAt
		}
		if a.displayName == "" {
			a.displayName = "Someone"
		}
		if a.kind == "" {
			a.kind = "member"
		}
		if n, ok := jNum(p, "size"); ok && n >= 0 {
			a.size = int64(n)
		}
		out = append(out, a)
	}
	return out
}

// artifactRows is a fold as the contract's rows, newest first.
func artifactRows(arts []sharedArtifact) []map[string]any {
	out := make([]map[string]any, 0, len(arts))
	for i := len(arts) - 1; i >= 0 && len(out) < artifactsMax; i-- {
		out = append(out, arts[i].row())
	}
	return out
}

// readTimeline reads the room's whole timeline with the room's session.
func readTimeline(ctx context.Context, d ReadDeps, roomKey, what string) ([]timelineEvent, string) {
	raw, err := d.RoomRead(ctx, roomKey, "/events")
	if err != nil {
		return nil, roomRefusal(err, what)
	}
	var events []timelineEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, "Landfall sent a timeline this CLI could not read."
	}
	return events, ""
}

// RunArtifacts builds the artifacts answer.
func RunArtifacts(ctx context.Context, roomSel string, d ReadDeps) map[string]any {
	room, why := pickRoom(d, roomSel)
	if why != "" {
		return failure(why)
	}
	events, why := readTimeline(ctx, d, room.RoomKey, "the room's artifacts")
	if why != "" {
		return failure(why)
	}
	arts := projectArtifacts(events, "")
	ans := map[string]any{"ok": true, "artifacts": artifactRows(arts)}
	if len(arts) > artifactsMax {
		ans["total"] = len(arts)
	}
	return ans
}

// RunArtifact builds one artifact's answer.
func RunArtifact(ctx context.Context, o ArtifactOptions, d ArtifactDeps) map[string]any {
	id := strings.TrimSpace(o.ArtifactID)
	if id == "" {
		return failure("Name the artifact to read: landfall artifact <artifactId>.")
	}
	path := "/artifacts/" + id + "/open"
	if _, ok := daemon.RawReadPathAllowed(path); !ok {
		return failure("That is not an artifact id.")
	}
	room, why := pickRoom(d.ReadDeps, o.Room)
	if why != "" {
		return failure(why)
	}
	events, why := readTimeline(ctx, d.ReadDeps, room.RoomKey, "the room's artifacts")
	if why != "" {
		return failure(why)
	}
	var meta *sharedArtifact
	for _, a := range projectArtifacts(events, "") {
		if a.artifactID == id {
			a := a
			meta = &a
			break
		}
	}
	if meta == nil {
		// A collapsed duplicate is still the same file: find it among every
		// sharing before saying the room has no such artifact.
		for _, e := range events {
			if e.Type == "artifact.shared" && jStr(e.Payload, "artifactId") == id {
				arts := projectArtifacts([]timelineEvent{e}, "")
				meta = &arts[0]
				break
			}
		}
	}
	if meta == nil {
		return failure("This room has no artifact " + id + ".")
	}
	if d.RawRead == nil {
		return failure("Update landfall to read artifacts from here.")
	}
	body, servedType, err := d.RawRead(ctx, room.RoomKey, path)
	if err != nil {
		if errors.Is(err, errDaemonTooOld) {
			return failure("The room daemon is older than this command. Run landfall daemon stop, then try again.")
		}
		var he *client.HTTPError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return failure("Landfall no longer has the file for " + meta.filename + ".")
		}
		return failure(roomRefusal(err, "this artifact"))
	}

	ans := map[string]any{
		"ok":          true,
		"artifactId":  id,
		"filename":    meta.filename,
		"contentType": meta.contentType,
		"size":        int64(len(body)),
	}
	if tools.TextLike(servedType) || tools.TextLike(meta.contentType) {
		maxChars := o.MaxChars
		if maxChars <= 0 {
			maxChars = artifactDefaultChars
		}
		text := string(body)
		if !utf8.ValidString(text) {
			text = strings.ToValidUTF8(text, "�")
		}
		total := utf8.RuneCountInString(text)
		truncated := false
		if total > maxChars {
			r := []rune(text)
			text = string(r[:maxChars])
			truncated = true
		}
		ans["text"], ans["totalChars"], ans["truncated"] = text, total, truncated
	} else {
		ans["binary"] = true
	}
	if o.Bytes && len(body) <= artifactBytesMax {
		ans["base64"] = base64.StdEncoding.EncodeToString(body)
	}
	return ans
}
