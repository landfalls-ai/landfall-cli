package instance

// instance_test.go ports the assertions of the Node ancestor's
// test/instance.test.mjs — resolution precedence, address validation, and the
// reachability-failure message contract. These tests exist because the bug
// they prevent was not a typo: four separate features each invented their own
// answer to "where is Landfall", in good faith, because there was no shared
// one (see instance.go's package doc). The guard test in
// internal/guard/no_hardcoded_hostname_test.go is what stops a fifth private
// default appearing; this file is what keeps the ONE answer correct.
//
// explainInstanceMismatch's own behavioural tests (ported from
// test/instance-mismatch.test.mjs) live in internal/auth/explain_test.go
// instead of here: the function itself is implemented in
// internal/auth/explain.go (it needs the cached credential, which is that
// package's concern), not in this package. Testing it from here would mean
// importing internal/auth from an instance_test.go in package `instance`,
// which is backwards next to this package's own "config <- instance, never
// instance <- auth" dependency direction (see instance.go / config.go package
// docs). internal/auth/explain_test.go still satisfies T015's
// `internal/auth/*_test.go` file pattern.
import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/config"
)

// withTempConfig points XDG_CONFIG_HOME at a fresh, auto-cleaned directory so
// no test ever touches the developer's own config.json — the Go analogue of
// the Node suite's withTempConfig helper.
func withTempConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// ── Resolve precedence ──────────────────────────────────────────────────────

