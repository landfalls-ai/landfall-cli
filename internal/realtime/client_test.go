package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// fakeConn is the Go equivalent of the Node original's injectable `ioImpl`
// (src/live.mjs:26): it lets the filtering and the degrade-silently behaviour
// be driven by hand, which a real socket makes impossible.
type fakeConn struct {
	mu        sync.Mutex
	listeners map[string][]func(...any)
	emits     []emitted
	connected bool
	dropped   int
	emitErr   error
}

type emitted struct {
	event string
	args  []any
}

func newFakeConn() *fakeConn {
	return &fakeConn{listeners: map[string][]func(...any){}}
}

func (f *fakeConn) On(event string, listener func(args ...any)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listeners[event] = append(f.listeners[event], listener)
	return nil
}

func (f *fakeConn) Emit(event string, args ...any) error {
	f.mu.Lock()
	if f.emitErr != nil {
		err := f.emitErr
		f.mu.Unlock()
		return err
	}
	f.emits = append(f.emits, emitted{event: event, args: args})
	f.mu.Unlock()
	return nil
}

func (f *fakeConn) Disconnect() {
	f.mu.Lock()
	f.dropped++
	f.connected = false
	f.mu.Unlock()
}

func (f *fakeConn) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

// fire invokes every listener registered for one socket event.
func (f *fakeConn) fire(event string, args ...any) {
	f.mu.Lock()
	ls := append([]func(args ...any){}, f.listeners[event]...)
	f.mu.Unlock()
	for _, l := range ls {
		l(args...)
	}
}

func (f *fakeConn) emitsFor(event string) []emitted {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []emitted
	for _, e := range f.emits {
		if e.event == event {
			out = append(out, e)
		}
	}
	return out
}

// harness wires a Client to a fakeConn and captures everything observable.
type harness struct {
	client *Client
	conn   *fakeConn

	mu     sync.Mutex
	events []client.Event
	logs   []string
}

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	h := &harness{conn: newFakeConn()}
	opts := Options{
		BaseURL:    "https://api.landfalls.ai/",
		Slug:       "landfall",
		Token:      "tok-123",
		IncidentID: "inc-1",
		OnEvent: func(e client.Event) {
			h.mu.Lock()
			h.events = append(h.events, e)
			h.mu.Unlock()
		},
		Log: func(s string) {
			h.mu.Lock()
			h.logs = append(h.logs, s)
			h.mu.Unlock()
		},
		Dial: func(baseURL, ns string, auth map[string]any) (Conn, error) {
			return h.conn, nil
		},
	}
	if mutate != nil {
		mutate(&opts)
	}
	h.client = New(opts)
	if err := h.client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return h
}

func (h *harness) got() []client.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]client.Event(nil), h.events...)
}

func (h *harness) logged() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.logs...)
}

