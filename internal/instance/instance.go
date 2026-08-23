// Package instance is THE one place that answers "where is Landfall".
//
// ── WHY THIS PACKAGE EXISTS ───────────────────────────────────────────────
//
// Before its Node ancestor (src/instance.mjs, feature 092) there were FOUR
// separate answers, and a customer could not reach Landfall at all: `landfall
// login` opened a monorepo developer's Vite dev server, the API defaulted to a
// local port in two files, `connect aws` defaulted to a hostname that did not
// exist in DNS, and failure messages linked a docs host that did not exist
// either. Each was added in good faith, because there was no shared one to
// reach for.
//
// That is a structural problem, so it has a structural fix: this package is the
// ONLY one allowed to name an address, and internal/guard's
// TestNoHardcodedAddress fails the build if that stops being true. Do not copy
// a hostname or a loopback URL out of this file into any other package.
//
// ── THE PRIORITY THIS PACKAGE INVERTS ─────────────────────────────────────
//
// The old defaults optimised for the handful of people running the whole
// platform on a laptop, at the cost of every customer. For a program people
// install with `brew`, that is backwards. So the DEFAULT is the hosted service
// and local development is an explicit opt-in — which costs a monorepo
// developer nothing, because the environment variables they already export
// still win over everything else.
package instance

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/config"
)

// Instance is a fully resolved answer to "where is Landfall" — every address
// or none. Resolve never returns a partial one, so no command can end up
// talking to the hosted API with a locally-nominated web address.
//
// The JSON tags matter: this exact shape is what internal/auth records inside
// credentials.json as "which Landfall minted this token".
type Instance struct {
	Name string `json:"name"`
	Web  string `json:"web"`
	API  string `json:"api"`
	Docs string `json:"docs"`
	// Source names the precedence level that won, for diagnostics ("default",
	// "flag", "config", "env:LANDFALL_URL", "env:LANDFALL_WEB_URL+LANDFALL_BASE_URL").
	Source string `json:"source,omitempty"`
}

// Hosted is the built-in default: the hosted Landfall.
//
// These hostnames deliberately name NO environment. The value ships inside
// every installed copy and can only be changed by cutting a release AND having
// every user upgrade, so anything environment-shaped ("dev") would become
// permanent the moment it was published. Environment-neutral names make a
// future repoint a DNS change instead of a migration.
//
// Returned by value, so a caller cannot mutate the package's own default.
func Hosted() Instance {
	return Instance{
		Name: "hosted",
		Web:  "https://app.landfalls.ai",
		API:  "https://api.landfalls.ai",
		Docs: "https://docs.landfalls.ai",
	}
}

// DefaultInstance is the Node source's DEFAULT_INSTANCE export, kept as a
// function for the same reason as Hosted: an exported struct variable would be
// writable by every importer.
func DefaultInstance() Instance { return Hosted() }

// docsFallback: documentation is not per-deployment. A self-hoster reads the
// same guides, so a custom instance keeps the hosted docs rather than being
// required to run a docs site just to make error messages resolve.
func docsFallback() string { return Hosted().Docs }

// Env is how Resolve reads environment variables. A nil Env means the real
// process environment; tests pass MapEnv so they never depend on (or disturb)
// the developer's own exports.
type Env func(key string) string

// MapEnv adapts a plain map for use as an Env. A nil or empty map is a valid,
// completely empty environment — which is what most precedence tests want.
func MapEnv(m map[string]string) Env {
	return func(key string) string { return m[key] }
}

// Options configures Resolve. The zero value resolves from the real
// environment with no explicit --url.
type Options struct {
	// URL is an explicit `--url <address>` nomination. Empty means none.
	URL string
	// Env supplies environment variables; nil means the real process environment.
	Env Env
}

func (o Options) env(key string) string {
	if o.Env == nil {
		return os.Getenv(key)
	}
	return o.Env(key)
}

// isLoopback covers the shapes local development actually uses.
//
// Both "::1" and "[::1]" are accepted: Go's url.URL.Hostname strips the
// brackets an IPv6 authority is written with, but a caller passing a raw
// hostname through may not have.
func isLoopback(hostname string) bool {
	return hostname == "localhost" ||
		hostname == "127.0.0.1" ||
		hostname == "::1" ||
		hostname == "[::1]" ||
		strings.HasSuffix(hostname, ".localhost")
}

