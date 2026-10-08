package cli

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
)

// joinFixture is a Landfall that mints a short link for inc-1 with the
// person's token and redeems it for a room session.
func joinFixture(t *testing.T) (*fakeLandfall, JoinIncidentDeps, *joinRecord) {
	f := newFakeLandfall(t)
	f.handle("POST /o/acme/incidents/inc-1/edge/share-link", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+personToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON200(w, map[string]any{"shareUrl": f.srv.URL + "/j/code123", "instructions": "…", "expiresAt": "2026-10-08T17:00:00Z"})
	})
	f.handle("POST /j/code123/redeem", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON200(w, map[string]any{"token": roomToken, "humanActorId": "user-1", "slug": "acme", "incidentId": "inc-1", "kind": "member"})
	})
	rec := &joinRecord{}
	read := f.deps(true)
	deps := JoinIncidentDeps{
		Org:          read.Org,
		Rooms:        read.Rooms,
		EnsureDaemon: func() bool { rec.ensured = true; return true },
		Redeem: func(ctx context.Context, link string) (*client.Config, error) {
			rec.redeemed = link
			cfg, err := client.RedeemShareLink(ctx, link, client.RedeemOptions{})
			if err != nil {
				return nil, err
			}
			return &cfg, nil
		},
		Attach: func(cfg client.Config, linkHash string) (*daemon.Response, error) {
			rec.cfg, rec.linkHash = cfg, linkHash
			return &daemon.Response{OK: true, RoomKey: "rk1", Frame: &client.ContextFrame{Incident: client.Incident{DisplayID: "Acme 7", Title: "web-edge 5xx"}}}, nil
		},
	}
	return f, deps, rec
}

type joinRecord struct {
	ensured  bool
	redeemed string
	cfg      client.Config
	linkHash string
}

func TestJoinIncidentMintsAndRedeemsALink(t *testing.T) {
	f, deps, rec := joinFixture(t)
	ans := roundTrip(t, RunJoinIncident(context.Background(), "inc-1", deps))
	link := f.srv.URL + "/j/code123"
	if ans["ok"] != true || ans["roomKey"] != "rk1" || ans["displayId"] != "Acme 7" || ans["title"] != "web-edge 5xx" || ans["link"] != link || ans["incidentId"] != "inc-1" {
		t.Fatalf("answer: %v", ans)
	}
	// The room holds the redeemed edge session, never the login token.
	if rec.cfg.Token != roomToken || rec.cfg.Slug != "acme" || rec.cfg.IncidentID != "inc-1" {
		t.Fatalf("attached with %+v, want the redeemed room session", rec.cfg)
	}
	if rec.redeemed != link || rec.linkHash != daemon.LinkHash(link) || !rec.ensured {
		t.Fatalf("record: %+v", rec)
	}
	mints := f.callsTo("/o/acme/incidents/inc-1/edge/share-link")
	if len(mints) != 1 || mints[0].Token != personToken {
		t.Fatalf("mint calls: %+v", mints)
	}
}

func TestJoinIncidentNamesTheRoomFromTheDaemonWhenTheFrameIsLate(t *testing.T) {
	_, deps, _ := joinFixture(t)
	deps.Attach = func(client.Config, string) (*daemon.Response, error) {
		return &daemon.Response{OK: true, RoomKey: "rk1"}, nil
	}
	ans := RunJoinIncident(context.Background(), "inc-1", deps)
	if ans["displayId"] != "Acme 7" || ans["title"] != "web-edge 5xx" {
		t.Fatalf("answer: %v", ans)
	}
}

func TestJoinIncidentErrors(t *testing.T) {
	ctx := context.Background()

	_, deps, _ := joinFixture(t)
	wantFailure(t, RunJoinIncident(ctx, "  ", deps), "Name the incident to join: landfall join --incident <incidentId>.")

	_, deps, _ = joinFixture(t)
	deps.Org = noRoomDeps().Org
	wantFailure(t, RunJoinIncident(ctx, "inc-1", deps), "Sign in to join an incident from here: run landfall login.")

	_, deps, _ = joinFixture(t)
	wantFailure(t, RunJoinIncident(ctx, "inc-404", deps), "Landfall has no incident inc-404 in acme.")

	f, deps, rec := joinFixture(t)
	f.status("POST /o/acme/incidents/inc-1/edge/share-link", http.StatusForbidden)
	wantFailure(t, RunJoinIncident(ctx, "inc-1", deps), "Landfall would not let you join that incident. Your sign-in may be for another organization.")
	if rec.ensured {
		t.Fatal("a refused mint must not start the daemon")
	}

	_, deps, _ = joinFixture(t)
	deps.EnsureDaemon = func() bool { return false }
	wantFailure(t, RunJoinIncident(ctx, "inc-1", deps), "The room daemon could not start here, so this folder cannot hold the room. Paste the share link into your agent instead.")

	f, deps, _ = joinFixture(t)
	f.status("POST /j/code123/redeem", http.StatusGone)
	wantFailure(t, RunJoinIncident(ctx, "inc-1", deps), "The join link would not redeem. Try again, or open the room from the web app.")

	_, deps, _ = joinFixture(t)
	deps.Attach = func(client.Config, string) (*daemon.Response, error) { return nil, errors.New("timeout") }
	wantFailure(t, RunJoinIncident(ctx, "inc-1", deps), "The room daemon could not join the room. Try again shortly.")
}

func TestJoinIncidentFlags(t *testing.T) {
	for _, tc := range []struct {
		args           []string
		incident, host string
		ok             bool
	}{
		{[]string{"--incident", "inc-1", "--host", "claude-code"}, "inc-1", "claude-code", true},
		{[]string{"--host=codex", "--incident=inc-2"}, "inc-2", "codex", true},
		{[]string{"--incident"}, "", "", true},
		{[]string{"https://app.landfalls.ai/j/abc"}, "", "", false},
		{nil, "", "", false},
	} {
		inc, host, ok := joinIncidentFlags(tc.args)
		if inc != tc.incident || host != tc.host || ok != tc.ok {
			t.Fatalf("%v → %q %q %v", tc.args, inc, host, ok)
		}
	}
}