func (h *harness) hasLog(sub string) bool {
	for _, l := range h.logged() {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// event builds the shape the gateway actually pushes.
func event(seq int64, typ string, payload map[string]any) map[string]any {
	m := map[string]any{"seq": float64(seq), "type": typ}
	if payload != nil {
		m["payload"] = payload
	}
	return m
}

// --- dial + subscribe ------------------------------------------------------

func TestConnectDialsRealtimeNamespaceWithAuth(t *testing.T) {
	var gotURL, gotNS string
	var gotAuth map[string]any
	conn := newFakeConn()

	c := New(Options{
		BaseURL:    "https://api.landfalls.ai/",
		Slug:       "landfall",
		Token:      "tok-123",
		IncidentID: "inc-1",
		Dial: func(baseURL, ns string, auth map[string]any) (Conn, error) {
			gotURL, gotNS, gotAuth = baseURL, ns, auth
			return conn, nil
		},
	})
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// `baseUrl.replace(/\/$/, '')` + '/realtime' (src/live.mjs:27).
	if gotURL != "https://api.landfalls.ai" {
		t.Errorf("base URL = %q, want the trailing slash stripped", gotURL)
	}
	if gotNS != "/realtime" {
		t.Errorf("namespace = %q, want /realtime", gotNS)
	}
	if gotAuth["token"] != "tok-123" || gotAuth["slug"] != "landfall" {
		t.Errorf("auth = %+v, want {token, slug}", gotAuth)
	}
}

func TestConnectSubscribesOnConnect(t *testing.T) {
	h := newHarness(t, nil)

	if len(h.conn.emitsFor(SubscribeEvent)) != 0 {
		t.Fatal("subscribed before the socket was up")
	}
	h.conn.fire("connect")

	subs := h.conn.emitsFor(SubscribeEvent)
	if len(subs) != 1 {
		t.Fatalf("subscribe emits = %d, want 1", len(subs))
	}
	arg, ok := subs[0].args[0].(map[string]any)
	if !ok || arg["incidentId"] != "inc-1" {
		t.Errorf("subscribe payload = %+v, want {incidentId: inc-1}", subs[0].args)
	}
	if !h.hasLog("live: subscribed to war-room updates") {
		t.Errorf("logs = %v, want the subscribe notice", h.logged())
	}
	if !h.client.Connected() {
		t.Error("Connected() = false after connect")
	}
}

func TestReconnectResubscribes(t *testing.T) {
	h := newHarness(t, nil)
	h.conn.fire("connect")
	h.conn.fire("disconnect", "transport close")
	if h.client.Connected() {
		t.Error("Connected() = true after disconnect")
	}
	h.conn.fire("connect")

	if n := len(h.conn.emitsFor(SubscribeEvent)); n != 2 {
		t.Errorf("subscribe emits = %d, want 2 (one per connect)", n)
	}
}

func TestConnectRequiresBaseURLAndIncident(t *testing.T) {
	dial := func(string, string, map[string]any) (Conn, error) { return newFakeConn(), nil }
	if err := New(Options{IncidentID: "i", Dial: dial}).Connect(context.Background()); err == nil {
		t.Error("want an error with no base URL")
	}
	if err := New(Options{BaseURL: "https://x", Dial: dial}).Connect(context.Background()); err == nil {
		t.Error("want an error with no incident id")
	}
}

// --- receive + filter ------------------------------------------------------

func TestIncidentEventReachesTheCallback(t *testing.T) {
	h := newHarness(t, nil)
	h.conn.fire("connect")
	h.conn.fire(IncidentEvent, event(7, "finding.published", map[string]any{
		"agentInstanceId": "someone-else",
		"summary":         "origin rolled back",
	}))

	got := h.got()
	if len(got) != 1 {
		t.Fatalf("delivered %d events, want 1", len(got))
	}
	if got[0].Type != "finding.published" {
		t.Errorf("type = %q", got[0].Type)
	}
	if got[0].Seq == nil || *got[0].Seq != 7 {
		t.Errorf("seq = %v, want 7", got[0].Seq)
	}
	// Raw must survive so read_timeline can return the event verbatim.
	if len(got[0].Raw) == 0 {
		t.Error("Raw is empty; the verbatim bytes were lost")
	}
	var round map[string]any
	if err := json.Unmarshal(got[0].Raw, &round); err != nil {
		t.Fatalf("Raw is not JSON: %v", err)
	}
	if round["payload"].(map[string]any)["summary"] != "origin rolled back" {
		t.Errorf("Raw lost the payload: %s", got[0].Raw)
	}
}

func TestQuietTypesAreDropped(t *testing.T) {
	h := newHarness(t, nil)
	h.conn.fire("connect")
	for _, typ := range []string{
		"edge.participant.heartbeat",
		"edge.participant.joined",
		"edge.participant.left",
		"edge.ticket.redeemed",
	} {
		h.conn.fire(IncidentEvent, event(1, typ, nil))
	}
	if got := h.got(); len(got) != 0 {
		t.Fatalf("delivered %d quiet events, want 0: %+v", len(got), got)
	}

	h.conn.fire(IncidentEvent, event(2, "edge.participant.spoke", nil))
	if got := h.got(); len(got) != 1 {
		t.Fatalf("a non-quiet event was dropped: %d delivered", len(got))
	}
}

func TestEchoSuppression(t *testing.T) {
	h := newHarness(t, nil)
	h.client.SetOwnInstanceID("me-1")
	h.conn.fire("connect")

	h.conn.fire(IncidentEvent, event(1, "note.added", map[string]any{"agentInstanceId": "me-1"}))
	if got := h.got(); len(got) != 0 {
		t.Fatalf("our own publication echoed back: %+v", got)
	}

	h.conn.fire(IncidentEvent, event(2, "note.added", map[string]any{"agentInstanceId": "them-2"}))
	h.conn.fire(IncidentEvent, event(3, "note.added", nil))
	if got := h.got(); len(got) != 2 {
		t.Fatalf("delivered %d, want 2 (someone else's, and one with no payload)", len(got))
	}
}

func TestEchoSuppressionUsesTheInstanceFuncAndToleratesNotYetJoined(t *testing.T) {
	var own string
	h := newHarness(t, func(o *Options) {
		o.OwnInstanceID = func() string { return own }
	})
	h.conn.fire("connect")

	// Before POST /edge/join lands there is no id: "unknown" must never be
	// read as "matches", so nothing is suppressed (src/live.mjs:39's `own &&`).
	h.conn.fire(IncidentEvent, event(1, "note.added", map[string]any{"agentInstanceId": "me-1"}))
	if got := h.got(); len(got) != 1 {
		t.Fatalf("suppressed an event before we knew our own id: %d delivered", len(got))
	}

	own = "me-1"
	h.conn.fire(IncidentEvent, event(2, "note.added", map[string]any{"agentInstanceId": "me-1"}))
	if got := h.got(); len(got) != 1 {
		t.Fatalf("echo not suppressed once the id was known: %d delivered", len(got))
	}
}

func TestUnreadableEventIsIgnoredNotPanicked(t *testing.T) {
	h := newHarness(t, nil)
	h.conn.fire("connect")
	h.conn.fire(IncidentEvent)                                 // no args at all
	h.conn.fire(IncidentEvent, nil)                            // an explicit nil
	h.conn.fire(IncidentEvent, "not json")                     // a bare string
	h.conn.fire(IncidentEvent, map[string]any{"a": func() {}}) // unmarshalable

	if got := h.got(); len(got) != 0 {
		t.Fatalf("delivered %d junk events, want 0", len(got))
	}
}

func TestNilCallbackIsFine(t *testing.T) {
	// A bridge with no callback and no logger must still connect and simply
	// drop what it receives, rather than nil-panic on the socket's goroutine.
	conn := newFakeConn()
	c := New(Options{
		BaseURL:    "https://api.landfalls.ai",
		IncidentID: "inc-1",
		Dial:       func(string, string, map[string]any) (Conn, error) { return conn, nil },
	})
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn.fire("connect")
	conn.fire(IncidentEvent, event(1, "note.added", nil))
	conn.fire("connect_error", errors.New("boom"))
}

// --- degrade silently ------------------------------------------------------

// The contract pinned by test/edge-bridge.test.mjs:679 — "with realtime
// unavailable the tools behave exactly as they do today, and say nothing about
// it". A connect failure is a log line and NOTHING else: no error out of
// Connect, no panic, no callback, and the client stays usable.
func TestConnectErrorIsLoggedAndNeverSurfaced(t *testing.T) {
	h := newHarness(t, nil)
	h.conn.fire("connect_error", errors.New("xhr poll error"))

	if h.client.Connected() {
		t.Error("Connected() = true after connect_error")
	}
	if got := h.got(); len(got) != 0 {
		t.Errorf("connect_error produced events: %+v", got)
	}
	if !h.hasLog("live: realtime unavailable (xhr poll error) — falling back to get_updates pulls") {
		t.Errorf("logs = %v, want the exact fallback notice", h.logged())
	}
	// And the watch survives: a later successful connect still subscribes.
	h.conn.fire("connect")
	if n := len(h.conn.emitsFor(SubscribeEvent)); n != 1 {
		t.Errorf("subscribe emits after recovery = %d, want 1", n)
	}
}

func TestConnectErrorWithNoLoggerIsSilent(t *testing.T) {
	conn := newFakeConn()
	c := New(Options{
		BaseURL:    "https://api.landfalls.ai",
		IncidentID: "inc-1",
		Dial:       func(string, string, map[string]any) (Conn, error) { return conn, nil },
	})
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn.fire("connect_error", errors.New("dns"))
	if c.Connected() {
		t.Error("Connected() = true after connect_error")
	}
}

func TestSubscribeFailureDegradesQuietly(t *testing.T) {
	h := newHarness(t, nil)
	h.conn.mu.Lock()
	h.conn.emitErr = errors.New("socket closed")
	h.conn.mu.Unlock()

	h.conn.fire("connect")
	if !h.hasLog("falling back to get_updates pulls") {
		t.Errorf("logs = %v, want a fallback notice", h.logged())
	}
	if h.hasLog("live: subscribed to war-room updates") {
		t.Error("claimed a subscription that failed")
	}
}

// --- lifecycle -------------------------------------------------------------

func TestStopDisconnectsAndIsIdempotent(t *testing.T) {
	h := newHarness(t, nil)
	h.conn.fire("connect")
	h.client.Stop()
	h.client.Stop()

	h.conn.mu.Lock()
	n := h.conn.dropped
	h.conn.mu.Unlock()
	if n != 1 {
		t.Errorf("Disconnect called %d times, want exactly 1", n)
	}
	if h.client.Connected() {
		t.Error("Connected() = true after Stop")
	}
}

func TestContextCancellationStopsTheWatch(t *testing.T) {
	conn := newFakeConn()
	ctx, cancel := context.WithCancel(context.Background())
	c := New(Options{
		BaseURL:    "https://api.landfalls.ai",
		IncidentID: "inc-1",
		Dial:       func(string, string, map[string]any) (Conn, error) { return conn, nil },
	})
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn.fire("connect")
	cancel()

	// The stop runs on its own goroutine, so poll for it rather than assuming
	// it has already happened by the time cancel() returns.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn.mu.Lock()
		done := conn.dropped > 0
		conn.mu.Unlock()
		if done {
			if c.Connected() {
				t.Error("Connected() = true after the context was cancelled")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Error("context cancellation did not disconnect the socket")
}

func TestDialFailureIsReportedNotPanicked(t *testing.T) {
	c := New(Options{
		BaseURL:    "https://api.landfalls.ai",
		IncidentID: "inc-1",
		Dial:       func(string, string, map[string]any) (Conn, error) { return nil, errors.New("no route") },
	})
	err := c.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no route") {
		t.Errorf("Connect error = %v, want it to wrap the dial failure", err)
	}
	if c.Connected() {
		t.Error("Connected() = true after a failed dial")
	}
}

// --- decoding --------------------------------------------------------------

func TestDecodeEvent(t *testing.T) {
	t.Run("map", func(t *testing.T) {
		e, err := DecodeEvent(event(3, "note.added", map[string]any{"text": "hi"}))
		if err != nil {
			t.Fatal(err)
		}
		if e.SeqOr(-1) != 3 || e.Type != "note.added" || e.Payload["text"] != "hi" {
			t.Errorf("decoded = %+v", e)
		}
	})
	t.Run("raw bytes", func(t *testing.T) {
		e, err := DecodeEvent(json.RawMessage(`{"seq":4,"type":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		if e.SeqOr(-1) != 4 {
			t.Errorf("seq = %v", e.Seq)
		}
	})
	t.Run("no seq stays nil", func(t *testing.T) {
		// session.EnqueueEvent refuses a seq-less event; it must arrive as nil
		// rather than being flattened to a real seq 0.
		e, err := DecodeEvent(map[string]any{"type": "x"})
		if err != nil {
			t.Fatal(err)
		}
		if e.Seq != nil {
			t.Errorf("seq = %v, want nil", *e.Seq)
		}
	})
	t.Run("large seq keeps precision", func(t *testing.T) {
		e, err := DecodeEvent(json.RawMessage(`{"seq":9007199254740993,"type":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		if e.SeqOr(-1) != 9007199254740993 {
			t.Errorf("seq = %d", e.SeqOr(-1))
		}
	})
	t.Run("junk", func(t *testing.T) {
		if _, err := DecodeEvent(nil); err == nil {
			t.Error("want an error for nil")
		}
		if _, err := DecodeEvent("nope"); err == nil {
			t.Error("want an error for a non-JSON string")
		}
	})
}

func TestErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		args []any
		want string
	}{
		{"error", []any{errors.New("boom")}, "boom"},
		{"string", []any{"boom"}, "boom"},
		{"object with message", []any{map[string]any{"message": "Unauthorized"}}, "Unauthorized"},
		{"empty", nil, "connect error"},
		{"nil arg", []any{nil}, "connect error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorMessage(tc.args); got != tc.want {
				t.Errorf("errorMessage(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
