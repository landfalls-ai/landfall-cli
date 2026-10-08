package daemon

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// openingEdge is the fake room session with a real client's OpenArtifact.
type openingEdge struct {
	*fakeEdge
	real *client.Client
}

func (o *openingEdge) OpenArtifact(ctx context.Context, id string) ([]byte, string, error) {
	return o.real.OpenArtifact(ctx, id)
}

func TestReadRawReadsOneArtifactWithTheRoomsSession(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0, 0xff, '\n', 1}
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/o/acme/incidents/inc-1/artifacts/art-1/open":
			w.Header().Set("content-type", "image/png")
			_, _ = w.Write(png)
		case "/o/acme/incidents/inc-1/artifacts/gone/open":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()
	cfg := client.Config{BaseURL: srv.URL, Slug: "acme", IncidentID: "inc-1", Token: "room-token"}
	d := readDaemon(t, &openingEdge{fakeEdge: &fakeEdge{}, real: client.New(cfg, nil)})
	ctx := context.Background()
	att := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	if !att.OK {
		t.Fatalf("attach: %+v", att)
	}

	res := d.handler.Handle(ctx, Request{Op: "read-raw", RoomKey: att.RoomKey, Path: "/artifacts/art-1/open"})
	if !res.OK {
		t.Fatalf("read-raw: %+v", res)
	}
	got, ct, err := RawBody(res.Body)
	if err != nil || ct != "image/png" || !bytes.Equal(got, png) {
		t.Fatalf("body = %v %q %v", got, ct, err)
	}
	mu.Lock()
	last := seen[len(seen)-1]
	mu.Unlock()
	if last != "GET /o/acme/incidents/inc-1/artifacts/art-1/open Bearer room-token" {
		t.Fatalf("request = %q", last)
	}

	res = d.handler.Handle(ctx, Request{Op: "read-raw", RoomKey: att.RoomKey, Path: "/artifacts/gone/open"})
	if res.OK || res.HTTPStatus != http.StatusNotFound {
		t.Fatalf("refused: %+v", res)
	}

	before := len(seen)
	for _, p := range []string{"/events", "/artifacts/art-1/open?agentInstanceId=x", "/artifacts/../events/open", "/artifacts/a/b/open", "https://evil.example/artifacts/a/open", "/artifacts//open", ""} {
		if r := d.handler.Handle(ctx, Request{Op: "read-raw", RoomKey: att.RoomKey, Path: p}); r.OK || !strings.Contains(r.Error, "not a read") {
			t.Fatalf("path %q: %+v", p, r)
		}
	}
	if len(seen) != before {
		t.Fatalf("a refused path reached the network: %v", seen[before:])
	}
	if r := d.handler.Handle(ctx, Request{Op: "read-raw", RoomKey: "nope", Path: "/artifacts/art-1/open"}); r.OK || r.Error != "no such room" {
		t.Fatalf("unknown room: %+v", r)
	}
	// The JSON read op still refuses the file route: bytes go through read-raw only.
	if r := d.handler.Handle(ctx, Request{Op: "read", RoomKey: att.RoomKey, Path: "/artifacts/art-1/open"}); r.OK {
		t.Fatalf("read op: %+v", r)
	}
}

func TestRawReadPathAllowed(t *testing.T) {
	for path, want := range map[string]string{
		"/artifacts/abc-123_X/open": "abc-123_X",
		"/artifacts/abc/open?x=1":   "",
		"/artifacts/abc":            "",
		"/artifacts/a%2Fb/open":     "",
		"//evil/artifacts/a/open":   "",
	} {
		got, ok := RawReadPathAllowed(path)
		if (want == "") == ok || got != want {
			t.Fatalf("%q → %q %v, want %q", path, got, ok, want)
		}
	}
}
