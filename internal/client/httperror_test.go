package client

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestARejectionIsATypedHTTPErrorWithTheSameText: callers that only print the
// error see exactly what they always did; a caller that must tell a refusal
// from a hiccup (the bridge worker) can, without parsing text.
func TestARejectionIsATypedHTTPErrorWithTheSameText(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/claims": {status: 400, body: `{"statusCode":400,"message":"statement required","error":"Bad Request"}`},
	})
	c := New(cfg, d)
	err := c.StageClaim(context.Background(), map[string]any{})
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("err = %T %v, want *HTTPError", err, err)
	}
	if he.Status != 400 || he.Reason != "statement required" {
		t.Fatalf("HTTPError = %+v", he)
	}
	if got, want := err.Error(), "/claims → HTTP 400: statement required"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestAValidatorMessageListIsKeptAsTheReason: NestJS answers a validation
// failure with an array of messages; the reason names them rather than
// collapsing to a bare status.
func TestAValidatorMessageListIsKeptAsTheReason(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/edge/contributions": {status: 400, body: `{"message":["text must be a string","kind is required"]}`},
	})
	err := New(cfg, d).Contribute(context.Background(), "finding", map[string]any{})
	var he *HTTPError
	if !errors.As(err, &he) || he.Reason != "text must be a string; kind is required" {
		t.Fatalf("err = %v", err)
	}
}

// TestAReasonFieldWinsOverMessage: the artifact policy answers with
// `reason`, which was already preferred before the type existed.
func TestAReasonFieldWinsOverMessage(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/artifacts": {status: 413, body: `{"reason":"over the 5 MiB limit","message":"Payload Too Large"}`},
	})
	_, err := New(cfg, d).UploadArtifact(context.Background(), "a.txt", "text/plain", "eA==")
	var he *HTTPError
	if !errors.As(err, &he) || he.Reason != "over the 5 MiB limit" {
		t.Fatalf("err = %v", err)
	}
}

func TestRefusalIsEveryClientErrorExceptNotNow(t *testing.T) {
	for status, want := range map[int]bool{
		400: true, 403: true, 404: true, 413: true, 422: true,
		401: false, 408: false, 425: false, 429: false,
		500: false, 502: false, 503: false, 302: false,
	} {
		err := fmt.Errorf("publish: %w", &HTTPError{Path: "/x", Status: status, Reason: "no"})
		if _, got := Refusal(err); got != want {
			t.Errorf("Refusal(HTTP %d) = %v, want %v", status, got, want)
		}
	}
	if _, got := Refusal(errors.New("connection refused")); got {
		t.Error("a transport failure is not a refusal")
	}
	if _, got := Refusal(nil); got {
		t.Error("nil is not a refusal")
	}
}

// TestAConflictIsARefusalOnlyWhenItNamesTheRoomsState: core-api answers 409
// both when a write loses an append race in a busy room (worth another try:
// nothing was appended) and when the room's state rules the write out (a
// closed engagement). Only the second is final.
func TestAConflictIsARefusalOnlyWhenItNamesTheRoomsState(t *testing.T) {
	for reason, want := range map[string]bool{
		// edge.service.ts and admission.service.ts, verbatim.
		"could not append edge event after retries":                false,
		"could not append claim event after retries":               false,
		"org-memory event append: seq allocation did not converge": false,
		"expectedSeq stale, retry":                                 false,
		"":                                                         false,
		"engagement is closed; admission is frozen":                true,
		"incident is closed; vetting is frozen":                    true,
		"that proposal is already applied":                         true,
	} {
		err := fmt.Errorf("publish: %w", &HTTPError{Path: "/claims", Status: 409, Reason: reason})
		if _, got := Refusal(err); got != want {
			t.Errorf("Refusal(HTTP 409 %q) = %v, want %v", reason, got, want)
		}
	}
}

// TestSessionExpiredIsA401Only: the worker keeps a hand-off that drew one
// queued for the next session and says why; nothing else reads as expiry.
func TestSessionExpiredIsA401Only(t *testing.T) {
	if !SessionExpired(fmt.Errorf("publish: %w", &HTTPError{Path: "/edge/contributions", Status: 401, Reason: "Unauthorized"})) {
		t.Error("HTTP 401 is an expired session")
	}
	for _, err := range []error{
		&HTTPError{Path: "/x", Status: 403},
		&HTTPError{Path: "/x", Status: 500},
		errors.New("connection refused"),
		nil,
	} {
		if SessionExpired(err) {
			t.Errorf("SessionExpired(%v) = true", err)
		}
	}
}
