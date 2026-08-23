package cli

// serve.go — `landfall serve [--link URL]`, a port of bin/landfall.mjs:441-490.
//
// This is the default command (a bare `landfall`, a bare `https://…` argument,
// or an unrecognized command name all land here — root.go's defaultCommand
// rule), and the one that ties every other package together:
//
//	resolveconfig  which incident, and with what credential
//	session        the in-memory bridge session the whole process is built on
//	tools          the 15 MCP tools, all built over that session
//	mcp            the JSON-RPC stdio loop those tools are served through
//	hooks          the doorbell an idle session's FileChanged hook watches, and
//	               the query socket short-lived hook processes ask
//
// THE STDOUT RULE. stdout is the MCP wire and nothing else, so `ui.Out` is set
// to io.Discard the moment this command starts: any incidental `ui.Outf` here
// would be a protocol violation, not a log line (ui.go). The wire itself is
// written through the package's own `stdout` writer, exactly as hookevents.go
// writes its host protocol — the wire format is not an incidental write and
// must not share that door.
//
// NOT JOINING IS NORMAL. A `serve` started outside a resolved config keeps
// running un-joined (bin/landfall.mjs:463): the whole point of the zero-config
// MCP entry is that a coding agent calls `join_war_room` itself later, over the
// very stdio loop this command is about to start. Exiting here would make that
// impossible.

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/mcp"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/session"
	"github.com/landfalls-ai/landfall-cli/internal/tools"
	"github.com/spf13/cobra"
)

// buildVersion is the version this process reports as the MCP server's own —
// the `serverInfo.version` an MCP client shows for the connection.
//
// bin/landfall.mjs:487 hardcodes `{ name: 'landfall', version: '0.2.0' }`,
// which had already drifted from package.json by three minor releases. The Go
// build takes it from the linker instead:
//
//	go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/landfall
//
// cmd/landfall's `version` var is that `-X` target; main hands it here through
// SetVersion before any command runs. An un-stamped build (`go run`, `go test`,
// a plain `go build`) reports "dev", which is a true statement about it.
var buildVersion = "dev"

// SetVersion records the linker-stamped build version. Called once, from main,
// before Execute. An empty value is ignored so an un-stamped build keeps the
// honest "dev" default rather than reporting an empty version string.
func SetVersion(v string) {
	if v != "" {
		buildVersion = v
	}
}

// leaveTimeout bounds the best-effort `leave` on the way out. The context that
// carried the process is already cancelled by then (that is what got us here),
// so the leave needs its own — and it must not hang a shutdown the user asked
// for by pressing Ctrl-C.
const leaveTimeout = 10 * time.Second

// --- the realtime seam ------------------------------------------------------

// liveWatcher subscribes to a joined room's live event stream. It is the
// STUBBED half of keepLive (bin/landfall.mjs:164's `watchIncident`): the real
// Socket.IO client is internal/realtime, which lands with tasks.md T057-T058.
//
// The seam is here rather than in serve's body so T058 is a one-line swap of
// the default in serveOptions.withDefaults, with no change to the enqueue /
// doorbell / logging policy below — that policy is `serve`'s, not the
// transport's, and it is already correct.
//
// Watch returns a stop function; it must be safe to call exactly once, and a
// watcher that cannot connect returns a no-op rather than an error, matching
// today's degrade-silently behavior (edge-bridge.test.mjs:679).
type liveWatcher interface {
	Watch(ctx context.Context, cfg client.Config, onEvent func(client.Event)) (stop func())
}

// noopWatcher is the default until T057 lands: no live push, so a joined
// session finds out what happened at its next `get_updates`. Everything else
// about serve — the tools, the socket, the doorbell plumbing — works exactly
// the same with or without it.
type noopWatcher struct{}

func (noopWatcher) Watch(context.Context, client.Config, func(client.Event)) func() {
	return func() {}
}

// --- the presence + live half (keepLive) ------------------------------------

