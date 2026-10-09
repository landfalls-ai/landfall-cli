package cli

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
)

func TestLineKeyOf(t *testing.T) {
	cases := map[string]string{
		"eu-west-1 5xx":           "eu-west-1-5xx",
		"  CloudFront  Origin/B ": "cloudfront-origin-b",
		"--dns--":                 "dns",
		"!!!":                     "",
		"Café élan":               "caf-lan",
		strings.Repeat("a", 200):  strings.Repeat("a", 128),
	}
	for in, want := range cases {
		if got := LineKeyOf(in); got != want {
			t.Errorf("LineKeyOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinesClaim(t *testing.T) {
	cases := []struct {
		name     string
		who      who
		label    string
		status   int
		body     string
		want     map[string]any
		wantAuth string
	}{
		{"claimed as the person", personSignedIn, "eu-west-1 5xx", 201, `{"outcome":"claimed","lineKey":"eu-west-1-5xx","claimId":"c-9","seq":88}`,
			map[string]any{"ok": true, "claimId": "c-9", "lineKey": "eu-west-1-5xx", "label": "eu-west-1 5xx"}, "Bearer oauth-tok"},
		{"someone holds it already", personSignedIn, "eu-west-1 5xx", 201, `{"outcome":"lost","lineKey":"eu-west-1-5xx","heldBy":{"displayName":"Dana","kind":"member"},"claimId":"c-1","seq":40}`,
			map[string]any{"ok": false, "error": "Dana already holds this line. Help them, or claim another.", "heldBy": "Dana", "claimId": "c-1", "lineKey": "eu-west-1-5xx"}, "Bearer oauth-tok"},
		{"not signed in, a teammate's link", personNotSignedIn, "eu-west-1 5xx", 201, `{}`,
			map[string]any{"ok": false, "error": "Sign in to claim a line as yourself: run landfall login."}, ""},
		{"a guest claims as themself", personGuest, "dns", 201, `{"outcome":"claimed","lineKey":"dns","claimId":"c-2","seq":5}`,
			map[string]any{"ok": true, "claimId": "c-2", "lineKey": "dns", "label": "dns"}, "Bearer room-tok"},
		{"the server refuses an agent", personOwnLink, "dns", 409, `{"message":"a line of investigation is owned by a person — an agent contributes to one, it does not hold it"}`,
			map[string]any{"ok": false, "error": "A line belongs to a person, and this session speaks for an agent. Run landfall login, then try again."}, "Bearer room-tok"},
		{"a server without lines", personSignedIn, "dns", 404, `{}`,
			map[string]any{"ok": false, "error": "Lines of investigation are not available on this Landfall yet."}, "Bearer oauth-tok"},
		{"no label", personSignedIn, "  ", 201, `{}`,
			map[string]any{"ok": false, "error": `Name the line with --label, for example --label "eu-west-1 5xx".`}, ""},
		{"a label with no letters", personSignedIn, "???", 201, `{}`,
			map[string]any{"ok": false, "error": "Name the line with some letters or numbers."}, ""},
		{"a label on two lines", personSignedIn, "a\nb", 201, `{}`,
			map[string]any{"ok": false, "error": "Keep the line's label to one line."}, ""},
		{"a label too long", personSignedIn, strings.Repeat("x", 121), 201, `{}`,
			map[string]any{"ok": false, "error": "Keep the line's label to 120 characters."}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newPersonFakeLandfall(t, c.status, c.body)
			got := personRoundTrip(t, RunLinesClaim(context.Background(), LinesFlags{Label: c.label}, personDeps(f, c.who)))
			if !sameAnswer(got, c.want) {
				t.Fatalf("answer = %v, want %v", got, c.want)
			}
			reqs := f.requests()
			if c.wantAuth == "" {
				if len(reqs) != 0 {
					t.Fatalf("the server was called: %+v", reqs)
				}
				return
			}
			if len(reqs) != 1 {
				t.Fatalf("requests = %+v", reqs)
			}
			r := reqs[0]
			if r.Method != http.MethodPost || r.Path != "/o/acme/incidents/inc-1/edge/lines" || r.Auth != c.wantAuth {
				t.Fatalf("request = %+v", r)
			}
			// The server's schema is strict: these three keys and nothing else.
			keys := make([]string, 0, len(r.Body))
			for k := range r.Body {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if strings.Join(keys, ",") != "idempotencyKey,label,lineKey" || r.Body["lineKey"] != LineKeyOf(c.label) || r.Body["label"] != strings.TrimSpace(c.label) {
				t.Fatalf("body = %v", r.Body)
			}
		})
	}
}

func TestLinesRelease(t *testing.T) {
	cases := []struct {
		name     string
		who      who
		claim    string
		status   int
		body     string
		want     map[string]any
		wantAuth string
	}{
		{"released", personSignedIn, "c-1", 200, `{"outcome":"released","claimId":"c-1","seq":90}`, map[string]any{"ok": true}, "Bearer oauth-tok"},
		{"already released", personSignedIn, "c-1", 200, `{"outcome":"not-held","claimId":"c-1","seq":70}`,
			map[string]any{"ok": true, "note": "That line was already released."}, "Bearer oauth-tok"},
		{"someone else holds it", personSignedIn, "c-1", 409, `{"message":"that line is held by Dana — ask them to release it, or take it over explicitly"}`,
			map[string]any{"ok": false, "error": "Someone else holds that line, so only they can release it."}, "Bearer oauth-tok"},
		{"not signed in", personNotSignedIn, "c-1", 200, `{}`,
			map[string]any{"ok": false, "error": "Sign in to release a line as yourself: run landfall login."}, ""},
		{"an expired sign-in", personSignedIn, "c-1", 401, `{}`,
			map[string]any{"ok": false, "error": "Your sign-in expired. Run landfall login."}, "Bearer oauth-tok"},
		{"no claim named", personSignedIn, "", 200, `{}`,
			map[string]any{"ok": false, "error": "Name the line with --claim and its claim id."}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newPersonFakeLandfall(t, c.status, c.body)
			got := personRoundTrip(t, RunLinesRelease(context.Background(), LinesFlags{Claim: c.claim}, personDeps(f, c.who)))
			if !sameAnswer(got, c.want) {
				t.Fatalf("answer = %v, want %v", got, c.want)
			}
			reqs := f.requests()
			if c.wantAuth == "" {
				if len(reqs) != 0 {
					t.Fatalf("the server was called: %+v", reqs)
				}
				return
			}
			if len(reqs) != 1 {
				t.Fatalf("requests = %+v", reqs)
			}
			r := reqs[0]
			if r.Method != http.MethodDelete || r.Path != "/o/acme/incidents/inc-1/edge/lines/"+c.claim || r.Auth != c.wantAuth {
				t.Fatalf("request = %+v", r)
			}
			if len(r.Body) != 1 || r.Body["idempotencyKey"] != "key-0123456789" {
				t.Fatalf("body = %v", r.Body)
			}
		})
	}
}

// TestPersonAnswersHaveNoEmDash: nothing these commands print to a person
// carries an em dash, even when the server's own words do.
func TestPersonAnswersHaveNoEmDash(t *testing.T) {
	f := newPersonFakeLandfall(t, 409, `{"message":"something odd — happened"}`)
	for _, got := range []map[string]any{
		RunVote(context.Background(), VoteFlags{Claim: 1, Position: "corroborate"}, personDeps(f, personSignedIn)),
		RunLinesClaim(context.Background(), LinesFlags{Label: "dns"}, personDeps(f, personSignedIn)),
		RunLinesRelease(context.Background(), LinesFlags{Claim: "c"}, personDeps(f, personSignedIn)),
	} {
		if s, _ := got["error"].(string); s == "" || strings.Contains(s, "—") {
			t.Fatalf("answer = %v", got)
		}
	}
}