func TestResolve_DefaultsToHostedAndNamesNoEnvironment(t *testing.T) {
	withTempConfig(t)

	got, err := Resolve(Options{Env: MapEnv(nil)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	hosted := Hosted()
	if got.Web != hosted.Web || got.API != hosted.API || got.Docs != hosted.Docs {
		t.Fatalf("got %+v, want the hosted default %+v", got, hosted)
	}
	if got.Source != "default" {
		t.Fatalf("Source = %q, want %q", got.Source, "default")
	}
	// The whole point of shipping an environment-neutral default: the value
	// must survive a future production cutover without a release.
	for _, address := range []string{got.Web, got.API, got.Docs} {
		if strings.Contains(address, "dev") {
			t.Errorf("%s must not name an environment", address)
		}
		if strings.Contains(address, "localhost") || strings.Contains(address, "127.0.0.1") {
			t.Errorf("%s must not be a local address", address)
		}
	}
}

func TestResolve_PerEndpointEnvWinsOverEverything(t *testing.T) {
	withTempConfig(t)

	if _, err := SaveNomination(Instance{Name: "custom", Web: "https://nominated.example", API: "https://nominated.example", Docs: Hosted().Docs}); err != nil {
		t.Fatalf("SaveNomination: %v", err)
	}

	got, err := Resolve(Options{
		URL: "https://flag.example",
		Env: MapEnv(map[string]string{
			"LANDFALL_WEB_URL":  "http://localhost:5173",
			"LANDFALL_BASE_URL": "http://localhost:3001",
		}),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Web != "http://localhost:5173" {
		t.Errorf("Web = %q, want the env override", got.Web)
	}
	if got.API != "http://localhost:3001" {
		t.Errorf("API = %q, want the env override", got.API)
	}
	if !strings.HasPrefix(got.Source, "env:") {
		t.Errorf("Source = %q, want it to start with env:", got.Source)
	}
}

func TestResolve_URLFlagBeatsSavedNomination(t *testing.T) {
	withTempConfig(t)

	if _, err := SaveNomination(Instance{Name: "custom", Web: "https://saved.example", API: "https://saved.example", Docs: Hosted().Docs}); err != nil {
		t.Fatalf("SaveNomination: %v", err)
	}

	got, err := Resolve(Options{URL: "https://flag.example", Env: MapEnv(nil)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Web != "https://flag.example" {
		t.Errorf("Web = %q, want the --url flag to win", got.Web)
	}
	if got.Source != "flag" {
		t.Errorf("Source = %q, want %q", got.Source, "flag")
	}
}

func TestResolve_SavedNominationBeatsDefaultAndSurvivesANewInvocation(t *testing.T) {
	withTempConfig(t)

	if _, err := SaveNomination(Instance{Name: "custom", Web: "https://saved.example", API: "https://saved.example", Docs: Hosted().Docs}); err != nil {
		t.Fatalf("SaveNomination: %v", err)
	}

	first, err := Resolve(Options{Env: MapEnv(nil)})
	if err != nil {
		t.Fatalf("Resolve (first): %v", err)
	}
	if first.Web != "https://saved.example" || first.Source != "config" {
		t.Fatalf("first = %+v, want web=https://saved.example source=config", first)
	}

	// A separate Resolve call is a separate "invocation": nothing is memoised
	// in-process, so persistence on disk is what carries the nomination.
	second, err := Resolve(Options{Env: MapEnv(nil)})
	if err != nil {
		t.Fatalf("Resolve (second): %v", err)
	}
	if second.Web != "https://saved.example" {
		t.Errorf("second.Web = %q, want it to persist across invocations", second.Web)
	}
}

func TestClearNomination_ReturnsToTheDefault(t *testing.T) {
	withTempConfig(t)

	if _, err := SaveNomination(Instance{Name: "custom", Web: "https://saved.example", API: "https://saved.example", Docs: Hosted().Docs}); err != nil {
		t.Fatalf("SaveNomination: %v", err)
	}
	ClearNomination()

	got, err := Resolve(Options{Env: MapEnv(nil)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Web != Hosted().Web {
		t.Errorf("Web = %q, want the hosted default", got.Web)
	}
	if got.Source != "default" {
		t.Errorf("Source = %q, want %q", got.Source, "default")
	}
}

func TestResolve_NominationMissingAFieldIsCompletedFromTheLevelBelow(t *testing.T) {
	withTempConfig(t)

	if _, err := SaveNomination(Instance{Name: "custom", Web: "https://partial.example"}); err != nil {
		t.Fatalf("SaveNomination: %v", err)
	}

	got, err := Resolve(Options{Env: MapEnv(nil)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Web != "https://partial.example" {
		t.Errorf("Web = %q", got.Web)
	}
	if got.API != "https://partial.example" {
		t.Errorf("API = %q, want it to fall back to web", got.API)
	}
	if got.Docs != Hosted().Docs {
		t.Errorf("Docs = %q, want the hosted default (docs are not per-deployment)", got.Docs)
	}
	if got.Name == "" || got.Web == "" || got.API == "" || got.Docs == "" || got.Source == "" {
		t.Errorf("Resolve must never leave a field empty: %+v", got)
	}
}

func TestLoadNomination_AMalformedConfigFileIsIgnored(t *testing.T) {
	withTempConfig(t)

	if err := writeRawConfigFile(t, "{ not json"); err != nil {
		t.Fatalf("writing malformed config: %v", err)
	}

	got, err := Resolve(Options{Env: MapEnv(nil)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Web != Hosted().Web {
		t.Errorf("Web = %q, want a malformed file to be treated as absent", got.Web)
	}
}

func writeRawConfigFile(t *testing.T, content string) error {
	t.Helper()
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(config.Path(), []byte(content), 0o644)
}

// ── ParseAddress ─────────────────────────────────────────────────────────────

func TestParseAddress(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    string
		wantErr string // substring expected in the error, case-insensitive; "" means no error
	}{
		{name: "trailing slash stripped", value: "https://example.com/", want: "https://example.com"},
		{name: "multiple trailing slashes stripped", value: "https://example.com///", want: "https://example.com"},
		{name: "empty is refused", value: "", wantErr: "empty"},
		{name: "not a URL is refused", value: "not-a-url", wantErr: "not a valid URL"},
		{name: "non-http(s) scheme is refused", value: "ftp://example.com", wantErr: "http or https"},
		// Plain http to a remote host would send a bearer token in clear text.
		{name: "plain http on a remote host is refused", value: "http://example.com", wantErr: "clear text"},
		// ...but local development is exactly this, and must stay silent.
		{name: "plain http on localhost is accepted", value: "http://localhost:5173", want: "http://localhost:5173"},
		{name: "plain http on a loopback IP is accepted", value: "http://127.0.0.1:3001", want: "http://127.0.0.1:3001"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAddress(tc.value, "address")
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseAddress(%q) = %q, nil; want an error containing %q", tc.value, got, tc.wantErr)
				}
				if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantErr)) {
					t.Fatalf("ParseAddress(%q) error = %q, want it to contain %q", tc.value, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAddress(%q) unexpected error: %v", tc.value, err)
			}
			if got != tc.want {
				t.Fatalf("ParseAddress(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// ── DescribeFailure ──────────────────────────────────────────────────────────

func TestDescribeFailure_EveryFailureMessageNamesWhatWhyAndHowToFix(t *testing.T) {
	inst := Hosted()
	inst.Source = "default"

	// The Node source's suite additionally loops REACHABILITY.NOT_LANDFALL.
	// This Go port deliberately has no such member: probe.go's own comment
	// records that nothing ever produced it in the Node implementation
	// either, so porting a status that cannot occur would be porting dead
	// code (tasks.md T038 note / OD-3).
	for _, status := range []Reachability{Unresolved, Refused} {
		t.Run(string(status), func(t *testing.T) {
			message := DescribeFailure(status, inst)
			if message == "" {
				t.Fatalf("%s must produce a message", status)
			}
			if !strings.Contains(message, inst.Web) {
				t.Errorf("message must name the address that was tried: %q", message)
			}
			if !strings.Contains(message, "landfall login --url") && !strings.Contains(message, "landfall instance reset") {
				t.Errorf("message must offer a runnable fix: %q", message)
			}
		})
	}
}

func TestDescribeFailure_ADeadLocalAddressIsNamedAsSuch(t *testing.T) {
	// The exact case the operator hit: LANDFALL_WEB_URL pointing at a dev
	// server that is not running. A bare "connection refused" is what made
	// it a dead end.
	local := Instance{Name: "local", Web: "http://localhost:5173", API: "http://localhost:3001", Docs: Hosted().Docs, Source: "env"}
	message := DescribeFailure(LocalDead, local)
	if !strings.Contains(strings.ToLower(message), "local development address") {
		t.Errorf("message = %q, want it to name a local development address", message)
	}
	if !strings.Contains(strings.ToLower(message), "nothing is listening") {
		t.Errorf("message = %q, want it to say nothing is listening", message)
	}
	if !strings.Contains(message, "unset LANDFALL_WEB_URL") {
		t.Errorf("message = %q, want a runnable unset command", message)
	}
}

func TestDescribeFailure_AnAmbiguousTimeoutProducesNoFatalMessage(t *testing.T) {
	hosted := Hosted()
	if got := DescribeFailure(Timeout, hosted); got != "" {
		t.Errorf("DescribeFailure(Timeout, ...) = %q, want empty (a slow link must not block sign-in)", got)
	}
	if got := DescribeFailure(OK, hosted); got != "" {
		t.Errorf("DescribeFailure(OK, ...) = %q, want empty", got)
	}
}

// ── Probe / reachability classification ─────────────────────────────────────
//
// The Node suite feeds REACHABILITY statuses straight into describeFailure
// without exercising the classifier itself over a network. This port adds
// that missing layer using the package's own injectable Doer, exactly as
// prescribed ("use ... the existing injectable client interface if the
// implementation already exposes one") rather than touching a real network.

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestProbe_OKOnAnyHTTPAnswerFromTheOriginRoot(t *testing.T) {
	var gotMethod, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// A path on the address must not change what gets probed: Probe always
	// hits the origin root.
	got := Probe(context.Background(), server.URL+"/some/nested/path", ProbeOptions{})
	if got != OK {
		t.Fatalf("Probe = %s, want OK", got)
	}
	if gotMethod != http.MethodHead {
		t.Errorf("method = %q, want HEAD (no body worth reading)", gotMethod)
	}
	if gotPath != "/" {
		t.Errorf("path probed = %q, want the origin root regardless of the address's own path", gotPath)
	}
}

func TestProbe_UnresolvedOnDNSFailure(t *testing.T) {
	d := doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "nowhere.example", IsNotFound: true}
	})
	got := Probe(context.Background(), "https://nowhere.example", ProbeOptions{Client: d})
	if got != Unresolved {
		t.Fatalf("Probe = %s, want Unresolved", got)
	}
}

func TestProbe_UnresolvedOnAnAddressWithNoHostWithoutCallingTheClient(t *testing.T) {
	called := false
	d := doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("must not be called")
	})
	got := Probe(context.Background(), "not-a-url", ProbeOptions{Client: d})
	if got != Unresolved {
		t.Fatalf("Probe = %s, want Unresolved", got)
	}
	if called {
		t.Error("an address with no host must fail before ever reaching the client")
	}
}

func TestProbe_TimeoutOnADeadlineExceeded(t *testing.T) {
	d := doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})
	got := Probe(context.Background(), "https://slow.example", ProbeOptions{Client: d})
	if got != Timeout {
		t.Fatalf("Probe = %s, want Timeout", got)
	}
}

func TestProbe_RefusedOnARemoteConnectionRefusal(t *testing.T) {
	d := doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	})
	got := Probe(context.Background(), "https://remote.example", ProbeOptions{Client: d})
	if got != Refused {
		t.Fatalf("Probe = %s, want Refused", got)
	}
}

func TestProbe_LocalDeadOnALoopbackConnectionRefusal(t *testing.T) {
	d := doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	})
	got := Probe(context.Background(), "http://localhost:5173", ProbeOptions{Client: d})
	if got != LocalDead {
		t.Fatalf("Probe = %s, want LocalDead (a local dev server that is not running)", got)
	}
}

func TestProbe_ZeroOptionsUsesDefaultTimeoutWithoutPanicking(t *testing.T) {
	// A zero ProbeOptions must not panic and must still classify correctly;
	// the fake client answers immediately so DefaultProbeTimeout is exercised
	// without the test actually waiting it out.
	d := doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	if got := Probe(context.Background(), "https://ok.example", ProbeOptions{Client: d}); got != OK {
		t.Fatalf("Probe = %s, want OK", got)
	}
}
