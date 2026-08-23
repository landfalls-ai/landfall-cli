// Package realtime is the bridge's outbound realtime connection — the Go port
// of src/live.mjs (feature 021, EDGE_AGENT_INTEGRATION §8.1).
//
// While `landfall serve` runs, it subscribes to the incident's Socket.IO room
// so the teammate is NUDGED the moment other investigators publish shared
// context; the agent then pulls it with `get_updates` at its next task
// boundary. Pull stays authoritative — an MCP host controls its own context.
//
// Best-effort BY DESIGN. A connection that never comes up is logged and
// nothing else: no error is returned to a tool caller, no retry is surfaced,
// no behaviour changes. That contract is pinned by test/edge-bridge.test.mjs:679
// ("with realtime unavailable the tools behave exactly as they do today, and
// say nothing about it"). Note the honest limitation this inherits from the
// Node original: nothing polls in the background, so with no socket the push
// half really is dead — the doorbell simply never rings. Adding a polling
// fallback is a deliberate non-goal here.
//
// Layering: this package depends on internal/client (the wire Event type) and
// nothing else of ours. It never touches internal/session — the caller wires
// OnEvent to session.EnqueueEvent, keeping the queue's ownership in one place.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/landfalls-ai/landfall-cli/internal/client"

	"github.com/zishang520/engine.io-client-go/transports"
	"github.com/zishang520/engine.io/v2/events"
	engineTypes "github.com/zishang520/engine.io/v2/types"
	sio "github.com/zishang520/socket.io-client-go/socket"
)

// The wire vocabulary, fixed by the server's `/realtime` Socket.IO gateway.
// That gateway also understands `resync` ({incidentId, sinceSeq}); the CLI has
// never used it and this port does not either.
const (
	// Namespace is the Socket.IO namespace the war room lives on.
	Namespace = "/realtime"
	// SubscribeEvent asks the gateway to join us to one incident's room.
	SubscribeEvent = "incident.subscribe"
	// IncidentEvent is the server's push of one durable timeline event.
	IncidentEvent = "incident.event"
)

// Conn is the slice of a Socket.IO client socket this package actually uses.
//
// It exists for the same reason `watchIncident` took an `ioImpl` parameter in
// the Node original (src/live.mjs:26): the behaviours worth testing — echo
// suppression, quiet-type filtering, degrade-silently — are exactly the ones a
// real socket makes untestable.
type Conn interface {
	// On registers a listener for a socket event. Re-registering is additive,
	// matching the underlying emitter.
	On(event string, listener func(args ...any)) error
	// Emit sends an event to the server.
	Emit(event string, args ...any) error
	// Disconnect closes the socket. Must be safe to call more than once.
	Disconnect()
	// Connected reports the socket's own view of its state.
	Connected() bool
}

// Dialer constructs a Conn. Swapped out in tests; DefaultDialer in production.
type Dialer func(baseURL, namespace string, auth map[string]any) (Conn, error)

// Options configures a Client. Only BaseURL and IncidentID are required —
// everything else has a safe zero value, since a bridge with no callback and
// no logger should still connect and simply drop what it receives.
type Options struct {
	// BaseURL is the API origin, e.g. https://api.landfalls.ai. A trailing
	// slash is stripped once, as `baseUrl.replace(/\/$/, '')` does.
	BaseURL string
	// Slug is the org slug, sent in the handshake auth alongside the token.
	Slug string
	// Token is the bearer/edge token, sent in the handshake auth.
	Token string
	// IncidentID is the incident to subscribe to on every (re)connect.
	IncidentID string

	// OnEvent receives every event that survives both filters. It is called
	// from the socket's own goroutine, so it must not block for long; the
	// intended wiring is session.EnqueueEvent, which parks and returns.
	OnEvent func(client.Event)

	// OwnInstanceID returns this agent instance's id for echo suppression.
	// A function rather than a value because the bridge learns its id from
	// POST /edge/join, which can land after the socket is already up —
	// the Node original allowed exactly the same (src/live.mjs:38).
	// Leave nil and use SetOwnInstanceID instead if that is simpler.
	OwnInstanceID func() string

	// Log receives the one-line best-effort notices. nil is fine (`log?.()`).
	Log func(string)

	// Dial overrides the socket constructor. nil means DefaultDialer.
	Dial Dialer
}

