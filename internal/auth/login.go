package auth

// login.go — the browser-handoff sign-in flow.
//
//	CLI                          browser                 Landfall web / API
//	 │ open ────────────────────► /cli-auth?nonce=…
//	 │                               ├─ authenticate via the org's branch
//	 │                               └─ POST /auth/cli-handoff ─► mint + STORE
//	 │                                                            a redeemable
//	 │                                                            grant
//	 │ poll ─── POST /auth/cli-handoff/poll {nonce} ─────────────► redeem
//	 │ ◄──────────────────────────────────────────── { accessToken, … }
//	 │ write ~/.config/landfall/credentials.json (0600)
//
// ── Why this is a POLL, not a loopback listener ───────────────────────────
// This used to bind a loopback HTTP server and have the BROWSER push the
// finished session to it. That mechanism needed three separate patches in one
// day and was still broken: Chrome's Private Network Access policy silently
// failed the push without an extra preflight header; Safari refused it
// regardless; and the actual cause, found in Safari's own Network panel, was
// that WebKit refuses to even ATTEMPT an https: page's fetch() to any http:
// target, loopback or not. No hostname choice fixes a categorical block.
//
// Rather than a fourth patch, the server ALSO stores a redeemable grant keyed
// by this same nonce, and the CLI retrieves it over an ordinary outbound HTTPS
// poll. There is no cross-origin request left for CORS, mixed-content, or
// Private Network Access to govern.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/instance"
	"github.com/pkg/browser"
)

// HandoffTimeout is how long to wait for the browser to complete the handoff.
// Generous: the browser side can involve typing a password and clicking through
// an SSO redirect.
const HandoffTimeout = 5 * time.Minute

// PollInterval is how often to poll /auth/cli-handoff/poll while waiting. Short
// enough that signing in feels immediate once the browser tab says "Command
// line connected"; long enough that a login storm from many teammates behind
// one office NAT stays well under the endpoint's per-IP rate limit.
const PollInterval = 1500 * time.Millisecond

// ErrHandoffTimeout is returned when the deadline passes with no session. Its
// message is asserted verbatim by the ported suite.
var ErrHandoffTimeout = errors.New("timed out waiting for the browser to complete sign-in")

// LoginOptions configures Login. The zero value signs in to the resolved
// instance with a real browser and a real HTTP client.
type LoginOptions struct {
	// OrgSlug, when set, pre-selects an organization in the web sign-in.
	OrgSlug string
	// URL is an explicit `--url` instance nomination, passed through to
	// instance.Resolve.
	URL string
	// Instance short-circuits resolution when the caller has already resolved
	// one. Resolving per-use is how a login could otherwise probe one origin
	// while polling another.
	Instance *instance.Instance
	// Client is the injectable HTTP surface for both the probe and the poll.
	Client Doer
	// OpenBrowser opens the handoff URL. nil means the real default browser;
	// tests supply a no-op so no window pops on a developer's machine or a CI
	// runner.
	OpenBrowser func(url string) error
	// PollInterval / PollTimeout override the defaults above (tests only).
	PollInterval time.Duration
	PollTimeout  time.Duration
}

// Log is a stderr logger. It never receives token text.
type Log func(msg string)

// Login signs in via the browser handoff and caches the result, returning the
// access token.
func Login(ctx context.Context, log Log, opts LoginOptions) (string, error) {
	if log == nil {
		log = func(string) {}
	}

	nonce, err := newNonce()
	if err != nil {
		return "", fmt.Errorf("generating a sign-in nonce: %w", err)
	}

	// Resolved ONCE, and used for every step below.
	var resolved instance.Instance
	if opts.Instance != nil {
		resolved = *opts.Instance
	} else {
		resolved, err = instance.Resolve(instance.Options{URL: opts.URL})
		if err != nil {
			return "", err
		}
	}

	// Preflight BEFORE the browser opens. The defect this replaced was not only
	// the wrong address, it was being handed a browser error page as the only
	// diagnosis. A timeout deliberately does not block: a strict check would
	// turn a slow link or a proxy into "the product is broken".
	switch reach := instance.Probe(ctx, resolved.Web, instance.ProbeOptions{Client: opts.Client}); reach {
	case instance.OK:
		// nothing to say
	case instance.Timeout:
		log(fmt.Sprintf("warning: %s did not answer quickly. Continuing anyway.", resolved.Web))
	default:
		return "", instance.NewUnreachableError(reach, resolved)
	}

	query := url.Values{"nonce": {nonce}}
	if opts.OrgSlug != "" {
		query.Set("org", opts.OrgSlug)
	}
	handoffURL := resolved.Web + "/cli-auth?" + query.Encode()

	log(fmt.Sprintf(
		"opening your browser to sign in… if it does not open, visit:\n%s\n"+
			"You will sign in the same way you sign in to Landfall on the web — with your "+
			"password, or through your organization’s identity provider.", handoffURL))
	openBrowser(opts.OpenBrowser, handoffURL)

	session, err := PollForHandoff(ctx, resolved.API, nonce, opts.OrgSlug, PollOptions{
		Client:   opts.Client,
		Interval: opts.PollInterval,
		Timeout:  opts.PollTimeout,
	})
	if err != nil {
		return "", err
	}

	session.Instance = &resolved
	if err := SaveSession(session); err != nil {
		return "", err
	}
	return session.AccessToken, nil
}

