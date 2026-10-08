package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

const (
	personToken = "person-token"
	roomToken   = "room-token"
)

// fakeLandfall is a Landfall API on httptest: each test registers the routes
// it needs, and every request is recorded with the token it carried.
type fakeLandfall struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	calls  []fakeCall
}

type fakeCall struct {
	Method, Path, Query, Token string
	Body                       map[string]any
}

func newFakeLandfall(t *testing.T) *fakeLandfall {
	t.Helper()
	f := &fakeLandfall{t: t, routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		f.mu.Lock()
		f.calls = append(f.calls, fakeCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Token: strings.TrimPrefix(r.Header.Get("authorization"), "Bearer "), Body: body})
		h := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			http.Error(w, `{"message":"no route"}`, http.StatusNotFound)
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLandfall) handle(methodPath string, h http.HandlerFunc) {
	f.mu.Lock()
	f.routes[methodPath] = h
	f.mu.Unlock()
}

// json answers a route with a fixed body, but only for the given token.
func (f *fakeLandfall) json(methodPath, token string, body any) {
	f.handle(methodPath, func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"authentication_failed"}`))
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
}

// status answers a route with only a status.
func (f *fakeLandfall) status(methodPath string, code int) {
	f.handle(methodPath, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
}

func (f *fakeLandfall) callsTo(path string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeLandfall) allCalls() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

// testRoom is one room this checkout ("ws") reads.
var testRoom = daemon.RoomView{
	RoomKey: "rk1", IncidentID: "inc-1", Slug: "acme", DisplayID: "Acme 7", Title: "web-edge 5xx",
	Readers: []*daemon.Reader{{Name: "terminal:ws", Kind: daemon.KindTerminal, WorkspaceKey: "ws"}},
}

// deps wires ReadDeps to the fake: the room's reads go through a real
// client with the room token (what the daemon's read op does), the person's
// through a real OrgReader with theirs.
func (f *fakeLandfall) deps(signedIn bool, rooms ...daemon.RoomView) ReadDeps {
	if rooms == nil {
		rooms = []daemon.RoomView{testRoom}
	}
	return ReadDeps{
		WorkspaceKey: "ws",
		Rooms:        func() ([]daemon.RoomView, error) { return rooms, nil },
		Peek:         func() ([]daemon.RoomView, error) { return nil, nil },
		RoomRead: func(ctx context.Context, roomKey, path string) (json.RawMessage, error) {
			for _, r := range rooms {
				if r.RoomKey == roomKey {
					if _, ok := daemon.ReadPathAllowed(path); !ok {
						f.t.Fatalf("the daemon's read op would refuse %q", path)
					}
					cl := client.New(client.Config{BaseURL: f.srv.URL, Slug: r.Slug, IncidentID: r.IncidentID, Token: roomToken}, nil)
					return cl.ReadJSON(ctx, http.MethodGet, path, nil)
				}
			}
			return nil, &client.HTTPError{Path: path, Status: 404}
		},
		Query: func(ctx context.Context, roomKey string, q chartQuery) (map[string]any, error) {
			cl := client.New(client.Config{BaseURL: f.srv.URL, Slug: "acme", IncidentID: "inc-1", Token: roomToken}, nil)
			return cl.QuerySignals(ctx, q.Source, q.Operation, q.Params, q.Connection, q.Account)
		},
		Org: func(context.Context) (*client.OrgReader, error) {
			if !signedIn {
				return nil, errNotSignedIn
			}
			return client.NewOrgReader(f.srv.URL, "acme", personToken, nil), nil
		},
		WebBase: "https://app.example",
		Now:     func() time.Time { return time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC) },
	}
}

// noRoomDeps is a machine with no room daemon.
func noRoomDeps() ReadDeps {
	return ReadDeps{
		WorkspaceKey: "ws",
		Rooms:        func() ([]daemon.RoomView, error) { return nil, os.ErrNotExist },
		Org:          func(context.Context) (*client.OrgReader, error) { return nil, errNotSignedIn },
	}
}

// wantFailure asserts the contract's failure answer with this sentence.
func wantFailure(t *testing.T, ans map[string]any, sentence string) {
	t.Helper()
	if ans["ok"] != false {
		t.Fatalf("ok = %v, want false: %v", ans["ok"], ans)
	}
	if ans["error"] != sentence {
		t.Fatalf("error = %q, want %q", ans["error"], sentence)
	}
	assertPlainSentence(t, sentence)
}

// assertPlainSentence: a sentence a person reads, never a dash or a stack.
func assertPlainSentence(t *testing.T, s string) {
	t.Helper()
	if strings.ContainsAny(s, "—–") || strings.Contains(s, "HTTP 5") || !strings.HasSuffix(s, ".") {
		t.Fatalf("not a plain sentence: %q", s)
	}
}

// roundTrip passes an answer through JSON, as the mod reads it.
func roundTrip(t *testing.T, ans map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(ans)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// events builds a timeline: n rows from seq 0, kinds chosen by pick.
func makeEvents(n int, pick func(seq int) map[string]any) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		e := pick(i)
		e["seq"] = i
		if _, ok := e["occurredAt"]; !ok {
			e["occurredAt"] = time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		}
		out = append(out, e)
	}
	return out
}

// serveEvents answers GET …/events the way core-api's pageBackwards does:
// rows below beforeSeq, the newest `limit` of them, ascending.
func (f *fakeLandfall) serveEvents(path string, events []map[string]any) {
	f.handle("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+roomToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("agentInstanceId") != "" {
			f.t.Errorf("a person's read must not name an agent instance: %s", r.URL.RawQuery)
		}
		rows := events
		if b := r.URL.Query().Get("beforeSeq"); b != "" {
			before, _ := strconv.Atoi(b)
			var below []map[string]any
			for _, e := range rows {
				if e["seq"].(int) < before {
					below = append(below, e)
				}
			}
			rows = below
		}
		if l := r.URL.Query().Get("limit"); l != "" {
			n, _ := strconv.Atoi(l)
			if len(rows) > n {
				rows = rows[len(rows)-n:]
			}
		}
		if rows == nil {
			rows = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(rows)
	})
}

// --- the commands, end to end through Cobra --------------------------------

// TestReadCommandsAnswerOneJSONLineAndExitZero runs each read command with
// no room daemon and no sign-in, through the real argument handling: each
// prints exactly one JSON line with ok:false and a sentence, and exits 0,
// even with a flag it does not know.
func TestReadCommandsAnswerOneJSONLineAndExitZero(t *testing.T) {
	rt, err := os.MkdirTemp("/tmp", "lfr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LANDFALL_SLUG", "")
	t.Setenv("LANDFALL_DAEMON", "0")

	for _, args := range [][]string{
		{"incidents", "--all", "--host", "claude-code"},
		{"timeline", "--room", "rk1", "--kind", "findings", "--limit", "5", "--surprise"},
		{"wall", "--room", "rk1"},
		{"lb", "--minutes", "30"},
		{"comms"},
		{"brain", "--q", "cdn origin"},
		{"join", "--incident", "inc-1", "--host", "claude-code"},
	} {
		t.Run(args[0], func(t *testing.T) {
			var out bytes.Buffer
			prev := stdout
			stdout = &out
			defer func() { stdout = prev }()
			ui := &UI{Out: &out, Err: io.Discard}
			code := run(ui, args, func(string) string { return "" })
			if code != 0 {
				t.Fatalf("exit %d, want 0; out %q", code, out.String())
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("want one line, got %q", out.String())
			}
			var ans map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &ans); err != nil {
				t.Fatalf("not JSON: %q", lines[0])
			}
			if ans["ok"] != false {
				t.Fatalf("want ok:false with no room and no sign-in, got %v", ans)
			}
			assertPlainSentence(t, ans["error"].(string))
		})
	}
}

func TestPickRoom(t *testing.T) {
	other := daemon.RoomView{RoomKey: "rk2", IncidentID: "inc-222222", Slug: "acme", DisplayID: "Acme 9", Readers: []*daemon.Reader{{WorkspaceKey: "elsewhere"}}}
	d := ReadDeps{WorkspaceKey: "ws", Rooms: func() ([]daemon.RoomView, error) { return []daemon.RoomView{other, testRoom}, nil }}
	if r, why := pickRoom(d, ""); why != "" || r.RoomKey != "rk1" {
		t.Fatalf("default room = %+v %q, want this checkout's", r, why)
	}
	for _, sel := range []string{"rk2", "inc-222222", "inc-22", "Acme 9"} {
		if r, why := pickRoom(d, sel); why != "" || r.RoomKey != "rk2" {
			t.Fatalf("--room %q = %+v %q", sel, r, why)
		}
	}
	if _, why := pickRoom(d, "nope"); why != "No room nope is open on this machine. Join it first." {
		t.Fatalf("unknown room: %q", why)
	}
	if _, why := pickRoom(ReadDeps{WorkspaceKey: "x", Rooms: d.Rooms}, ""); why != msgNoRoom {
		t.Fatalf("no room here: %q", why)
	}
	if _, why := pickRoom(noRoomDeps(), ""); why != msgNoRoom {
		t.Fatalf("no daemon: %q", why)
	}
}

// TestPickRoomPrefersTheRoomThisSessionIsIn: a daemon attached to an older,
// resolved room and the current one answers with the current one, never the
// first by key (the live run's desktop panes read the old room).
func TestPickRoomPrefersTheRoomThisSessionIsIn(t *testing.T) {
	term := func() *daemon.Reader { return &daemon.Reader{Kind: daemon.KindTerminal, WorkspaceKey: "ws"} }
	agent := func(harness string, connected bool) *daemon.Reader {
		return &daemon.Reader{Kind: daemon.KindAgent, WorkspaceKey: "ws", Harness: harness, Connected: connected}
	}
	resolved := &narrate.RoomStatus{Status: "resolved"}
	open := &narrate.RoomStatus{Status: "investigating"}

	cases := []struct {
		name  string
		rooms []daemon.RoomView
		peek  []daemon.RoomView
		want  string
	}{
		{
			name: "the room this harness's agent is in, though older and quieter",
			rooms: []daemon.RoomView{
				{RoomKey: "a-old", MaxSeq: 400, Readers: []*daemon.Reader{term(), agent("codex", true)}},
				{RoomKey: "b-new", MaxSeq: 30, Readers: []*daemon.Reader{term(), agent("claude-code", true)}},
			},
			want: "b-new",
		},
		{
			name: "an agent that left does not count",
			rooms: []daemon.RoomView{
				{RoomKey: "a", MaxSeq: 10, Readers: []*daemon.Reader{term(), agent("claude-code", false)}},
				{RoomKey: "b", MaxSeq: 20, Readers: []*daemon.Reader{term()}},
			},
			want: "b",
		},
		{
			name: "an open incident before a resolved one, read from the peek",
			rooms: []daemon.RoomView{
				{RoomKey: "a-resolved", MaxSeq: 500, Readers: []*daemon.Reader{term()}},
				{RoomKey: "b-open", MaxSeq: 40, Readers: []*daemon.Reader{term()}},
			},
			peek: []daemon.RoomView{{RoomKey: "a-resolved", Status: resolved}, {RoomKey: "b-open", Status: open}},
			want: "b-open",
		},
		{
			name: "the peek's agent.inRoom counts too",
			rooms: []daemon.RoomView{
				{RoomKey: "a", MaxSeq: 500, Readers: []*daemon.Reader{term()}},
				{RoomKey: "b", MaxSeq: 40, Readers: []*daemon.Reader{term()}},
			},
			peek: []daemon.RoomView{{RoomKey: "a", Agent: &daemon.AgentView{}}, {RoomKey: "b", Agent: &daemon.AgentView{InRoom: true}}},
			want: "b",
		},
		{
			name: "otherwise the newest activity",
			rooms: []daemon.RoomView{
				{RoomKey: "a", MaxSeq: 10, Readers: []*daemon.Reader{term()}},
				{RoomKey: "b", MaxSeq: 99, Readers: []*daemon.Reader{term()}},
			},
			want: "b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := ReadDeps{
				WorkspaceKey: "ws", Harness: "claude-code",
				Rooms: func() ([]daemon.RoomView, error) { return tc.rooms, nil },
				Peek:  func() ([]daemon.RoomView, error) { return tc.peek, nil },
			}
			if r, why := pickRoom(d, ""); why != "" || r.RoomKey != tc.want {
				t.Fatalf("pickRoom = %q %q, want %q", r.RoomKey, why, tc.want)
			}
			// --room still names any room outright.
			if r, _ := pickRoom(d, tc.rooms[0].RoomKey); r.RoomKey != tc.rooms[0].RoomKey {
				t.Fatalf("--room %q = %q", tc.rooms[0].RoomKey, r.RoomKey)
			}
		})
	}
}

// TestCurrentRoomsOrdersTheWatchStream: the stream's rooms[0] is the room the
// mod draws, so the peek is ordered the same way before it is written.
func TestCurrentRoomsOrdersTheWatchStream(t *testing.T) {
	rooms := []daemon.RoomView{
		{RoomKey: "a", MaxSeq: 900, Status: &narrate.RoomStatus{Status: "resolved"}, Agent: &daemon.AgentView{}},
		{RoomKey: "b", MaxSeq: 100, Status: &narrate.RoomStatus{Status: "investigating"}, Agent: &daemon.AgentView{}},
		{RoomKey: "c", MaxSeq: 50, Status: &narrate.RoomStatus{Status: "investigating"}, Agent: &daemon.AgentView{InRoom: true}},
		{RoomKey: "d", MaxSeq: 300, Status: &narrate.RoomStatus{Status: "closed"}},
	}
	currentRooms(rooms, "ws", "claude-code")
	got := ""
	for _, r := range rooms {
		got += r.RoomKey
	}
	if got != "cbad" {
		t.Fatalf("order = %q, want the agent's room, the open one, then resolved by newest", got)
	}
}
