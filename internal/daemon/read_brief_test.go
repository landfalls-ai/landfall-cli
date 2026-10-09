package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// TestReadOpReadsTheFrameAsThePerson: `landfall brief` reads the context
// frame and the claims through the read op, with the room's session and no
// agentInstanceId, so the server records no context pull and moves no seat's
// cursor. A frame read that names an agent is refused before the network.
func TestReadOpReadsTheFrameAsThePerson(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/o/acme/incidents/inc-1/edge/context/frame":
			_, _ = w.Write([]byte(`{"asOfSeq":7}`))
		case "/o/acme/incidents/inc-1/claims":
			_, _ = w.Write([]byte(`{"claims":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	cfg := client.Config{BaseURL: srv.URL, Slug: "acme", IncidentID: "inc-1", Token: "room-token"}
	edge := &readingEdge{fakeEdge: &fakeEdge{}, real: client.New(cfg, nil)}
	d := readDaemon(t, edge)
	ctx := context.Background()
	att := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "terminal:ws", Kind: "terminal", WorkspaceKey: "ws"}})
	if !att.OK {
		t.Fatalf("attach: %+v", att)
	}
	mu.Lock()
	before := len(seen)
	mu.Unlock()

	res := d.handler.Handle(ctx, Request{Op: "read", RoomKey: att.RoomKey, Path: "/edge/context/frame"})
	if !res.OK || string(res.Body) != `{"asOfSeq":7}` {
		t.Fatalf("frame read: %+v", res)
	}
	res = d.handler.Handle(ctx, Request{Op: "read", RoomKey: att.RoomKey, Path: "/claims"})
	if !res.OK || string(res.Body) != `{"claims":[]}` {
		t.Fatalf("claims read: %+v", res)
	}
	mu.Lock()
	got := append([]string(nil), seen[before:]...)
	mu.Unlock()
	want := []string{
		"GET /o/acme/incidents/inc-1/edge/context/frame Bearer room-token",
		"GET /o/acme/incidents/inc-1/claims Bearer room-token",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests = %q, want %q", got, want)
	}

	for _, p := range []string{"/edge/context/frame?agentInstanceId=i-1", "/edge/context/delta", "/edge/context/frame?sinceVersion=3"} {
		if r := d.handler.Handle(ctx, Request{Op: "read", RoomKey: att.RoomKey, Path: p}); r.OK || !strings.Contains(r.Error, "not a read") {
			t.Fatalf("path %q: %+v", p, r)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != before+2 {
		t.Fatalf("a refused path reached the network: %v", seen[before+2:])
	}
}
