package cli

// remediation_test.go ports test/remediation.test.mjs's 16 cases for
// `landfall remediation approve`. Same injectable-deps harness convention as
// connect_aws_test.go — the network is faked, nothing here makes a real HTTP
// call.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type remediationHarness struct {
	errBuf bytes.Buffer

	status int
	body   map[string]any

	calls    []apiCallRecord
	getToken func(ctx context.Context) string
	getSlug  func() string
}

func newRemediationHarness() *remediationHarness {
	return &remediationHarness{
		status:   202,
		body:     map[string]any{},
		getToken: func(context.Context) string { return "tok-123" },
		getSlug:  func() string { return "acme" },
	}
}

func (h *remediationHarness) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	h.calls = append(h.calls, apiCallRecord{URL: req.URL.String(), Method: req.Method, Body: body})
	return jsonResponse(h.status, h.body), nil
}

func (h *remediationHarness) ui() *UI { return &UI{Out: io.Discard, Err: &h.errBuf} }

func (h *remediationHarness) errText() string { return h.errBuf.String() }

func (h *remediationHarness) deps() RemediationApproveDeps {
	return RemediationApproveDeps{
		Doer:       fakeDoer(h.Do),
		GetToken:   h.getToken,
		GetOrgSlug: h.getSlug,
		BaseURL:    "https://api.test",
		Env:        func(string) string { return "" },
	}
}

func (h *remediationHarness) firstBody(t *testing.T) map[string]any {
	t.Helper()
	if len(h.calls) == 0 {
		t.Fatal("no request recorded")
	}
	var decoded map[string]any
	if err := json.Unmarshal(h.calls[0].Body, &decoded); err != nil {
		t.Fatalf("could not decode request body: %v", err)
	}
	return decoded
}

// -------------------------------------------------------------- flag parsing

func TestParseRemediationApproveFlags_RequiresRemediationID(t *testing.T) {
	_, err := ParseRemediationApproveFlags([]string{"--incident", "inc-1"})
	if err == nil || !strings.Contains(err.Error(), "usage: landfall remediation approve") {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

func TestParseRemediationApproveFlags_RequiresIncident(t *testing.T) {
	_, err := ParseRemediationApproveFlags([]string{"rem-123"})
	if err == nil || !strings.Contains(err.Error(), "requires --incident") {
		t.Fatalf("err = %v, want an --incident error", err)
	}
}

func TestParseRemediationApproveFlags_HappyPath(t *testing.T) {
	flags, err := ParseRemediationApproveFlags([]string{
		"rem-123", "--incident", "inc-1", "--org", "acme", "--override", "solo on-call",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := RemediationApproveFlags{
		RemediationID: "rem-123",
		Incident:      "inc-1",
		Org:           "acme",
		Version:       "",
		Override:      &RemediationOverride{Reason: "solo on-call"},
	}
	if flags.RemediationID != want.RemediationID || flags.Incident != want.Incident ||
		flags.Org != want.Org || flags.Version != want.Version {
		t.Fatalf("flags = %+v, want %+v", flags, want)
	}
	if flags.Override == nil || flags.Override.Reason != want.Override.Reason {
		t.Fatalf("override = %+v, want %+v", flags.Override, want.Override)
	}
}

// -------------------------------------------------------------- session gating

func TestRunRemediationApprove_NoSession(t *testing.T) {
	h := newRemediationHarness()
	h.getToken = func(context.Context) string { return "" }
	code := RunRemediationApprove(context.Background(), RemediationApproveFlags{RemediationID: "rem-1", Incident: "inc-1"}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), "landfall login") {
		t.Fatalf("errs = %q", h.errText())
	}
	if len(h.calls) != 0 {
		t.Fatalf("calls = %v, want none", h.calls)
	}
}

func TestRunRemediationApprove_NoOrgPin(t *testing.T) {
	h := newRemediationHarness()
	h.getSlug = func() string { return "" }
	code := RunRemediationApprove(context.Background(), RemediationApproveFlags{RemediationID: "rem-1", Incident: "inc-1"}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), "no organization pin") {
		t.Fatalf("errs = %q", h.errText())
	}
	if len(h.calls) != 0 {
		t.Fatalf("calls = %v, want none", h.calls)
	}
}

func TestRunRemediationApprove_OrgMismatch(t *testing.T) {
	h := newRemediationHarness()
	code := RunRemediationApprove(context.Background(), RemediationApproveFlags{RemediationID: "rem-1", Incident: "inc-1", Org: "other-co"}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), `signed in to "acme", not "other-co"`) {
		t.Fatalf("errs = %q", h.errText())
	}
	if len(h.calls) != 0 {
		t.Fatalf("calls = %v, want none", h.calls)
	}
}

// -------------------------------------------------------------- the real request shape

func TestRunRemediationApprove_RealEndpointPath(t *testing.T) {
	h := newRemediationHarness()
	RunRemediationApprove(context.Background(), RemediationApproveFlags{RemediationID: "rem-mit-1", Incident: "inc-1"}, h.ui(), h.deps())
	if len(h.calls) != 1 {
		t.Fatalf("calls = %v, want exactly 1", h.calls)
	}
	want := "https://api.test/o/acme/incidents/inc-1/remediation/proposals/rem-mit-1/approve"
	if h.calls[0].URL != want {
		t.Fatalf("url = %q, want %q", h.calls[0].URL, want)
	}
	if h.calls[0].Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", h.calls[0].Method)
	}
}