// liveSession is bin/landfall.mjs's keepLive (line 162): the presence heartbeat
// and the live room watch, started together and stopped together.
//
// It exists as a struct rather than a returned closure because `serve` restarts
// it on every join — the agent may call `join_war_room` for a second incident
// mid-session, and Node's `stopLive?.()` before re-assigning (line 451) is the
// same rule spelled with a mutex, since a join arrives on an MCP handler
// goroutine while the shutdown path may be running on another.
type liveSession struct {
	ui       *UI
	doorbell *hooks.Doorbell
	watcher  liveWatcher
	interval time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	unwatch func()
}

// start begins (or restarts) presence + live watch for a freshly joined room.
func (l *liveSession) start(ctx context.Context, sess *session.Session, cl session.EdgeClient, cfg client.Config) {
	l.stop()

	liveCtx, cancel := context.WithCancel(ctx)
	go beat(liveCtx, cl, l.interval)

	unwatch := l.watcher.Watch(liveCtx, cfg, func(evt client.Event) {
		// #227: ring the doorbell on the 0 → non-empty EDGE only. A session that
		// already owes context will be shown this event too when the bell it has
		// yet to answer is answered; re-ringing on every event would wake an idle
		// agent once per message in a busy room. hooks.RingOnEdge owns that guard
		// so serve cannot get it subtly wrong (bin/landfall.mjs:167-173).
		wasEmpty := len(sess.Pending()) == 0
		queued := sess.EnqueueEvent(liveCtx, evt)
		if l.doorbell != nil {
			l.doorbell.RingOnEdge(wasEmpty, queued, len(sess.Pending()))
		}
		// The stderr line only reaches a human who happens to be watching the
		// terminal; the agent is reached in-band, on its next tool result.
		l.ui.Log("%s", narrate.FormatEventLine(evt))
	})

	l.mu.Lock()
	l.cancel, l.unwatch = cancel, unwatch
	l.mu.Unlock()
}

// stop ends presence + live watch. Idempotent — shutdown calls it, and so does
// the next join.
func (l *liveSession) stop() {
	l.mu.Lock()
	cancel, unwatch := l.cancel, l.unwatch
	l.cancel, l.unwatch = nil, nil
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if unwatch != nil {
		unwatch()
	}
}

// beat is the heartbeat half of keepLive. A failed beat is swallowed
// (`.catch(() => {})`, bin/landfall.mjs:163): one dropped request during an
// incident is not a reason to drop the seat.
//
// Each beat gets its own context rather than the live one, so a beat already in
// flight when the room is left is not cancelled halfway — but it is still
// bounded, so nothing can outlive the process meaningfully.
func beat(ctx context.Context, cl session.EdgeClient, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			beatCtx, cancel := context.WithTimeout(context.Background(), interval)
			_ = cl.Heartbeat(beatCtx, "investigating")
			cancel()
		}
	}
}

// --- the command ------------------------------------------------------------

// serveOptions are serve's injectable edges. The zero value is the production
// configuration; every field exists so the command can be exercised end to end
// against pipes and a fake client, with no network, no real stdio and no
// process-wide state.
type serveOptions struct {
	// In / Out are the MCP wire. Nil means the real process streams.
	In  io.Reader
	Out io.Writer
	// Resolve answers "which incident, with what credential". Nil means the
	// shared resolveConfig join/note/leave use.
	Resolve func(ctx context.Context, ui *UI, link string) (*client.Config, error)
	// NewClient builds the edge client for a config — both for the config
	// resolved at startup and for one the agent redeems later through
	// join_war_room. Nil means the real HTTP client.
	NewClient session.ClientFactory
	// Redeem turns a share link into a config inside join_war_room. Nil means
	// the real HTTP redemption, honoring LANDFALL_BASE_URL.
	Redeem session.RedeemFunc
	// Workspace scopes BOTH the doorbell and the hook query socket. Its zero
	// value is this process's cwd and environment, which is what production
	// wants; one field redirects both in a test.
	Workspace hooks.Workspace
	// Watcher is the realtime seam. Nil means noopWatcher until T058.
	Watcher liveWatcher
	// Version is the MCP serverInfo version. Empty means the linker-stamped
	// buildVersion.
	Version string
	// HeartbeatInterval is keepLive's beat. Zero means HEARTBEAT_MS.
	HeartbeatInterval time.Duration
}

