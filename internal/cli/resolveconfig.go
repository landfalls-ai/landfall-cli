package cli

// resolveconfig.go — the joinable-config precedence shared by serve, join,
// note and leave, ported from bin/landfall.mjs's `envConfig` (91-102) and
// `resolveConfig` (104-154).

import (
	"context"
	"net/url"
	"os"
	"regexp"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/instance"
)

// defaultAgentLabel is what an edge agent narrates under when
// LANDFALL_AGENT_LABEL says nothing.
const defaultAgentLabel = "edge-agent"

// ticketQuery detects a share link that carries a join ticket — the guest
// path, where the ticket IS the credential and no login is involved.
var ticketQuery = regexp.MustCompile(`[?&]ticket=`)

// incidentPath matches a PLAIN incident URL (no ticket) for the OAuth path.
// Not anchored, exactly as the Node original's regex is not: the incident
// segment may sit under a longer path.
var incidentPath = regexp.MustCompile(`/o/([^/]+)/incidents/([^/]+)`)

func agentLabel() string {
	if label := os.Getenv("LANDFALL_AGENT_LABEL"); label != "" {
		return label
	}
	return defaultAgentLabel
}

// envConfig is the feature-012 environment configuration. It returns nil when
// incomplete — 021 allows a link or a lazy join instead, so a partial set of
// variables is "not configured", never an error.
func envConfig() *client.Config {
	baseURL := os.Getenv("LANDFALL_BASE_URL")
	if baseURL == "" {
		baseURL = instance.DefaultInstance().API
	}
	cfg := client.Config{
		BaseURL:    baseURL,
		Slug:       os.Getenv("LANDFALL_SLUG"),
		IncidentID: os.Getenv("LANDFALL_INCIDENT"),
		Token:      os.Getenv("LANDFALL_TOKEN"),
		AgentLabel: agentLabel(),
	}
	if cfg.Slug == "" || cfg.IncidentID == "" || cfg.Token == "" {
		return nil
	}
	return &cfg
}

// incidentTarget is a "which incident" answer with no credential attached.
type incidentTarget struct {
	slug       string
	incidentID string
}

// parseIncidentURL pulls slug + incident id out of a plain incident URL.
// A value that is not an absolute URL yields nothing, matching the Node
// original's `new URL(url)` throwing and being swallowed.
func parseIncidentURL(raw string) *incidentTarget {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil
	}
	m := incidentPath.FindStringSubmatch(u.Path)
	if m == nil {
		return nil
	}
	return &incidentTarget{slug: m[1], incidentID: m[2]}
}

// resolveConfig resolves a joinable config, in priority order:
//
//  1. a share link WITH a ticket → redeem it (guest / non-member path, 021);
//  2. explicit env config (LANDFALL_SLUG+LANDFALL_INCIDENT+LANDFALL_TOKEN, 012);
//  3. a cached login token + an incident target (a plain incident URL, or
//     LANDFALL_SLUG+LANDFALL_INCIDENT) → join as the authenticated member with
//     NO share link (024).
//
// Returns (nil, nil) when none apply — that is the normal "not configured"
// answer, and every caller decides for itself what to do about it. An error is
// returned only for a genuine failure (a ticket that would not redeem, an
// unusable instance address).
func resolveConfig(ctx context.Context, ui *UI, link string) (*client.Config, error) {
	label := agentLabel()
	baseURL := os.Getenv("LANDFALL_BASE_URL")

	// A share link is either the long form (a `?ticket=` query) or the short
	// form (https://<domain>/j/<code>, which deliberately carries no ticket in
	// the URL text — the code is an opaque server-side lookup key instead).
	// RedeemShareLink itself tells the two apart; this just decides whether to
	// call it at all.
	_, isShortLink := client.ParseShortLink(link)
	if link != "" && (ticketQuery.MatchString(link) || isShortLink) {
		cfg, err := client.RedeemShareLink(ctx, link, client.RedeemOptions{BaseURL: baseURL})
		if err != nil {
			return nil, err
		}
		cfg.AgentLabel = label
		ui.Log("magic link redeemed — incident %s (workspace %s).", cfg.IncidentID, cfg.Slug)
		return &cfg, nil
	}

	if env := envConfig(); env != nil {
		return env, nil
	}

	target := parseIncidentURL(link)
	if target == nil {
		if slug, id := os.Getenv("LANDFALL_SLUG"), os.Getenv("LANDFALL_INCIDENT"); slug != "" && id != "" {
			target = &incidentTarget{slug: slug, incidentID: id}
		}
	}
	if target == nil {
		return nil, nil
	}

	if token := auth.GetCachedAccessToken(ctx, nil); token != "" {
		// FR-014: a token is only good against the Landfall that minted it.
		// Without this check a session cached against one instance is sent
		// blindly to another and fails as an opaque 401, which reads as "your
		// session broke" rather than "you are pointed somewhere else". A
		// credential written before instances were recorded reads as unknown,
		// which counts as a mismatch rather than being assumed to match.
		resolved, err := instance.Resolve(instance.Options{})
		if err != nil {
			return nil, err
		}
		checked := resolved
		if baseURL != "" {
			checked.API = baseURL
		}
		if mismatch := auth.ExplainInstanceMismatch(checked); mismatch != "" {
			ui.Log("%s", mismatch)
			return nil, nil
		}
		ui.Log("authenticated — joining incident %s (workspace %s) with no share link.",
			target.incidentID, target.slug)

		api := baseURL
		if api == "" {
			api = instance.DefaultInstance().API
		}
		return &client.Config{
			BaseURL:    api,
			Slug:       target.slug,
			IncidentID: target.incidentID,
			Token:      token,
			AgentLabel: label,
		}, nil
	}

	// Feature 043 (T061, FR-055): an EXPIRED credential — including a legacy
	// Keycloak one this CLI can no longer refresh — produces an explicit
	// instruction, never a silent failure. A CLI that quietly stops working
	// during an incident is worse than one that says what to do.
	if expired := auth.ExplainExpiredCredential(); expired != "" {
		ui.Log("%s", expired)
	} else {
		ui.Log("not signed in — run `landfall login`, or paste a share link.")
	}
	return nil, nil
}