// newNonce mints the 256-bit value that ties the browser's handoff to this
// process. base64url, unpadded, so it survives a URL query with no escaping.
func newNonce() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// openBrowser is best-effort: on a headless box there is nothing to open, and
// the URL has already been printed for the user to open manually. A failure to
// launch a browser must never fail a sign-in that can still be completed by
// hand.
func openBrowser(open func(string) error, target string) {
	if open == nil {
		open = browser.OpenURL
	}
	_ = open(target)
}

// PollOptions configures PollForHandoff. The zero value uses the real client
// and the constants above.
type PollOptions struct {
	Client Doer
	// Interval is the wait between polls. Zero means the PollInterval default —
	// NOT "no wait", which would hammer the endpoint straight through its
	// per-IP rate limit the moment a caller passed a bare PollOptions{}. A
	// NEGATIVE value means no wait at all, and exists only so tests can drive
	// the loop without real sleeping.
	Interval time.Duration
	Timeout  time.Duration
}

// PollForHandoff polls POST /auth/cli-handoff/poll for the session the
// browser's own POST /auth/cli-handoff already stashed under this nonce.
//
// ── Why the nonce alone is enough ─────────────────────────────────────────
// It is the SAME nonce Login put in the URL it sent the browser to — a 256-bit
// value nobody else ever sees, so presenting it back is proof of having been
// the one who opened that link. The server enforces the rest: an exact-hash
// lookup only (no listing or enumeration is possible), and single use via an
// atomic conditional update, so a retried poll — a normal event; this loop has
// no way to know its previous request landed — can never redeem the same grant
// twice.
//
// A 404 means "not yet" and is indistinguishable, on purpose, from "never
// existed". A non-2xx, a network blip, and an unparseable body all get the
// identical treatment: keep polling until the deadline. One failed request is
// never a reason to give up early.
func PollForHandoff(ctx context.Context, apiURL, nonce, orgSlug string, opts PollOptions) (Session, error) {
	interval := opts.Interval
	switch {
	case interval == 0:
		interval = PollInterval
	case interval < 0:
		interval = 0
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = HandoffTimeout
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// The wait comes FIRST, matching the Node source: the browser has only
		// just been opened, so an immediate poll is guaranteed to miss.
		if interval > 0 {
			select {
			case <-ctx.Done():
				return Session{}, ctx.Err()
			case <-time.After(interval):
			}
		} else if ctx.Err() != nil {
			return Session{}, ctx.Err()
		}

		body, ok := postJSON(ctx, opts.Client, apiURL+"/auth/cli-handoff/poll",
			map[string]string{"nonce": nonce})
		if !ok || body.AccessToken == "" {
			continue
		}

		session := Session{
			AccessToken: body.AccessToken,
			ExpiresAt:   body.ExpiresAt,
		}
		// Present on every server new enough to mint one. Tolerated as absent
		// against an older API (graceful degradation — the credential is then
		// simply not refreshable) rather than rejecting the handoff over it.
		if body.RefreshToken != "" {
			refresh := body.RefreshToken
			session.RefreshToken = &refresh
		}
		// The server's answer wins; the caller-supplied slug is only a fallback
		// for a response that omits one.
		switch {
		case body.OrgSlug != nil && *body.OrgSlug != "":
			slug := *body.OrgSlug
			session.OrgSlug = &slug
		case orgSlug != "":
			slug := orgSlug
			session.OrgSlug = &slug
		}
		return session, nil
	}
	return Session{}, ErrHandoffTimeout
}
