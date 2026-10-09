package daemon

// readraw.go — the `read-raw` op: one shared artifact's bytes, read with the
// room's own session, for `landfall artifact`. The read op (read.go) answers
// JSON only; an artifact is bytes, so this op carries them base64 inside its
// JSON body: {"contentType":"text/markdown","base64":"..."}.
//
// The same rule as the read op: the person reading their own room, so the
// route is GET …/artifacts/:id/open with no agentInstanceId, nothing records
// a context pull and no seat's cursor moves. The size cap is the server's
// (5 MiB); this op adds none.

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// artifactOpenPath is the one route the raw read makes.
var artifactOpenPath = regexp.MustCompile(`^/artifacts/([A-Za-z0-9_-]{1,128})/open$`)

// RawReadPathAllowed reports whether path is the open route of one artifact,
// with no query, and returns the artifact's id.
func RawReadPathAllowed(path string) (string, bool) {
	u, err := url.Parse(path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	m := artifactOpenPath.FindStringSubmatch(u.Path)
	if m == nil || u.Path != path {
		return "", false
	}
	return m[1], true
}

// rawBody is the read-raw op's answer body.
type rawBody struct {
	ContentType string `json:"contentType"`
	Base64      []byte `json:"base64"`
}

// RawBody decodes a read-raw answer's body: the bytes and their content type.
func RawBody(body json.RawMessage) ([]byte, string, error) {
	var rb rawBody
	if err := json.Unmarshal(body, &rb); err != nil {
		return nil, "", err
	}
	return rb.Base64, rb.ContentType, nil
}

// readRaw answers the read-raw op.
func (d *Daemon) readRaw(ctx context.Context, req Request) Response {
	room := d.room(req.RoomKey)
	if room == nil {
		return fail("no such room")
	}
	id, allowed := RawReadPathAllowed(req.Path)
	if !allowed {
		return fail("that is not a read this daemon makes")
	}
	cl, _ := room.ClientAndInstance()
	if cl == nil {
		return fail("the room is not connected yet")
	}
	raw, contentType, err := cl.OpenArtifact(ctx, id)
	if err != nil {
		res := fail("the room refused the read: " + strings.TrimSpace(err.Error()))
		var he *client.HTTPError
		if errors.As(err, &he) {
			res.HTTPStatus = he.Status
		}
		return res
	}
	if raw == nil {
		raw = []byte{}
	}
	body, err := json.Marshal(rawBody{ContentType: contentType, Base64: raw})
	if err != nil {
		return fail("the artifact could not be passed on")
	}
	res := ok()
	res.RoomKey, res.Body = room.Key, body
	return res
}
