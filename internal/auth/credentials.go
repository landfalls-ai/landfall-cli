// Package auth is the CLI's browser-handoff sign-in and the cached session it
// produces.
//
// It speaks OIDC to nobody. An organization's people authenticate against THEIR
// OWN identity provider, and a CLI hard-coded to one broker would work for
// exactly one population and would be a second, divergent place where "which
// authentication branch does this organization use?" gets decided. So the CLI
// does the least it possibly can: open the Landfall web app and wait for a
// session to be handed back, never learning or caring which branch was used.
//
// Where the browser is sent comes from internal/instance and NOWHERE else.
// This package owning its own default is the exact line that once made a
// `brew install` followed by `landfall login` fail for every customer.
//
// Tokens are NEVER logged.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/instance"
)

// CredentialsFileName is the cached session's basename. Deliberately separate
// from config.json: signing out clears this, and must not clear a self-hoster's
// nomination.
const CredentialsFileName = "credentials.json"

// RefreshSkew is how much of the access token's remaining lifetime counts as
// "close enough to expired" to refresh proactively, rather than waiting for a
// caller to see a 401 mid-request. One minute — generous next to the 1h
// access-token TTL, cheap against the 30-day refresh-token one.
const RefreshSkew = 60_000 * time.Millisecond

// defaultAccessTokenLifetimeMs is the fallback expiry written when the server
// did not supply a parseable one: just under an hour, matching the access
// token's real TTL.
const defaultAccessTokenLifetimeMs int64 = 3_540_000

// requestTimeout bounds each individual auth HTTP call. The Node source relied
// on fetch's ambient behaviour; an explicit bound is better, and short enough
// that a hung endpoint cannot stall a command indefinitely.
const requestTimeout = 5 * time.Second

// Doer is the injectable HTTP surface — every call in this package goes
// through one so the whole flow is testable without a real network, matching
// the Node source's `fetchImpl` injection.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

func client(d Doer) Doer {
	if d == nil {
		return &http.Client{Timeout: requestTimeout}
	}
	return d
}

// Credentials is the on-disk shape of credentials.json.
//
// The snake_case JSON keys are the file format and are NOT negotiable — a file
// written by the Node CLI must be readable by this one and vice versa. Note the
// asymmetry that has already broken a real credentials file once: the FILE uses
// snake_case (access_token, refresh_token, expires_at, org_slug) while the
// SERVER's responses use camelCase (accessToken, refreshToken, expiresAt).
type Credentials struct {
	AccessToken string `json:"access_token"`

	// Instance records WHICH Landfall minted this. Without it, a stored token
	// can be silently presented to a deployment that never issued it once the
	// resolved instance changes — the failure then looks like "your session
	// broke" rather than "you are pointed somewhere else". A credential written
	// before this field existed reads as nil, which is treated as a MISMATCH and
	// prompts a fresh sign-in rather than being assumed to match.
	Instance *instance.Instance `json:"instance"`

	// RefreshToken renews the short-lived access token silently. Absent only for
	// a credential written by an older CLI/server pair (graceful degradation:
	// treated as legacy, never assumed refreshable).
	RefreshToken *string `json:"refresh_token"`

	// ExpiresAt is a MILLISECOND epoch, not seconds. This is the file's format
	// (JavaScript's Date.now()), and reading it as seconds yields a credential
	// that looks ~55,000 years expired.
	//
	// A pointer, because the field is genuinely tri-state: a real timestamp, or
	// absent/null on a credential whose expiry could not be determined — which
	// must fall through to the refresh path rather than being read as the epoch
	// (i.e. as expired-with-certainty, which is a different claim).
	ExpiresAt *int64 `json:"expires_at"`

	// OrgSlug is the organization the session is pinned to. Valid even after the
	// token expires: it is not a credential.
	OrgSlug *string `json:"org_slug"`

	// Iss names the issuer. An `iss` containing "/realms/" marks a legacy
	// Keycloak credential, which cannot be refreshed.
	Iss string `json:"iss"`
}