func (o serveOptions) withDefaults() serveOptions {
	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Out == nil {
		// The package's own writer, not ui.Out — ui.Out is io.Discard for this
		// command by design, and the wire is not an incidental write.
		o.Out = stdout
	}
	if o.Resolve == nil {
		o.Resolve = resolveConfig
	}
	if o.NewClient == nil {
		o.NewClient = func(cfg client.Config) session.EdgeClient { return client.New(cfg, nil) }
	}
	if o.Redeem == nil {
		// Node threads `baseUrl: process.env.LANDFALL_BASE_URL` into the session
		// so a later join_war_room redeems against the same instance the process
		// was pointed at (bin/landfall.mjs:449, src/tools.mjs:332). The session
		// package's own default does not read the environment, so serve supplies
		// it here rather than leaving a self-hosted instance's share link being
		// redeemed against whatever host the link happens to name.
		o.Redeem = func(ctx context.Context, shareURL string) (client.Config, error) {
			return client.RedeemShareLink(ctx, shareURL, client.RedeemOptions{
				BaseURL: os.Getenv("LANDFALL_BASE_URL"),
			})
		}
	}
	if o.Watcher == nil {
		o.Watcher = noopWatcher{}
	}
	if o.Version == "" {
		o.Version = buildVersion
	}
	if o.HeartbeatInterval == 0 {
		o.HeartbeatInterval = heartbeatInterval
	}
	return o
}

func newServeCommand(ui *UI, link string) *cobra.Command {
	c := newCommand(ui, "serve", func(cmd *cobra.Command, _ []string) error {
		return runServe(cmdContext(cmd), ui, link, serveOptions{})
	})
	// `--link` has already been spliced out of argv by parseArgs, and a bare
	// positional URL has already become the link, so there is nothing here for
	// Cobra to parse — and an unrecognized command name reaching serve through
	// the defaultCommand rule may carry anything at all, including things that
	// look like flags.
	c.DisableFlagParsing = true
	return c
}

