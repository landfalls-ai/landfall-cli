package auth

import (
	"fmt"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/instance"
)

// ExplainExpiredCredential returns the message to print when a cached
// credential has expired — including the case where it is a legacy Keycloak one
// this CLI can no longer refresh.
//
// Returns "" when there is nothing to explain: no cached credential at all, or
// one that is still valid.
func ExplainExpiredCredential() string {
	cache := ReadCache()
	if cache == nil || cache.AccessToken == "" {
		return ""
	}
	if expires, ok := cache.ExpiresAtMs(); ok && expires > time.Now().UnixMilli() {
		return ""
	}

	// A Keycloak issuer URL always contains a realm path segment. Such a
	// credential predates refresh tokens entirely, so there is no silent
	// recovery — say so, rather than offering a fix that cannot work.
	if strings.Contains(cache.Iss, "/realms/") {
		return "Your cached Landfall credential was issued by the old Keycloak sign-in and has " +
			"expired. It cannot be refreshed. Run `landfall login` to sign in through your " +
			"organization’s own identity provider."
	}
	return "Your Landfall session has expired. Run `landfall login` to sign in again."
}

// ExplainInstanceMismatch answers: is the cached session for the instance we
// are now pointed at?
//
// Returns an explanatory message when it is NOT, and "" when it is fine.
//
// A credential written before instance tracking carries no instance at all.
// That reads as UNKNOWN, and unknown is treated as a MISMATCH rather than
// assumed to match: assuming would silently present an existing token to a host
// that never issued it, which is the precise thing recording the instance
// exists to prevent. This is the rule most likely to be "simplified" by a later
// reader into `if recorded != "" && recorded != resolved` — that inversion is
// the bug, not a cleanup.
func ExplainInstanceMismatch(resolved instance.Instance) string {
	cache := ReadCache()
	if cache == nil || cache.AccessToken == "" {
		return "" // nothing cached; not a mismatch
	}

	cachedAPI := ""
	if cache.Instance != nil {
		cachedAPI = cache.Instance.API
	}
	if cachedAPI != "" && cachedAPI == resolved.API {
		return ""
	}

	if cachedAPI != "" {
		return fmt.Sprintf(
			"Your saved session is for %s, but you are now pointed at %s.\n"+
				"  A session from one Landfall cannot be used against another.\n"+
				"  Run `landfall login` to sign in to this one.", cachedAPI, resolved.API)
	}
	return fmt.Sprintf(
		"Your saved session predates instance tracking, so it cannot be confirmed to belong to "+
			"%s.\n  Run `landfall login` to sign in again.", resolved.API)
}

// GetCachedOrgSlug returns the organization slug the cached session belongs to,
// or "".
//
// A Landfall session is pinned to exactly one organization, so this is the
// session's own answer to "which org am I in?" rather than a preference — which
// is why the declared-intent hook uses it instead of asking the engineer to
// configure a slug a second time.
//
// Returned even when the token has EXPIRED: callers that need a live token ask
// for one separately, and a slug is not a credential.
func GetCachedOrgSlug() string {
	cache := ReadCache()
	if cache == nil || cache.OrgSlug == nil {
		return ""
	}
	return *cache.OrgSlug
}
