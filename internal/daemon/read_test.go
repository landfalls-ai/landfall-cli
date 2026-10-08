package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// readingEdge is the fake room session plus a real client's ReadJSON, aimed
// at an httptest Landfall.
type readingEdge struct {
	*fakeEdge
	real *client.Client
}

func (r *readingEdge) ReadJSON(ctx context.Context, method, path string, body map[string]any) (json.RawMessage, error) {
	return r.real.ReadJSON(ctx, method, path, body)
}

func readDaemon(t *testing.T, edge session.EdgeClient) *Daemon {
	t.Helper()
	rt, err := os.MkdirTemp("/tmp", "lfd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	return New(Options{
		Workspace: hooks.Workspace{Cwd: t.TempDir(), Env: map[string]string{"XDG_RUNTIME_DIR": rt}},
		IdleGrace: 200 * time.Millisecond,
		Deps: Deps{
			NewClient: func(client.Config) session.EdgeClient { return edge },
			Watch:     (&fakeWire{}).watch,
			Heartbeat: time.Hour,
		},
	})
}

func TestReadOpReadsWithTheRoomsSessionOnly(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/o/acme/incidents/inc-1/events":
			_, _ = w.Write([]byte(`[{"seq":1,"type":"chat.message"}]`))
		case "/o/acme/incidents/inc-1/comms/messages":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	cfg := client.Config{BaseURL: srv.URL, Slug: "acme", IncidentID: "inc-1", Token: "room-token"}
	edge := &readingEdge{fakeEdge: &fakeEdge{}, real: client.New(cfg, nil)}
	d := readDaemon(t, edge)
	ctx := context.Background()
	att := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "claude-code:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	if !att.OK {
		t.Fatalf("attach: %+v", att)
	}

	res := d.handler.Handle(ctx, Request{Op: "read", RoomKey: att.RoomKey, Path: "/events?limit=5&beforeSeq=10"})
	if !res.OK || string(res.Body) != `[{"seq":1,"type":"chat.message"}]` {
		t.Fatalf("read: %+v", res)
	}
	mu.Lock()
	got := seen[len(seen)-1]
	mu.Unlock()
	if got != "GET /o/acme/incidents/inc-1/events?beforeSeq=10&limit=5 Bearer room-token" {
		t.Fatalf("request = %q", got)
	}

	// The server's refusal carries its status, for the command's sentence.
	res = d.handler.Handle(ctx, Request{Op: "read", RoomKey: att.RoomKey, Path: "/comms/messages"})
	if res.OK || res.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("refused read: %+v", res)
	}

	// Anything off the allow-list is refused before the network.
	before := len(seen)
	for _, p := range []string{"/widgets/data", "/events?agentInstanceId=x", "/edge/share-link", "https://evil.example/events", "/memory/../edge/lines", ""} {
		if r := d.handler.Handle(ctx, Request{Op: "read", RoomKey: att.RoomKey, Path: p}); r.OK || !strings.Contains(r.Error, "not a read") {
			t.Fatalf("path %q: %+v", p, r)
		}
	}
	if len(seen) != before {
		t.Fatalf("a refused path reached the network: %v", seen[before:])
	}
	if r := d.handler.Handle(ctx, Request{Op: "read", RoomKey: "nope", Path: "/events"}); r.OK || r.Error != "no such room" {
		t.Fatalf("unknown room: %+v", r)
	}
}

func TestReadOpNeedsASessionThatCanRead(t *testing.T) {
	edge := &fakeEdge{}
	d := readDaemon(t, edge)
	ctx := context.Background()
	cfg := client.Config{BaseURL: "http://x", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	att := d.handler.Handle(ctx, Request{Op: "attach", Room: &cfg, Reader: &ReaderSpec{Name: "a:ws:1", Kind: "agent", WorkspaceKey: "ws"}})
	r := d.handler.Handle(ctx, Request{Op: "read", RoomKey: att.RoomKey, Path: "/memory"})
	if r.OK || r.Error != "this room's session cannot make that read" {
		t.Fatalf("read: %+v", r)
	}
}

func TestReadPathAllowed(t *testing.T) {
	for path, want := range map[string]string{
		"/events":                    "/events",
		"/events?limit=50":           "/events?limit=50",
		"/events?sinceSeq=3":         "/events?sinceSeq=3",
		"/comms/messages":            "/comms/messages",
		"/memory":                    "/memory",
		"/memory?q=x":                "",
		"/dashboard":                 "",
		"/claims/attention":          "",
		"//evil.example/events":      "",
		"/events?limit=5&throughSeq": "",
	} {
		got, ok := ReadPathAllowed(path)
		if (want == "") == ok || got != want {
			t.Fatalf("%q → %q %v, want %q", path, got, ok, want)
		}
	}
}