// runServe is the whole command. It blocks until stdin closes or a signal
// arrives, then shuts down in bin/landfall.mjs:477-481's order — live watch,
// hook socket, leave — and returns nil, which main turns into exit 0.
func runServe(ctx context.Context, ui *UI, link string, opts serveOptions) error {
	opts = opts.withDefaults()

	// From here on stdout belongs to the MCP wire. Make a stray ui.Outf inert
	// rather than let it corrupt a JSON-RPC stream the agent is parsing.
	ui.Out = io.Discard

	// SIGINT/SIGTERM cancel everything below and exit 0. Node installs the same
	// two handlers and calls process.exit(0) from them (bin/landfall.mjs:
	// 476-483); the exit code is returned up to main here instead, so nothing
	// can be left unflushed.
	stopCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// #227: the doorbell an idle session's FileChanged hook watches. It carries
	// a timestamp, a pid and a count — never room content; the events themselves
	// stay on the query socket and out of the user's repository.
	doorbell := hooks.NewDoorbell(hooks.DoorbellOptions{
		Cwd: opts.Workspace.Dir(),
		Log: func(msg string) { ui.Log("%s", msg) },
	})

	live := &liveSession{ui: ui, doorbell: doorbell, watcher: opts.Watcher, interval: opts.HeartbeatInterval}

	sess := session.New(session.Options{
		AgentLabel:    agentLabel(),
		Redeem:        opts.Redeem,
		ClientFactory: opts.NewClient,
		// A join the AGENT performs, mid-session, over MCP. The startup join
		// below does the same three things inline, because there is no session
		// to hand this callback yet at that point.
		OnJoined: func(s *session.Session, cfg client.Config) error {
			cl := s.Client()
			if cl == nil {
				return nil
			}
			live.start(stopCtx, s, cl, cfg)
			ui.Log(`joined incident %s as "%s" (instance %s).`, cfg.IncidentID, s.AgentLabel(), cl.AgentInstanceID())
			return nil
		},
	})

	cfg, err := opts.Resolve(stopCtx, ui, link)
	if err != nil {
		return err
	}
	if cfg != nil {
		cl := opts.NewClient(*cfg)
		if _, err := cl.Join(stopCtx); err != nil {
			return err
		}
		sess.SetClient(cl)
		live.start(stopCtx, sess, cl, *cfg)
		// Literal quotes rather than %q, which would escape a non-ASCII agent
		// label this line is meant to read back verbatim.
		ui.Log(`joined incident %s as "%s" (instance %s).`, cfg.IncidentID, cfg.AgentLabel, cl.AgentInstanceID())
	} else {
		ui.Log("not joined yet — the agent should call join_war_room with a Landfall share link.")
	}

	// The local query socket (#225): lifecycle hooks are separate, short-lived
	// processes and cannot reach this process's pending queue or cursor. Binding
	// here is what makes them answerable — no separate daemon, and best-effort:
	// StartHookSocket returns nil (having already logged why) when binding is
	// impossible, and a serve process that cannot offer the socket must still
	// serve MCP. Deliberately NOT treated as an error here.
	sock := hooks.StartHookSocket(stopCtx, sess, hooks.StartOptions{
		Workspace: opts.Workspace,
		Consume:   func(_ hooks.SocketSession, upTo int64) int64 { return sess.ConsumeUpTo(upTo) },
		Log:       func(msg string) { ui.Log("%s", msg) },
	})
	if sock != nil {
		ui.Log("hook query socket at %s", sock.SocketPath)
	}

	ui.Log("MCP stdio server ready — connect your agent. Every tool call narrates to the war room.")
	serveErr := serveStdio(stopCtx, opts.In, opts.Out, mcp.Options{
		Tools:        tools.Build(sess),
		ServerInfo:   &mcp.ServerInfo{Name: "landfall", Version: opts.Version},
		Instructions: tools.EdgeAgentInstructions,
	})

	shutdown(ctx, live, sock, sess)

	// A cancelled context IS the shutdown path, not a failure: Node's handler
	// exits 0 from the signal. Anything else (a broken stdout, an unreadable
	// stdin) is a real error and surfaces as one.
	if errors.Is(serveErr, context.Canceled) {
		return nil
	}
	return serveErr
}

// shutdown is bin/landfall.mjs:477-481, in the same order: stop presence and
// the live watch, close the query socket, then leave the room. Every step is
// best-effort — a shutdown that fails partway must still reach the next step.
//
// The leave gets a FRESH context: the one that carried the process is already
// cancelled by the time a signal brings us here, and a leave sent on a
// cancelled context never reaches the server, which would leave a ghost
// participant in the room for the server's own presence timeout.
func shutdown(ctx context.Context, live *liveSession, sock *hooks.BoundSocket, sess *session.Session) {
	live.stop()
	if sock != nil {
		_ = sock.Close()
	}
	cl := sess.Client()
	if cl == nil {
		return
	}
	leaveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaveTimeout)
	defer cancel()
	_ = cl.Leave(leaveCtx)
}

// serveStdio runs the MCP loop and makes it interruptible.
//
// mcp.Serve blocks in a read on `in`, and a read on a terminal or a pipe cannot
// be cancelled by a context — checking ctx between frames (which mcp.Serve does)
// only helps a stream that is still producing them. So the loop runs on its own
// goroutine and this returns the moment EITHER it finishes or the context is
// done.
//
// The abandoned goroutine is deliberate and bounded: the only thing left to
// happen after this returns is the shutdown sequence and process exit, and the
// goroutine holds nothing a shutdown step needs. A "clean" alternative would
// mean closing os.Stdin out from under a read, which is a different and worse
// hazard.
func serveStdio(ctx context.Context, in io.Reader, out io.Writer, opts mcp.Options) error {
	done := make(chan error, 1)
	go func() { done <- mcp.Serve(ctx, in, out, opts) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return nil
	}
}