// Client is one live watch of one incident. The zero value is not usable; use
// New. All methods are safe for concurrent use.
type Client struct {
	opts Options

	mu        sync.RWMutex
	conn      Conn
	ownID     string
	connected bool
	stopped   bool
}

// New builds a Client. It does not dial; call Connect for that.
func New(opts Options) *Client {
	opts.BaseURL = strings.TrimSuffix(opts.BaseURL, "/")
	if opts.Dial == nil {
		opts.Dial = DefaultDialer
	}
	return &Client{opts: opts}
}

// SetOwnInstanceID sets the id used for echo suppression. Safe to call after
// Connect — the bridge routinely learns its instance id from POST /edge/join
// only after the socket is already up.
func (c *Client) SetOwnInstanceID(id string) {
	c.mu.Lock()
	c.ownID = id
	c.mu.Unlock()
}

// ownInstanceID resolves the id, preferring the caller's function when one was
// supplied, so a caller that already owns the value need not mirror it here.
func (c *Client) ownInstanceID() string {
	if fn := c.opts.OwnInstanceID; fn != nil {
		if id := fn(); id != "" {
			return id
		}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ownID
}

// Connected reports whether the socket is currently up.
//
// Deliberately NOT load-bearing: the whole point of this package is that a
// caller does not have to care. Use it for a status line, never to gate a tool
// call — every read is an ordinary HTTP GET that works either way.
func (c *Client) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// Connect dials the gateway and starts watching. It returns an error ONLY for
// a misconfiguration or a socket that could not be constructed at all — never
// for a connection that failed to come up, which is logged and otherwise
// ignored (src/live.mjs:43-45). A caller should treat even the returned error
// as best-effort: log it and carry on serving, exactly as the Node bridge does.
//
// When ctx is cancelled the watch stops itself. Reconnects are the underlying
// client's job; each one re-runs the subscribe, as the Node original's
// `connect` handler does.
func (c *Client) Connect(ctx context.Context) error {
	if c.opts.BaseURL == "" {
		return errors.New("realtime: no base URL")
	}
	if c.opts.IncidentID == "" {
		return errors.New("realtime: no incident id")
	}

	conn, err := c.opts.Dial(c.opts.BaseURL, Namespace, map[string]any{
		"token": c.opts.Token,
		"slug":  c.opts.Slug,
	})
	if err != nil {
		return fmt.Errorf("realtime: dial: %w", err)
	}
	if conn == nil {
		return errors.New("realtime: dialer returned no connection")
	}

	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		conn.Disconnect()
		return errors.New("realtime: client already stopped")
	}
	c.conn = conn
	c.mu.Unlock()

	// Registration order mirrors src/live.mjs:31-45.
	_ = conn.On("connect", func(...any) { c.handleConnect(conn) })
	_ = conn.On(IncidentEvent, func(args ...any) { c.handleIncidentEvent(args) })
	_ = conn.On("connect_error", func(args ...any) { c.handleConnectError(args) })
	// Not in the Node original, and deliberately not load-bearing: it only
	// keeps Connected() honest between a drop and the next reconnect.
	_ = conn.On("disconnect", func(...any) { c.setConnected(false) })

	if ctx != nil && ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			c.Stop()
		}()
	}
	return nil
}

// Stop closes the watch. Idempotent — the Node original's returned stop().
func (c *Client) Stop() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.stopped = true
	c.connected = false
	c.mu.Unlock()

	if conn != nil {
		conn.Disconnect()
	}
}

