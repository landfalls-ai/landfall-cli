package daemon

// read.go — the `read` op: one read of a room route with the room's own
// session, for the person's commands (`landfall timeline`, `wall`, `comms`,
// `brain`). The session token never leaves the daemon; the command gets the
// body.
//
// Only an allow-list of read routes, each with only the query parameters it
// needs, and never `agentInstanceId`: a command asking here is the person
// reading their own room, not an agent receiving context, so nothing records
// a context pull and no seat's cursor moves.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// ReadTimeout bounds one read: the whole timeline of a long incident can be
// a few megabytes.
const ReadTimeout = 20 * time.Second

// roomReader is what a room session must offer for the read op.
// *client.Client has it; a test fake may not, and then the read says so.
type roomReader interface {
	ReadJSON(ctx context.Context, method, path string, body map[string]any) (json.RawMessage, error)
}

// readRoutes are the routes the read op will make, and the query parameters
// each may carry. Paths are relative to /o/:slug/incidents/:id.
var readRoutes = map[string]map[string]bool{
	"/events":         {"limit": true, "beforeSeq": true, "sinceSeq": true},
	"/comms/messages": {},
	"/memory":         {},
	// `landfall brief`: the shared context as the person reads it, and the
	// claims it folds each open item's state from. With no agentInstanceId
	// the frame read records no context pull and moves no seat's cursor.
	"/edge/context/frame": {},
	"/claims":             {},
}

// ReadPathAllowed reports whether path (with its query) is one the read op
// makes, and returns it in the form sent.
func ReadPathAllowed(path string) (string, bool) {
	u, err := url.Parse(path)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "", false
	}
	allowed, ok := readRoutes[u.Path]
	if !ok {
		return "", false
	}
	q := u.Query()
	for k := range q {
		if !allowed[k] {
			return "", false
		}
	}
	if len(q) == 0 {
		return u.Path, true
	}
	return u.Path + "?" + q.Encode(), true
}

// read answers the read op.
func (d *Daemon) read(ctx context.Context, req Request) Response {
	room := d.room(req.RoomKey)
	if room == nil {
		return fail("no such room")
	}
	path, allowed := ReadPathAllowed(req.Path)
	if !allowed {
		return fail("that is not a read this daemon makes")
	}
	cl, _ := room.ClientAndInstance()
	if cl == nil {
		return fail("the room is not connected yet")
	}
	rr, canRead := cl.(roomReader)
	if !canRead {
		return fail("this room's session cannot make that read")
	}
	body, err := rr.ReadJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		res := fail("the room refused the read: " + strings.TrimSpace(err.Error()))
		var he *client.HTTPError
		if errors.As(err, &he) {
			res.HTTPStatus = he.Status
		}
		return res
	}
	res := ok()
	res.RoomKey, res.Body = room.Key, body
	return res
}
