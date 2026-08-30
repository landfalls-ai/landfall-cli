package client

// link.go — the magic link (feature 021), ported from `src/link.mjs`. A
// war-room member shares
//
//	https://<landfall>/o/<slug>/incidents/<id>/agent?ticket=<single-use-jwt>
//
// and ANY teammate's MCP-capable agent turns it into a live investigation
// seat: parse → redeem the ticket (POST edge/redeem, the ticket IS the
// credential) → receive a short-lived edge session token scoped to exactly
// that incident. Transport-injectable, so it is testable without a network.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ShareLink is a parsed agent share link.
type ShareLink struct {
	BaseURL    string
	Slug       string
	IncidentID string
	Ticket     string
}

var sharePath = regexp.MustCompile(`^/o/([^/]+)/incidents/([^/]+)/agent/?$`)

// ShortLink is the short join-link form: https://<domain>/j/<code>. Unlike
// ShareLink, the code carries no org/incident/ticket data of its own — it is
// an opaque server-side lookup key, so the URL a human copies stays short and
// carries no instructions or credential material in its own text. Everything
// the old long link put in the URL (slug, incident id, single-use ticket) is
// resolved server-side by /j/<code>/redeem instead.
type ShortLink struct {
	BaseURL string
	Code    string
}

var shortLinkPath = regexp.MustCompile(`^/j/([A-Za-z0-9_-]+)/?$`)

// ParseShortLink recognizes the short join-link form. ok is false for
// anything else (including a long ShareLink), so callers can try both forms
// without ParseShortLink itself needing to produce an error message.
func ParseShortLink(raw string) (ShortLink, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ShortLink{}, false
	}
	m := shortLinkPath.FindStringSubmatch(u.Path)
	if m == nil {
		return ShortLink{}, false
	}
	return ShortLink{BaseURL: u.Scheme + "://" + u.Host, Code: m[1]}, true
}

// ParseShareLink splits an agent share link into its parts. Errors on
// anything else.
func ParseShareLink(raw string) (ShareLink, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ShareLink{}, fmt.Errorf("not a Landfall agent share link: %s", raw)
	}
	m := sharePath.FindStringSubmatch(u.Path)
	if m == nil {
		return ShareLink{}, fmt.Errorf("not a Landfall agent share link (expected /o/<slug>/incidents/<id>/agent)")
	}
	ticket := u.Query().Get("ticket")
	if ticket == "" {
		return ShareLink{}, fmt.Errorf("share link is missing its join ticket — ask for a fresh one")
	}
	return ShareLink{
		BaseURL:    u.Scheme + "://" + u.Host,
		Slug:       m[1],
		IncidentID: m[2],
		Ticket:     ticket,
	}, nil
}

// RedeemOptions overrides the link's own origin (dev/tests where the web and
// API origins differ) and injects the transport.
type RedeemOptions struct {
	BaseURL string
	Doer    Doer
}

type redeemResponse struct {
	Token        string `json:"token"`
	HumanActorID string `json:"humanActorId"`
	// Slug and IncidentID are only ever populated by the short-link redeem
	// response — the long-link path already knows both from the URL itself
	// and ignores these fields.
	Slug       string `json:"slug"`
	IncidentID string `json:"incidentId"`
}

// RedeemShareLink redeems a share link for an incident-scoped edge session,
// returning the full bridge config. Accepts either link form — the short
// https://<domain>/j/<code> form is tried first since it is the one minted
// today; the long /o/<slug>/incidents/<id>/agent?ticket=... form still works
// for a link minted before this CLI version, or a stale copy.
func RedeemShareLink(ctx context.Context, shareURL string, opts RedeemOptions) (Config, error) {
	if short, ok := ParseShortLink(shareURL); ok {
		return redeemShortLink(ctx, short, opts)
	}
	parsed, err := ParseShareLink(shareURL)
	if err != nil {
		return Config{}, err
	}
	base := parsed.BaseURL
	if opts.BaseURL != "" {
		base = opts.BaseURL
	}
	base = strings.TrimSuffix(base, "/")

	doer := opts.Doer
	if doer == nil {
		doer = http.DefaultClient
	}

	endpoint := base + "/o/" + parsed.Slug + "/incidents/" + parsed.IncidentID + "/edge/redeem"
	decoded, err := postRedeem(ctx, doer, endpoint, map[string]any{"ticket": parsed.Ticket})
	if err != nil {
		return Config{}, err
	}
	return Config{
		BaseURL:      base,
		Slug:         parsed.Slug,
		IncidentID:   parsed.IncidentID,
		Token:        decoded.Token,
		HumanActorID: decoded.HumanActorID,
	}, nil
}

// redeemShortLink is RedeemShareLink's short-link counterpart. The code alone
// is the credential — there is no separate ticket, and no slug/incident id to
// echo back from the URL, so the server's response has to carry those two
// back (unlike the long-link path, which already has them from the URL).
func redeemShortLink(ctx context.Context, short ShortLink, opts RedeemOptions) (Config, error) {
	base := short.BaseURL
	if opts.BaseURL != "" {
		base = opts.BaseURL
	}
	base = strings.TrimSuffix(base, "/")

	doer := opts.Doer
	if doer == nil {
		doer = http.DefaultClient
	}

	endpoint := base + "/j/" + short.Code + "/redeem"
	decoded, err := postRedeem(ctx, doer, endpoint, nil)
	if err != nil {
		return Config{}, err
	}
	return Config{
		BaseURL:      base,
		Slug:         decoded.Slug,
		IncidentID:   decoded.IncidentID,
		Token:        decoded.Token,
		HumanActorID: decoded.HumanActorID,
	}, nil
}

// postRedeem is the HTTP round-trip shared by both link forms: POST, decode
// the redemption response, and turn a non-2xx into the same actionable error
// either link would want to surface (an expired/already-used ticket or code
// both fail through the same edge redemption pipeline server-side).
func postRedeem(ctx context.Context, doer Doer, endpoint string, payload map[string]any) (redeemResponse, error) {
	var reqBody io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return redeemResponse{}, err
		}
		reqBody = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reqBody)
	if err != nil {
		return redeemResponse{}, err
	}
	req.Header.Set("content-type", "application/json")

	res, err := doer.Do(req)
	if err != nil {
		return redeemResponse{}, err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 200 || res.StatusCode > 299 {
		hint := ""
		if res.StatusCode == http.StatusGone {
			hint = " — the link expired or was already used; ask for a fresh one"
		}
		_, _ = io.Copy(io.Discard, res.Body)
		return redeemResponse{}, fmt.Errorf("could not join the war room (HTTP %d)%s", res.StatusCode, hint)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return redeemResponse{}, err
	}
	var decoded redeemResponse
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return redeemResponse{}, err
		}
	}
	return decoded, nil
}
