package auth

// explain_test.go ports the behavioural assertions of the Node ancestor's
// test/instance-mismatch.test.mjs — ExplainInstanceMismatch's rule that an
// absent recorded instance reads as a MISMATCH, never a match (FR-014).
//
// The Node file's FIRST test is deliberately not ported here: it asserts
// that bin/landfall.mjs's CLI entry point actually calls
// explainInstanceMismatch, not merely imports it — a wiring regression test
// for a real dead-code escape (v0.3.0 shipped the check fully implemented,
// unit-testable, and never called; see that file's own header comment). This
// Go rewrite's command wiring (cmd/, or wherever the Cobra entry point ends
// up) is a different mechanism entirely and out of scope for this package's
// tests — T010/T015 name internal/instance, internal/config and
// internal/auth only. The behavioural rule itself — the part that matters
// for FR-014 — is fully covered below.
import (
	"strings"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/instance"
)

// hosted stands in for "the instance currently resolved" across these tests
// — a fictional domain, per testutil_test.go's note on why.
var hosted = instance.Instance{Name: "hosted", Web: "https://instance.example/web", API: testAPI}

func TestExplainInstanceMismatch_CrossInstanceSessionIsRefusedWithAnExplanation(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken: "x",
		ExpiresAt:   ptrInt64(time.Now().Add(10 * time.Minute).UnixMilli()),
		Instance:    &instance.Instance{API: "https://other-company.example/api"},
	})

	message := ExplainInstanceMismatch(hosted)
	if message == "" {
		t.Fatal("a cross-instance session must be reported, not used")
	}
	if !strings.Contains(message, "other-company.example") {
		t.Errorf("message must name the instance the session belongs to: %q", message)
	}
	if !strings.Contains(message, testAPI) {
		t.Errorf("message must name the instance now targeted: %q", message)
	}
	if !strings.Contains(message, "landfall login") {
		t.Errorf("message must offer a runnable fix: %q", message)
	}
}

func TestExplainInstanceMismatch_ACredentialPredatingInstanceTrackingIsAMismatch(t *testing.T) {
	// The real shape found on the operator's machine: a valid-looking
	// credential with no `instance` field at all. Assuming it matches would
	// send a token to a deployment that never issued it. This is the rule
	// most likely to be "simplified" by a later reader into
	// `if recorded != "" && recorded != resolved` — that inversion is the
	// bug, not a cleanup (see explain.go's own doc comment).
	withCredentials(t, &Credentials{
		AccessToken: "x",
		ExpiresAt:   ptrInt64(time.Now().Add(10 * time.Minute).UnixMilli()),
		OrgSlug:     ptrString("landfall"),
		Iss:         "landfall-core",
		// Instance intentionally left nil.
	})

	message := ExplainInstanceMismatch(hosted)
	if message == "" {
		t.Fatal("an untracked credential must not be silently trusted")
	}
	if !strings.Contains(strings.ToLower(message), "predates instance tracking") {
		t.Errorf("message = %q, want it to say the credential predates instance tracking", message)
	}
	if !strings.Contains(message, "landfall login") {
		t.Errorf("message must offer a runnable fix: %q", message)
	}
}

func TestExplainInstanceMismatch_ASessionForTheInstanceInUsePassesSilently(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken: "x",
		ExpiresAt:   ptrInt64(time.Now().Add(10 * time.Minute).UnixMilli()),
		Instance:    &instance.Instance{API: hosted.API},
	})

	if got := ExplainInstanceMismatch(hosted); got != "" {
		t.Errorf("ExplainInstanceMismatch = %q, want empty for a matching instance", got)
	}
}

func TestExplainInstanceMismatch_NoCachedSessionAtAllIsNotAMismatch(t *testing.T) {
	withCredentials(t, nil)

	if got := ExplainInstanceMismatch(hosted); got != "" {
		t.Errorf("ExplainInstanceMismatch = %q, want empty when nothing is cached", got)
	}
}
