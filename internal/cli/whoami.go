package cli

// whoami.go: `landfall whoami [--json]` (console spec §2.6, §9): is there a
// sign-in, for which organization, on which Landfall. The Claude Code mod
// reads it before it draws, to choose between the incident picker and the
// sign-in state.
//
// OFFLINE by design: it reads the credentials file and resolves the instance,
// and makes no network call. It does not refresh an expired token either (a
// refresh rotates the refresh token, which is a write, and the mod asks this
// on every open). A credential the server has since revoked therefore still
// reads as signed in here; the next command that uses it answers `Sign in to
// …`, which the mod already turns into the sign-in state.
//
// "Signed in" follows the same rule auth.GetCachedAccessToken applies before
// every person-read, so the two never disagree on a credential at rest:
//
//   - an access token is cached, for the Landfall this CLI points at now
//     (auth.ExplainInstanceMismatch's rule: a credential that records no
//     instance is a mismatch, never assumed to match), and
//   - it has more than auth.RefreshSkew left, or it can be refreshed (a
//     refresh token and the instance that minted it are both recorded).

import (
	"os"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/instance"
	"github.com/spf13/cobra"
)

// Why a credential at rest is not a sign-in, as whoami's `reason` says it.
const (
	whoamiNone          = "none"
	whoamiExpired       = "expired"
	whoamiOtherInstance = "other-instance"
)

// WhoamiDeps are whoami's effects: the environment, the credentials file and
// the clock. Nothing here can reach the network.
type WhoamiDeps struct {
	Env       func(string) string
	ReadCache func() *auth.Credentials
	Now       func() time.Time
}

func newWhoamiCommand(ui *UI) *cobra.Command {
	var (
		jsonOut bool
		host    string
	)
	c := newCommand(ui, "whoami", func(*cobra.Command, []string) error {
		ans := RunWhoami(WhoamiDeps{})
		if jsonOut {
			return printAnswer(ui.Out, ans)
		}
		ui.Outf("%s\n", whoamiText(ans))
		return nil
	})
	c.Flags().BoolVar(&jsonOut, "json", false, "answer one JSON line")
	c.Flags().StringVar(&host, "host", "", "the agent host this runs for (accepted, not used)")
	c.FParseErrWhitelist.UnknownFlags = true
	c.Hidden = true
	return c
}

// RunWhoami builds the answer:
//
//	{"ok":true,"signedIn":true,"org":"acme","instance":{"name","web","api"},
//	 "expiresAt":"<RFC 3339>","refreshable":true}
//
// with signedIn false (and a `reason`) when there is no credential, it is for
// another Landfall, or it is expired and cannot be refreshed. `org`,
// `expiresAt` and `refreshable` are present whenever a credential records
// them, signed in or not, so the mod can say "Your sign-in expired."
func RunWhoami(d WhoamiDeps) map[string]any {
	getenv := d.Env
	if getenv == nil {
		getenv = os.Getenv
	}
	readCache := d.ReadCache
	if readCache == nil {
		readCache = auth.ReadCache
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}

	inst, err := instance.Resolve(instance.Options{Env: instance.Env(getenv)})
	if err != nil {
		return failure("The Landfall address this CLI is set to is not valid. Run landfall instance reset, or set a valid one.")
	}
	ans := map[string]any{
		"ok":       true,
		"signedIn": false,
		"instance": map[string]any{"name": inst.Name, "web": trimSlash(inst.Web), "api": trimSlash(inst.API)},
	}

	cache := readCache()
	org := getenv("LANDFALL_SLUG")
	if org == "" && cache != nil && cache.OrgSlug != nil {
		org = *cache.OrgSlug
	}
	if org != "" {
		ans["org"] = org
	}
	if cache == nil || cache.AccessToken == "" {
		ans["reason"] = whoamiNone
		return ans
	}

	expires, known := cache.ExpiresAtMs()
	if known {
		ans["expiresAt"] = time.UnixMilli(expires).UTC().Format(time.RFC3339)
	}
	refreshable := cache.RefreshToken != nil && *cache.RefreshToken != "" &&
		cache.Instance != nil && cache.Instance.API != ""
	ans["refreshable"] = refreshable

	if cache.Instance == nil || trimSlash(cache.Instance.API) != trimSlash(inst.API) {
		ans["reason"] = whoamiOtherInstance
		return ans
	}
	fresh := known && expires-now.UnixMilli() > auth.RefreshSkew.Milliseconds()
	if !fresh && !refreshable {
		ans["reason"] = whoamiExpired
		return ans
	}
	ans["signedIn"] = true
	return ans
}

// whoamiText is the answer for a person at a terminal.
func whoamiText(ans map[string]any) string {
	if ans["ok"] != true {
		s, _ := ans["error"].(string)
		return s
	}
	web := ""
	if i, ok := ans["instance"].(map[string]any); ok {
		web, _ = i["web"].(string)
	}
	org, _ := ans["org"].(string)
	if ans["signedIn"] == true {
		if org != "" {
			return "Signed in to " + org + " at " + web + "."
		}
		return "Signed in at " + web + "."
	}
	switch ans["reason"] {
	case whoamiExpired:
		return "Your sign-in to " + web + " expired. Run landfall login."
	case whoamiOtherInstance:
		return "Your sign-in is for another Landfall, not " + web + ". Run landfall login."
	}
	return "Not signed in to " + web + ". Run landfall login."
}

func trimSlash(s string) string { return strings.TrimRight(s, "/") }
