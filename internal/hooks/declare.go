// declare.go — POST a confirmed classified intent to Landfall (#233 → #231). A
// Go port of `src/hooks/declare.mjs`.
//
// The endpoint is `POST /o/:slug/edge/declared-intent`, whose body schema is
// `.strict()`: an unknown key is a 400, not a silent strip. That is deliberate
// on the server's side and it is why this file sends the intent object exactly
// as IntentFor built it, with nothing added — no command line, no hostname, no
// local paths, not even a client version. If a field is ever wanted there, it
// has to be added to the contract first, which is the intended friction.
//
// The response is `{decision: 'attach'|'open'|'none', incidentId?, workspaceUrl?,
// reason?}` — what to DO about the intent is the server's call (#235), not this
// hook's. The hook reports and relays; it never decides that a war room should
// exist.
//
// WHY THIS TALKS TO LANDFALL DIRECTLY, AND MUST KEEP DOING SO. Every other hook
// in this package reaches the running session over the local query socket
// (socket.go), which deliberately has NO write verb — it must never become a
// second way to ACT in a war room. `pre-tool-use` is the one hook with something
// to say outward rather than a question to ask inward, so it resolves its OWN
// cached credential (internal/auth) and posts. Routing it through the socket
// instead would mean adding exactly the write verb that boundary exists to
// forbid.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/instance"
)

// DeclareTimeout is how long to wait on the API before giving up. A hook must
// not hang a shell.
const DeclareTimeout = 5 * time.Second

// Doer is the injectable HTTP surface, matching internal/auth's.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Target is where to send an intent and as whom, or why it cannot be sent.
type Target struct {
	OK bool
	// Reason is meant for a human, and is only set when OK is false.
	Reason  string
	BaseURL string
	Slug    string
	Token   string
}

// TargetDeps are ResolveTarget's injectable effects.
type TargetDeps struct {
	// Env reads environment variables. Nil means the process environment.
	Env func(string) string
	// ReadToken is the cached access token. Nil means auth.GetCachedAccessToken.
	ReadToken func(ctx context.Context) string
	// ReadOrgSlug is the cached organization. Nil means auth.GetCachedOrgSlug.
	ReadOrgSlug func() string
}

// ResolveTarget resolves where to send an intent and as whom.
//
// Resolved BEFORE the engineer is prompted: asking someone to approve a report
// that cannot be sent wastes the one interruption this feature is allowed.
func ResolveTarget(ctx context.Context, deps TargetDeps) Target {
	getenv := deps.Env
	if getenv == nil {
		getenv = os.Getenv
	}
	readToken := deps.ReadToken
	if readToken == nil {
		readToken = func(ctx context.Context) string { return auth.GetCachedAccessToken(ctx, nil) }
	}
	readOrgSlug := deps.ReadOrgSlug
	if readOrgSlug == nil {
		readOrgSlug = auth.GetCachedOrgSlug
	}

	token := readToken(ctx)
	if token == "" {
		return Target{OK: false, Reason: "not signed in — run `landfall login` (nothing was reported)"}
	}
	slug := getenv("LANDFALL_SLUG")
	if slug == "" {
		slug = readOrgSlug()
	}
	if slug == "" {
		return Target{OK: false, Reason: "no organization on the cached session — set LANDFALL_SLUG (nothing was reported)"}
	}

	// No private default here. This file used to carry its own
	// `http://localhost:3001`, one of four separate answers to "where is
	// Landfall" that between them made the published CLI unusable.
	// instance.DefaultInstance is the shared fallback, never a locally-invented
	// one.
	baseURL := getenv("LANDFALL_BASE_URL")
	if baseURL == "" {
		baseURL = instance.DefaultInstance().API
	}
	return Target{
		OK:      true,
		BaseURL: strings.TrimRight(baseURL, "/"),
		Slug:    slug,
		Token:   token,
	}
}

// DeclaredIntentOutcome is the server's answer, or why there was none.
type DeclaredIntentOutcome struct {
	Decision     string `json:"decision"`
	IncidentID   string `json:"incidentId,omitempty"`
	WorkspaceURL string `json:"workspaceUrl,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// SendIntent sends one declared intent.
//
// A failure here is reported to the engineer on stderr and never raised: the
// command they are actually trying to run is none of this hook's business, and
// an unreachable API — or an organization that has not enabled the feature, in
// which case #231 answers 404 by design — must not turn into an error in the
// middle of their turn.
//
// A zero timeout means DeclareTimeout; a nil doer means a plain http.Client.
func SendIntent(ctx context.Context, target Target, intent Intent, doer Doer, timeout time.Duration) DeclaredIntentOutcome {
	if timeout <= 0 {
		timeout = DeclareTimeout
	}
	if doer == nil {
		doer = &http.Client{Timeout: timeout}
	}
	endpoint := target.BaseURL + "/o/" + url.PathEscape(target.Slug) + "/edge/declared-intent"

	body, err := json.Marshal(intent)
	if err != nil {
		return DeclaredIntentOutcome{Decision: "none", Reason: "could not reach Landfall (" + err.Error() + ")"}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return DeclaredIntentOutcome{Decision: "none", Reason: "could not reach Landfall (" + err.Error() + ")"}
	}
	req.Header.Set("authorization", "Bearer "+target.Token)
	req.Header.Set("content-type", "application/json")

	res, err := doer.Do(req)
	if err != nil {
		why := err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			why = "timed out"
		}
		return DeclaredIntentOutcome{Decision: "none", Reason: "could not reach Landfall (" + why + ")"}
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 200 || res.StatusCode > 299 {
		// 404 is the shipped state for an organization that has not enabled
		// auto-attach — #231 answers it with the same body as an unknown org, on
		// purpose, so there is nothing to distinguish and nothing to report but
		// the status.
		return DeclaredIntentOutcome{
			Decision: "none",
			Reason:   "declared intent not accepted (HTTP " + strconv.Itoa(res.StatusCode) + ")",
		}
	}

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return DeclaredIntentOutcome{Decision: "none", Reason: "could not reach Landfall (" + err.Error() + ")"}
	}
	var out DeclaredIntentOutcome
	if err := json.Unmarshal(raw, &out); err != nil {
		return DeclaredIntentOutcome{Decision: "none", Reason: "could not reach Landfall (" + err.Error() + ")"}
	}
	return out
}

// DescribeOutcome is one line of feedback for the engineer, per decision. Never
// echoes the intent.
func DescribeOutcome(outcome DeclaredIntentOutcome) string {
	switch outcome.Decision {
	case "attach":
		return "joined the matching war room" + workspaceSuffix(outcome.WorkspaceURL)
	case "open":
		return "opened a provisional war room" + workspaceSuffix(outcome.WorkspaceURL)
	}
	if outcome.Reason != "" {
		return "no war room opened (" + outcome.Reason + ")"
	}
	return "no war room opened"
}

func workspaceSuffix(workspaceURL string) string {
	if workspaceURL == "" {
		return ""
	}
	return " — " + workspaceURL
}
