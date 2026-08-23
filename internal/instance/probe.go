package instance

// ── Reachability ──────────────────────────────────────────────────────────
//
// The reported defect this whole package exists for was not only the wrong
// address. It was that the CLI printed one, opened a browser, and reported
// nothing, leaving a browser error page as the only diagnosis. So failure is
// CLASSIFIED, not uniform: a uniform "could not connect" would trade one
// unhelpful message for another.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// DefaultProbeTimeout bounds the preflight so it never dominates the sign-in
// budget.
const DefaultProbeTimeout = 3 * time.Second

// Reachability classifies a probe result.
//
// There is deliberately no NOT_LANDFALL member. The Node source declared one
// and wrote a message for it, but nothing ever produced it — `probe()` returns
// OK for ANY HTTP answer, because distinguishing "Landfall" from "some other
// server" via a HEAD of / would be guessing, and the real request that follows
// settles it properly. Carrying a status that cannot occur into the port would
// have meant porting dead code and a dead message (tasks.md T038 note / OD-3).
type Reachability string

const (
	// OK: something is listening and speaking HTTP.
	OK Reachability = "ok"
	// Unresolved: the hostname did not resolve (DNS).
	Unresolved Reachability = "unresolved"
	// Refused: a remote host actively refused the connection.
	Refused Reachability = "refused"
	// LocalDead: a loopback address refused the connection — i.e. a local
	// development server that is not running. Named as itself rather than as a
	// generic connection error, because that is the exact case that produced a
	// dead end for the operator who reported the original bug.
	LocalDead Reachability = "local-dead"
	// Timeout: no answer in time. Deliberately NOT fatal to the caller (see
	// DescribeFailure).
	Timeout Reachability = "timeout"
)

// Doer is the injectable HTTP surface. Every network call in this CLI goes
// through one so it is testable without a real network — the same reason the
// Node source injects `fetchImpl` throughout its own suite.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ProbeOptions configures Probe. The zero value uses a plain http.Client and
// DefaultProbeTimeout.
type ProbeOptions struct {
	Client  Doer
	Timeout time.Duration
}

// Probe classifies an address's reachability. It never returns an error: an
// unclassifiable failure is Refused, which is the most actionable of the
// remaining options.
//
// A HEAD of the origin root is enough — any HTTP answer proves something is
// listening and speaking HTTP.
func Probe(ctx context.Context, address string, opts ProbeOptions) Reachability {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return Unresolved
	}
	// Probe the origin root, never whatever path the address happened to carry.
	origin := u.Scheme + "://" + u.Host + "/"

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, origin, nil)
	if err != nil {
		return Refused
	}

	res, err := client.Do(req)
	if err == nil {
		// Drain and close so the connection can be reused; a HEAD has no body
		// worth reading.
		_ = res.Body.Close()
		return OK
	}
	return classify(err, u.Hostname())
}

// classify maps a Go transport error onto the same five buckets the Node
// source derived from libuv's error codes (ENOTFOUND/EAI_AGAIN → unresolved,
// ECONNREFUSED → refused-or-local-dead, the request 'timeout' event → timeout).
func classify(err error, hostname string) Reachability {
	// DNS first: a DNS failure that is also a timeout is still a name-resolution
	// problem, and saying "unresolved" points at the right fix. This mirrors the
	// Node source treating EAI_AGAIN (a temporary DNS failure) as UNRESOLVED
	// rather than as a timeout.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return Unresolved
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return Timeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return Timeout
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		if isLoopback(hostname) {
			return LocalDead
		}
		return Refused
	}
	return Refused
}

// fixes is the two-branch remedy appended to every failure message: the
// self-hosted case and the local-development case. Both are runnable as
// written — a message that names a problem without a command that fixes it is
// what made the original bug a dead end.
const fixes = "\n  If your organization runs its own Landfall:\n" +
	"      landfall login --url https://landfall.example.com --save\n" +
	"  If you are running Landfall locally:\n" +
	"      LANDFALL_WEB_URL=http://localhost:5173 LANDFALL_BASE_URL=http://localhost:3001 landfall login"

// DescribeFailure turns a classification into the message contract: WHAT was
// tried, WHY it failed, and a command that FIXES it. All three are required.
//
// Returns "" when there is nothing to say — reachable, or an ambiguous timeout
// the caller should WARN about but not block on. A strict timeout check would
// turn a slow link, a proxy, or a captive portal into "the product is broken",
// which is a worse failure than the one this replaced.
func DescribeFailure(status Reachability, inst Instance) string {
	switch status {
	case Unresolved:
		return fmt.Sprintf("Could not reach Landfall at %s\n  That address did not resolve.%s", inst.Web, fixes)
	case Refused:
		return fmt.Sprintf("Could not reach Landfall at %s\n  The connection was refused.%s", inst.Web, fixes)
	case LocalDead:
		return fmt.Sprintf(
			"Could not reach Landfall at %s\n"+
				"  That is a local development address, and nothing is listening on it.\n"+
				"  If you meant to use the hosted Landfall, clear the local setting:\n"+
				"      unset LANDFALL_WEB_URL LANDFALL_BASE_URL\n"+
				"      landfall instance reset%s", inst.Web, fixes)
	default:
		return ""
	}
}

// ExitUnreachable is the exit code for "the instance is wrong or unreachable",
// kept distinct from a general failure (1) so scripts and onboarding flows can
// tell "wrong address" from "wrong credentials" (contracts/cli-commands.md).
const ExitUnreachable = 2

// UnreachableError carries its own exit code, mirroring the Node source's
// `error.exitCode ?? 2` convention: the top-level handler reads the code OFF
// the error rather than hardcoding 2, so a future caller can raise a different
// one without touching the dispatcher.
type UnreachableError struct {
	Msg      string
	ExitCode int
	Status   Reachability
}

func (e *UnreachableError) Error() string { return e.Msg }

// Unreachable identifies this error class to the top-level handler without it
// needing a type assertion against every package.
func (e *UnreachableError) Unreachable() bool { return true }

// NewUnreachableError builds the error a command should return when a probe
// says the instance cannot be reached.
func NewUnreachableError(status Reachability, inst Instance) *UnreachableError {
	return &UnreachableError{
		Msg:      DescribeFailure(status, inst),
		ExitCode: ExitUnreachable,
		Status:   status,
	}
}
