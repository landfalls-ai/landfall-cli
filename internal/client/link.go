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
}

// RedeemShareLink redeems a share link for an incident-scoped edge session,
// returning the full bridge config.
func RedeemShareLink(ctx context.Context, shareURL string, opts RedeemOptions) (Config, error) {
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

	body, err := json.Marshal(map[string]any{"ticket": parsed.Ticket})
	if err != nil {
		return Config{}, err
	}
	endpoint := base + "/o/" + parsed.Slug + "/incidents/" + parsed.IncidentID + "/edge/redeem"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Config{}, err
	}
	req.Header.Set("content-type", "application/json")

	res, err := doer.Do(req)
	if err != nil {
		return Config{}, err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 200 || res.StatusCode > 299 {
		hint := ""
		if res.StatusCode == http.StatusGone {
			hint = " — the link expired or was already used; ask for a fresh one"
		}
		_, _ = io.Copy(io.Discard, res.Body)
		return Config{}, fmt.Errorf("could not join the war room (HTTP %d)%s", res.StatusCode, hint)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return Config{}, err
	}
	var decoded redeemResponse
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return Config{}, err
		}
	}
	return Config{
		BaseURL:      base,
		Slug:         parsed.Slug,
		IncidentID:   parsed.IncidentID,
		Token:        decoded.Token,
		HumanActorID: decoded.HumanActorID,
	}, nil
}
