package auth

// cli_handoff_poll_test.go ports test/cli-handoff-poll.test.mjs: the
// browser-handoff poll treats 404 ("not yet", indistinguishable on purpose
// from "never existed"), a non-2xx status, a network error, and an
// unparseable body all identically — keep polling until the deadline — and
// resolves the org-slug precedence (server wins, caller is only a fallback).
//
// PollOptions.Interval has different zero-value semantics than the Node
// suite's intervalMs: zero here means the real PollInterval default (so a
// bare PollOptions{} can never hammer the endpoint), and a NEGATIVE value is
// what buys an immediate, no-wait poll for a test — see login.go's own doc
// comment on PollOptions.Interval. Every test below that wants the Node
// suite's intervalMs:0 behaviour therefore passes Interval: -1.
import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestPollForHandoff_ResolvesOnTheFirst200WithTheMintedSession(t *testing.T) {
	d := &fakeDoer{handler: func(_ int, url string, body map[string]any) (*http.Response, error) {
		if want := testAPI + "/auth/cli-handoff/poll"; url != want {
			t.Errorf("url = %q, want %q", url, want)
		}
		return jsonResponse(t, 200, map[string]any{
			"accessToken":  "access-1",
			"refreshToken": "lf_refresh_1",
			"expiresAt":    futureRFC3339(time.Hour),
			"orgSlug":      "acme",
		}), nil
	}}

	session, err := PollForHandoff(context.Background(), testAPI, "the-nonce", "", PollOptions{Client: d, Interval: -1})
	if err != nil {
		t.Fatalf("PollForHandoff: %v", err)
	}
	if session.AccessToken != "access-1" {
		t.Errorf("AccessToken = %q", session.AccessToken)
	}
	if session.RefreshToken == nil || *session.RefreshToken != "lf_refresh_1" {
		t.Errorf("RefreshToken = %v", session.RefreshToken)
	}
	if session.OrgSlug == nil || *session.OrgSlug != "acme" {
		t.Errorf("OrgSlug = %v", session.OrgSlug)
	}
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(d.calls))
	}
	if d.calls[0].body["nonce"] != "the-nonce" {
		t.Errorf("request body = %v, want nonce=the-nonce", d.calls[0].body)
	}
}

func TestPollForHandoff_A404KeepsPollingUntilItSucceeds(t *testing.T) {
	// 404 is "not yet", indistinguishable on purpose from "never existed".
	d := &fakeDoer{handler: func(callNumber int, _ string, _ map[string]any) (*http.Response, error) {
		if callNumber < 3 {
			return jsonResponse(t, 404, map[string]any{"error": "not_found"}), nil
		}
		return jsonResponse(t, 200, map[string]any{
			"accessToken": "access-eventually",
			"expiresAt":   futureRFC3339(time.Hour),
		}), nil
	}}

	session, err := PollForHandoff(context.Background(), testAPI, "the-nonce", "", PollOptions{Client: d, Interval: -1})
	if err != nil {
		t.Fatalf("PollForHandoff: %v", err)
	}
	if session.AccessToken != "access-eventually" {
		t.Errorf("AccessToken = %q", session.AccessToken)
	}
	if len(d.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(d.calls))
	}
}

func TestPollForHandoff_ANetworkErrorIsToleratedNotThrown(t *testing.T) {
	// The browser side can take minutes; one failed request is never a
	// reason to give up early.
	attempt := 0
	d := &fakeDoer{handler: func(_ int, _ string, _ map[string]any) (*http.Response, error) {
		attempt++
		if attempt < 3 {
			return nil, errors.New("ENOTFOUND")
		}
		return jsonResponse(t, 200, map[string]any{
			"accessToken": "access-after-blip",
			"expiresAt":   futureRFC3339(time.Hour),
		}), nil
	}}

	session, err := PollForHandoff(context.Background(), testAPI, "the-nonce", "", PollOptions{Client: d, Interval: -1})
	if err != nil {
		t.Fatalf("PollForHandoff: %v", err)
	}
	if session.AccessToken != "access-after-blip" {
		t.Errorf("AccessToken = %q", session.AccessToken)
	}
	if attempt != 3 {
		t.Errorf("attempt = %d, want 3", attempt)
	}
}

func TestPollForHandoff_GivesUpAtTheTimeoutNeverHangingForever(t *testing.T) {
	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		return jsonResponse(t, 404, map[string]any{"error": "not_found"}), nil
	}}

	_, err := PollForHandoff(context.Background(), testAPI, "the-nonce", "", PollOptions{
		Client: d, Interval: 5 * time.Millisecond, Timeout: 30 * time.Millisecond,
	})
	if !errors.Is(err, ErrHandoffTimeout) {
		t.Fatalf("err = %v, want ErrHandoffTimeout", err)
	}
}

func TestPollForHandoff_FallsBackToTheCallerSuppliedOrgSlugOnlyWhenTheServerOmitsOne(t *testing.T) {
	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		return jsonResponse(t, 200, map[string]any{
			"accessToken": "x",
			"expiresAt":   time.Now().Format(time.RFC3339),
			"orgSlug":     nil,
		}), nil
	}}

	session, err := PollForHandoff(context.Background(), testAPI, "the-nonce", "fallback-org", PollOptions{Client: d, Interval: -1})
	if err != nil {
		t.Fatalf("PollForHandoff: %v", err)
	}
	if session.OrgSlug == nil || *session.OrgSlug != "fallback-org" {
		t.Errorf("OrgSlug = %v, want fallback-org", session.OrgSlug)
	}
}

func TestPollForHandoff_TheServerSuppliedOrgSlugWinsOverTheCallerSuppliedOneWhenBothArePresent(t *testing.T) {
	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		return jsonResponse(t, 200, map[string]any{
			"accessToken": "x",
			"expiresAt":   time.Now().Format(time.RFC3339),
			"orgSlug":     "server-org",
		}), nil
	}}

	session, err := PollForHandoff(context.Background(), testAPI, "the-nonce", "caller-org", PollOptions{Client: d, Interval: -1})
	if err != nil {
		t.Fatalf("PollForHandoff: %v", err)
	}
	if session.OrgSlug == nil || *session.OrgSlug != "server-org" {
		t.Errorf("OrgSlug = %v, want server-org", session.OrgSlug)
	}
}

func TestPollForHandoff_AMalformedJSONResponseIsToleratedAsNotYet(t *testing.T) {
	d := &fakeDoer{handler: func(callNumber int, _ string, _ map[string]any) (*http.Response, error) {
		if callNumber == 1 {
			return malformedJSONResponse(200), nil
		}

		return jsonResponse(t, 200, map[string]any{
			"accessToken": "ok",
			"expiresAt":   futureRFC3339(time.Hour),
		}), nil
	}}

	session, err := PollForHandoff(context.Background(), testAPI, "the-nonce", "", PollOptions{Client: d, Interval: -1})
	if err != nil {
		t.Fatalf("PollForHandoff: %v", err)
	}
	if session.AccessToken != "ok" {
		t.Errorf("AccessToken = %q", session.AccessToken)
	}
}