// ExpiresAtMs returns the recorded expiry and whether one was recorded at all.
func (c *Credentials) ExpiresAtMs() (int64, bool) {
	if c == nil || c.ExpiresAt == nil {
		return 0, false
	}
	return *c.ExpiresAt, true
}

// Session is a freshly minted session on its way to disk. ExpiresAt is the
// server's own string (RFC 3339); an empty or unparseable value falls back to
// "just under an hour from now", exactly as the Node source does.
type Session struct {
	AccessToken  string
	RefreshToken *string
	ExpiresAt    string
	OrgSlug      *string
	Instance     *instance.Instance
}

// cacheDir mirrors internal/config's directory resolution without importing it:
// credentials are not configuration, and must never be routed through a config
// library that merges environment variables and flags.
func cacheDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "landfall")
}

// CachePath is the full path to credentials.json.
func CachePath() string { return filepath.Join(cacheDir(), CredentialsFileName) }

// ReadCache returns the cached credentials, or nil when there are none.
//
// Every failure — absent, unreadable, malformed — collapses to nil. A CLI that
// refuses to run because its cache file is corrupt is worse than one that asks
// you to sign in again.
func ReadCache() *Credentials {
	raw, err := os.ReadFile(CachePath())
	if err != nil {
		return nil
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil
	}
	return &c
}