// ParseAddress parses and normalises one address, or explains why it cannot.
//
// Trailing slashes are stripped HERE so that every consumer can concatenate a
// path without each one re-deciding, which is the kind of small inconsistency
// that produces `//cli-auth` in a URL a user is asked to trust.
//
// Plain http is refused for a remote host (it would send a bearer token in
// clear text) but accepted in SILENCE for loopback, because that is simply what
// local development is, and warning about it every time would train people to
// ignore warnings.
//
// label names the thing being parsed, so the error says which of several
// addresses was wrong ("saved API address", "LANDFALL_WEB_URL", …).
func ParseAddress(value, label string) (string, error) {
	if label == "" {
		label = "address"
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		// Capitalized "The ..." to match src/instance.mjs:97's exact wording —
		// a divergence caught during Wave 3 integration (Go convention would
		// lowercase this, but FR-001 wants byte-identical stderr).
		return "", fmt.Errorf("The %s is empty. Expected a URL such as %s", label, Hosted().Web) //nolint:staticcheck // ST1005: intentional, see above
	}

	u, err := url.Parse(trimmed)
	// Go's url.Parse is far more permissive than the WHATWG URL constructor the
	// Node source used: "not-a-url" parses successfully as a relative path
	// reference. An absent scheme or host is what "not a valid URL" means here.
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf( //nolint:staticcheck // ST1005: matches src/instance.mjs's capitalized wording, FR-001
			"The %s %q is not a valid URL. Expected something like %s", label, value, Hosted().Web)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("The %s %q must use http or https, not %s:", label, value, u.Scheme) //nolint:staticcheck // ST1005: same
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return "", fmt.Errorf( //nolint:staticcheck // ST1005: same
			"The %s %q uses plain http over the network, which would send your credentials in "+
				"clear text. Use https, or a local address if you are running Landfall on this machine.",
			label, value)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Resolve returns the instance, highest precedence first:
//
//  1. per-endpoint environment variables (LANDFALL_WEB_URL, LANDFALL_BASE_URL)
//  2. a nomination: an explicit --url, LANDFALL_URL, or the persisted config
//  3. the built-in hosted default
//
// Level 1 is deliberately the HIGHEST. It is what monorepo developers already
// export today, so their setup keeps working byte for byte; those variables
// stop being the default, not the mechanism.
//
// This order is precisely why Viper does not own precedence in this CLI: Viper
// puts an explicit flag above everything, which would invert levels 1 and 2 and
// break every local-development setup. internal/config is only asked for the
// bytes on disk; the ordering below is hand-written on purpose.
func Resolve(opts Options) (Instance, error) {
	nominated := opts.URL
	if nominated == "" {
		nominated = opts.env("LANDFALL_URL")
	}

	var persisted *config.Nomination
	if nominated == "" {
		persisted = config.LoadNomination()
	}

	base := Hosted()
	source := "default"

	switch {
	case nominated != "":
		address, err := ParseAddress(nominated, "instance address")
		if err != nil {
			return Instance{}, err
		}
		// A single address nominates BOTH the sign-in surface and the service,
		// which is the common self-hosted shape (one origin). Someone whose
		// deployment splits them uses the per-endpoint variables below, which win.
		base = Instance{Name: "custom", Web: address, API: address, Docs: docsFallback()}
		if opts.URL != "" {
			source = "flag"
		} else {
			source = "env:LANDFALL_URL"
		}

	case persisted != nil:
		name := "custom"
		if persisted.Name == "hosted" {
			name = "hosted"
		}
		web, err := ParseAddress(firstNonEmpty(persisted.Web, Hosted().Web), "saved web address")
		if err != nil {
			return Instance{}, err
		}
		api, err := ParseAddress(
			firstNonEmpty(persisted.API, persisted.Web, Hosted().API), "saved API address")
		if err != nil {
			return Instance{}, err
		}
		docs, err := ParseAddress(firstNonEmpty(persisted.Docs, docsFallback()), "saved docs address")
		if err != nil {
			return Instance{}, err
		}
		base = Instance{Name: name, Web: web, API: api, Docs: docs}
		source = "config"
	}

	// Level 1 is applied LAST, so it overrides whatever the levels below produced.
	var overrides []string
	if raw := opts.env("LANDFALL_WEB_URL"); raw != "" {
		web, err := ParseAddress(raw, "LANDFALL_WEB_URL")
		if err != nil {
			return Instance{}, err
		}
		base.Web = web
		overrides = append(overrides, "LANDFALL_WEB_URL")
	}
	if raw := opts.env("LANDFALL_BASE_URL"); raw != "" {
		api, err := ParseAddress(raw, "LANDFALL_BASE_URL")
		if err != nil {
			return Instance{}, err
		}
		base.API = api
		overrides = append(overrides, "LANDFALL_BASE_URL")
	}
	if len(overrides) > 0 {
		base.Name = "local"
		source = "env:" + strings.Join(overrides, "+")
	}

	base.Source = source
	return base, nil
}

// SaveNomination persists a nomination after validating it, so a bad address
// can never be written to disk and then break every subsequent command.
//
// Deliberately NOT stored in credentials.json: signing out must not discard a
// self-hoster's choice of deployment.
func SaveNomination(n Instance) (Instance, error) {
	if n.Web != "" {
		web, err := ParseAddress(n.Web, "instance address")
		if err != nil {
			return Instance{}, err
		}
		n.Web = web
	}
	if n.API != "" {
		api, err := ParseAddress(n.API, "instance address")
		if err != nil {
			return Instance{}, err
		}
		n.API = api
	}
	if n.Docs != "" {
		docs, err := ParseAddress(n.Docs, "instance address")
		if err != nil {
			return Instance{}, err
		}
		n.Docs = docs
	}
	if err := config.SaveNomination(config.Nomination{
		Name: n.Name, Web: n.Web, API: n.API, Docs: n.Docs,
	}); err != nil {
		return Instance{}, err
	}
	return n, nil
}

// ClearNomination returns to the built-in default, reporting whether anything
// was actually removed.
func ClearNomination() bool { return config.ClearNomination() }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