func TestRunRemediationApprove_OmitsVersionWhenNotPassed(t *testing.T) {
	h := newRemediationHarness()
	RunRemediationApprove(context.Background(), RemediationApproveFlags{RemediationID: "rem-1", Incident: "inc-1"}, h.ui(), h.deps())
	body := h.firstBody(t)
	if _, ok := body["version"]; ok {
		t.Fatalf("body = %v, want no version key", body)
	}
}

func TestRunRemediationApprove_IncludesVersionWhenPassed(t *testing.T) {
	h := newRemediationHarness()
	RunRemediationApprove(context.Background(), RemediationApproveFlags{RemediationID: "rem-1", Incident: "inc-1", Version: "v7"}, h.ui(), h.deps())
	body := h.firstBody(t)
	if body["version"] != "v7" {
		t.Fatalf("body = %v, want version=v7", body)
	}
}

func TestRunRemediationApprove_PlainApproveSucceeds(t *testing.T) {
	h := newRemediationHarness()
	code := RunRemediationApprove(context.Background(), RemediationApproveFlags{RemediationID: "rem-1", Incident: "inc-1"}, h.ui(), h.deps())
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(h.errText(), "✓ Approved.") {
		t.Fatalf("errs = %q", h.errText())
	}
}

func TestRunRemediationApprove_OverrideApprove(t *testing.T) {
	h := newRemediationHarness()
	code := RunRemediationApprove(context.Background(), RemediationApproveFlags{
		RemediationID: "rem-1", Incident: "inc-1",
		Override: &RemediationOverride{Reason: "solo on-call, bar unreachable"},
	}, h.ui(), h.deps())
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	body := h.firstBody(t)
	want := map[string]any{"reason": "solo on-call, bar unreachable"}
	if !reflect.DeepEqual(body["override"], want) {
		t.Fatalf("override = %v, want %v", body["override"], want)
	}
	if !strings.Contains(h.errText(), `override recorded: reason="solo on-call, bar unreachable"`) {
		t.Fatalf("errs = %q", h.errText())
	}
}

// -------------------------------------------------------------- server refusals, named

func TestRunRemediationApprove_OverrideRequiresHumanSession(t *testing.T) {
	h := newRemediationHarness()
	h.status = 403
	h.body = map[string]any{"error": "override_requires_human_session"}
	code := RunRemediationApprove(context.Background(), RemediationApproveFlags{
		RemediationID: "rem-1", Incident: "inc-1", Override: &RemediationOverride{Reason: "x"},
	}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), "never an API key") {
		t.Fatalf("errs = %q", h.errText())
	}
}

func TestRunRemediationApprove_NotAdmittedWithoutOverride(t *testing.T) {
	h := newRemediationHarness()
	h.status = 400
	h.body = map[string]any{"error": "remediation_not_admitted", "shortfall": "1/2 corroborators"}
	code := RunRemediationApprove(context.Background(), RemediationApproveFlags{RemediationID: "rem-1", Incident: "inc-1"}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	err := h.errText()
	if !strings.Contains(err, "1/2 corroborators") {
		t.Fatalf("errs = %q", err)
	}
	if !strings.Contains(err, "--override") {
		t.Fatalf("errs = %q, want the --override suggestion", err)
	}
}

func TestRunRemediationApprove_NotAdmittedWithOverride(t *testing.T) {
	h := newRemediationHarness()
	h.status = 400
	h.body = map[string]any{"error": "remediation_not_admitted", "shortfall": "stale"}
	code := RunRemediationApprove(context.Background(), RemediationApproveFlags{
		RemediationID: "rem-1", Incident: "inc-1", Override: &RemediationOverride{Reason: "x"},
	}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), "was not accepted") {
		t.Fatalf("errs = %q, want it to report the override was not accepted", h.errText())
	}
}

func TestRunRemediationApprove_OverrideReasonRequired(t *testing.T) {
	h := newRemediationHarness()
	h.status = 400
	h.body = map[string]any{"error": "override_reason_required"}
	code := RunRemediationApprove(context.Background(), RemediationApproveFlags{
		RemediationID: "rem-1", Incident: "inc-1", Override: &RemediationOverride{Reason: ""},
	}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), `--override "why you are overriding this"`) {
		t.Fatalf("errs = %q", h.errText())
	}
}

func TestRunRemediationApprove_UnrecognizedFailure(t *testing.T) {
	h := newRemediationHarness()
	h.status = 500
	h.body = map[string]any{"message": "boom"}
	code := RunRemediationApprove(context.Background(), RemediationApproveFlags{RemediationID: "rem-1", Incident: "inc-1"}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	err := h.errText()
	if !strings.Contains(err, "HTTP 500") {
		t.Fatalf("errs = %q", err)
	}
	if !strings.Contains(err, "boom") {
		t.Fatalf("errs = %q", err)
	}
}