// SaveSession writes credentials.json with owner-only permissions.
//
// The mode is applied TWICE on purpose: os.OpenFile's perm argument is masked
// by the process umask (a umask of 0 is unusual but not impossible, and some
// filesystems ignore the create mode entirely), so an explicit os.Chmod
// afterwards is what actually guarantees 0600. The Node source does the same
// belt-and-braces double-chmod, and this is a bearer token on disk.
func SaveSession(s Session) error {
	if err := os.MkdirAll(cacheDir(), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", cacheDir(), err)
	}

	expires := parseExpiresAt(s.ExpiresAt)
	body, err := json.MarshalIndent(Credentials{
		AccessToken:  s.AccessToken,
		Instance:     s.Instance,
		RefreshToken: s.RefreshToken,
		ExpiresAt:    &expires,
		OrgSlug:      s.OrgSlug,
		Iss:          "landfall-core",
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding credentials: %w", err)
	}

	path := CachePath()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Best-effort, exactly as the Node source's `.catch(() => {})`: a filesystem
	// that cannot express the mode must not make sign-in fail outright.
	_ = os.Chmod(path, 0o600)
	return nil
}

// parseExpiresAt converts the server's RFC 3339 string into a millisecond
// epoch, falling back to "just under an hour from now" when there is nothing
// parseable — the same fallback the Node source applies when `expiresAt` is not
// a string.
func parseExpiresAt(value string) int64 {
	if value != "" {
		if t, err := time.Parse(time.RFC3339, value); err == nil {
			return t.UnixMilli()
		}
	}
	return time.Now().UnixMilli() + defaultAccessTokenLifetimeMs
}

// GetCachedAccessToken returns a usable access token, refreshing it silently
// first if it is expired or within RefreshSkew of expiring — or "" if the user
// must run `landfall login` (no cached session, or the refresh token itself is
// dead: expired, reused, or revoked).
//
// It NEVER triggers a browser flow on its own; the background refresh is a
// single unauthenticated HTTP call.
//
// A credential cached by the previous Keycloak flow is still honoured until it
// expires. Once expired it cannot be refreshed (it predates refresh tokens
// entirely, so RefreshAccessToken declines rather than guessing) and
// ExplainExpiredCredential produces an explicit instruction. Never a silent
// failure: a CLI that quietly stops working during an incident is worse than
// one that says what to do.
func GetCachedAccessToken(ctx context.Context, d Doer) string {
	cache := ReadCache()
	if cache == nil || cache.AccessToken == "" {
		return ""
	}
	if expires, ok := cache.ExpiresAtMs(); ok {
		if expires-time.Now().UnixMilli() > RefreshSkew.Milliseconds() {
			return cache.AccessToken
		}
	}
	return RefreshAccessToken(ctx, d)
}

// refreshResponse is the server's reply to /auth/cli-refresh and
// /auth/cli-handoff/poll. camelCase — the wire format, distinct from the
// snake_case file format above.
type refreshResponse struct {
	AccessToken  string  `json:"accessToken"`
	RefreshToken string  `json:"refreshToken"`
	ExpiresAt    string  `json:"expiresAt"`
	OrgSlug      *string `json:"orgSlug"`
}

// RefreshAccessToken silently exchanges the cached refresh token for a fresh,
// rotated pair and persists the result. Returns the new access token, or "" —
// with NOTHING written — if there is nothing to refresh with, the call fails,
// or the server refuses it.
//
// Every refusal reason collapses to the same shape here on purpose: an unknown,
// expired, reused, or org-revoked refresh token all look identical from this
// side. The specific cause lives only in the server's own audit record.
//
// It targets cache.Instance.API — the SAME Landfall that minted the chain —
// never whichever instance happens to be currently resolved. Presenting a
// refresh token to a deployment that never issued it is precisely what
// recording the minting instance exists to prevent. A credential predating
// instance tracking has nothing to target and is declined exactly like one with
// no refresh token at all.
func RefreshAccessToken(ctx context.Context, d Doer) string {
	cache := ReadCache()
	if cache == nil || cache.RefreshToken == nil || *cache.RefreshToken == "" {
		return ""
	}
	if cache.Instance == nil || cache.Instance.API == "" {
		return ""
	}

	body, ok := postJSON(ctx, d, cache.Instance.API+"/auth/cli-refresh",
		map[string]string{"refreshToken": *cache.RefreshToken})
	if !ok {
		// Offline, DNS failure, 401, unparseable body — all indistinguishable
		// from "could not refresh" here. The caller falls back to the
		// expired-credential path.
		return ""
	}
	if body.AccessToken == "" || body.RefreshToken == "" {
		return ""
	}

	rotated := body.RefreshToken
	if err := SaveSession(Session{
		AccessToken:  body.AccessToken,
		RefreshToken: &rotated,
		ExpiresAt:    body.ExpiresAt,
		OrgSlug:      cache.OrgSlug,
		Instance:     cache.Instance,
	}); err != nil {
		return ""
	}
	return body.AccessToken
}

// IsAuthenticated reports whether an unexpired (or silently refreshable)
// session is cached.
func IsAuthenticated(ctx context.Context, d Doer) bool {
	return GetCachedAccessToken(ctx, d) != ""
}

// Logout signs out: it revokes the refresh chain server-side FIRST, then clears
// the local file.
//
// Without the server call, a stray copy of the credential file left on a backup
// or another machine could keep refreshing silently after an explicit sign-out,
// which would make "sign out" a lie.
//
// The revoke is best-effort and silent on failure (RFC 7009's own convention):
// local sign-out must still succeed offline, so a failed or unreachable revoke
// never blocks clearing the file. A credential with nothing to revoke — no
// refresh token or no recorded instance — skips straight to the local delete.
func Logout(ctx context.Context, d Doer) error {
	cache := ReadCache()
	if cache != nil && cache.RefreshToken != nil && *cache.RefreshToken != "" &&
		cache.Instance != nil && cache.Instance.API != "" {
		postJSON(ctx, d, cache.Instance.API+"/auth/cli-logout",
			map[string]string{"refreshToken": *cache.RefreshToken})
	}
	if err := os.Remove(CachePath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", CachePath(), err)
	}
	return nil
}

// postJSON performs one JSON POST and decodes the reply, collapsing EVERY
// failure mode — transport error, non-2xx status, unparseable body — into a
// single `false`. Callers in this package treat all three identically by
// design; distinguishing them would leak the server's refusal reasoning to a
// client that must not depend on it.
func postJSON(ctx context.Context, d Doer, url string, payload any) (refreshResponse, bool) {
	var out refreshResponse

	encoded, err := json.Marshal(payload)
	if err != nil {
		return out, false
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return out, false
	}
	req.Header.Set("content-type", "application/json")

	res, err := client(d).Do(req)
	if err != nil {
		return out, false
	}
	defer func() {
		if res.Body != nil {
			_ = res.Body.Close()
		}
	}()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return out, false
	}
	if res.Body == nil {
		return out, false
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return out, false
	}
	return out, true
}