func (c *Client) handleConnect(conn Conn) {
	c.setConnected(true)
	if err := conn.Emit(SubscribeEvent, map[string]any{"incidentId": c.opts.IncidentID}); err != nil {
		c.log(fmt.Sprintf("live: could not subscribe (%v) — falling back to get_updates pulls", err))
		return
	}
	c.log("live: subscribed to war-room updates")
}

func (c *Client) handleIncidentEvent(args []any) {
	if len(args) == 0 {
		return
	}
	evt, err := DecodeEvent(args[0])
	if err != nil {
		// An undecodable push is the socket's problem, not the agent's:
		// `if (!evt) return` in the Node original. Log at most.
		c.log(fmt.Sprintf("live: ignoring unreadable incident.event (%v)", err))
		return
	}
	if !ShouldDeliver(evt, c.ownInstanceID()) {
		return
	}
	if fn := c.opts.OnEvent; fn != nil {
		fn(evt)
	}
}

func (c *Client) handleConnectError(args []any) {
	c.setConnected(false)
	c.log(fmt.Sprintf("live: realtime unavailable (%s) — falling back to get_updates pulls", errorMessage(args)))
}

func (c *Client) setConnected(v bool) {
	c.mu.Lock()
	c.connected = v
	c.mu.Unlock()
}

func (c *Client) log(msg string) {
	if fn := c.opts.Log; fn != nil {
		fn(msg)
	}
}

// DecodeEvent turns one `incident.event` argument into the wire Event type.
//
// The Socket.IO parser hands us already-decoded JSON (usually a
// map[string]any), while a test or a future transport may hand us raw bytes;
// both are accepted. Re-marshalling a decoded map is what lets client.Event's
// UnmarshalJSON keep `Raw`, so read_timeline can still return the event
// verbatim rather than a lossy re-render of the named fields.
func DecodeEvent(arg any) (client.Event, error) {
	var raw []byte
	switch v := arg.(type) {
	case nil:
		return client.Event{}, errors.New("nil event")
	case client.Event:
		return v, nil
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return client.Event{}, err
		}
		raw = b
	}
	var evt client.Event
	if err := json.Unmarshal(raw, &evt); err != nil {
		return client.Event{}, err
	}
	return evt, nil
}

// errorMessage renders a connect_error payload the way `e?.message ?? 'connect
// error'` does: prefer a real message, never print an empty parenthetical.
func errorMessage(args []any) string {
	for _, a := range args {
		switch v := a.(type) {
		case nil:
			continue
		case error:
			if msg := v.Error(); msg != "" {
				return msg
			}
		case string:
			if v != "" {
				return v
			}
		case map[string]any:
			if msg, ok := v["message"].(string); ok && msg != "" {
				return msg
			}
		default:
			if s := fmt.Sprintf("%v", v); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return "connect error"
}

// --- the real socket -------------------------------------------------------

// DefaultDialer builds a real Socket.IO v4 client, using exactly the
// construction validated against the production gateway in phase0-result.md:
// WebSocket transport only (the Node original's `transports: ['websocket']`),
// handshake `auth: {token, slug}`, namespace joined off the manager.
func DefaultDialer(baseURL, namespace string, auth map[string]any) (Conn, error) {
	opts := sio.DefaultOptions()
	opts.SetTransports(engineTypes.NewSet(transports.WebSocket))
	opts.SetAuth(auth)

	manager := sio.NewManager(baseURL, opts)
	sock := manager.Socket(namespace, opts)
	if sock == nil {
		return nil, errors.New("socket.io: manager returned no socket")
	}
	return &socketConn{sock: sock}, nil
}

// socketConn adapts the library's *socket.Socket to Conn.
type socketConn struct {
	sock *sio.Socket

	once sync.Once
}

func (s *socketConn) On(event string, listener func(args ...any)) error {
	return s.sock.On(events.EventName(event), events.Listener(listener))
}

func (s *socketConn) Emit(event string, args ...any) error {
	return s.sock.Emit(event, args...)
}

func (s *socketConn) Disconnect() {
	s.once.Do(func() { s.sock.Disconnect() })
}

func (s *socketConn) Connected() bool { return s.sock.Connected() }
