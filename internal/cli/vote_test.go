package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

// seen is one request a fake Landfall received.
type seen struct {
	Method, Path, Auth string
	Body               map[string]any
}

// personFakeLandfall answers every request with status and body, and records it.
type personFakeLandfall struct {
	mu     sync.Mutex
	got    []seen
	status int
	body   string
	srv    *httptest.Server
}

func newPersonFakeLandfall(t *testing.T, status int, body string) *personFakeLandfall {
	t.Helper()
	f := &personFakeLandfall{status: status, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		f.mu.Lock()
		f.got = append(f.got, seen{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("authorization"), Body: b})
		f.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *personFakeLandfall) requests() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.got...)
}

// fakeJWT is a token whose payload names sub; nothing verifies it here.
func fakeJWT(sub string) string {
	payload, _ := json.Marshal(map[string]string{"sub": sub})
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// who is the person's situation on this machine.
type who struct {
	oauth, slug, sub string
	roomHuman        string
}

func personDeps(f *personFakeLandfall, w who) PersonDeps {
	return PersonDeps{
		Room: func(string) (client.Config, error) {
			return client.Config{BaseURL: f.srv.URL, Slug: "acme", IncidentID: "inc-1", Token: "room-tok", HumanActorID: w.roomHuman}, nil
		},
		OAuthToken:   func(context.Context) string { return w.oauth },
		OAuthSlug:    func() string { return w.slug },
		OAuthSubject: func() string { return w.sub },
		NewKey:       func() string { return "key-0123456789" },
	}
}

var (
	personSignedIn     = who{oauth: "oauth-tok", slug: "acme", sub: "u-alice", roomHuman: "u-teammate"}
	personNotSignedIn  = who{roomHuman: "u-teammate"}
	personOwnLink      = who{sub: "u-alice", roomHuman: "u-alice"}
	personOtherOrg     = who{oauth: "oauth-tok", slug: "other", sub: "u-alice", roomHuman: "u-alice"}
	personOtherOrgLink = who{oauth: "oauth-tok", slug: "other", sub: "u-alice", roomHuman: "u-bob"}
	personGuest        = who{roomHuman: "guest:5f0c"}
)

func TestVote(t *testing.T) {
	cases := []struct {
		name     string
		who      who
		flags    VoteFlags
		status   int
		body     string
		want     map[string]any
		wantAuth string // "" means the server must not be called
	}{
		{"signed in: the person's own token", personSignedIn, VoteFlags{Claim: 212, Position: "corroborate"}, 202, `{"ok":true,"state":"admitted"}`,
			map[string]any{"ok": true, "claimSeq": float64(212), "position": "corroborate", "admitted": true}, "Bearer oauth-tok"},
		{"contest with a reason", personSignedIn, VoteFlags{Claim: 212, Position: "Contest", Reason: "the deploy\nwas at 15:48Z"}, 202, `{"ok":true,"state":"staged"}`,
			map[string]any{"ok": true, "claimSeq": float64(212), "position": "contest", "admitted": false}, "Bearer oauth-tok"},
		{"not signed in, a teammate's link", personNotSignedIn, VoteFlags{Claim: 212, Position: "corroborate"}, 202, `{}`,
			map[string]any{"ok": false, "error": "Sign in to vote as yourself: run landfall login."}, ""},
		{"not signed in, the person's own link", personOwnLink, VoteFlags{Claim: 212, Position: "corroborate"}, 202, `{"ok":true,"state":"staged"}`,
			map[string]any{"ok": true, "claimSeq": float64(212), "position": "corroborate", "admitted": false}, "Bearer room-tok"},
		{"signed in to another org, own link", personOtherOrg, VoteFlags{Claim: 212, Position: "corroborate"}, 202, `{"ok":true,"state":"staged"}`,
			map[string]any{"ok": true, "claimSeq": float64(212), "position": "corroborate", "admitted": false}, "Bearer room-tok"},
		{"signed in to another org, a teammate's link", personOtherOrgLink, VoteFlags{Claim: 212, Position: "corroborate"}, 202, `{}`,
			map[string]any{"ok": false, "error": "Sign in to vote as yourself: run landfall login."}, ""},
		{"a guest votes as themself, and is told how it counts", personGuest, VoteFlags{Claim: 212, Position: "corroborate"}, 202, `{"ok":true,"state":"staged"}`,
			map[string]any{"ok": true, "claimSeq": float64(212), "position": "corroborate", "admitted": false, "note": guestVoteNote}, "Bearer room-tok"},
		{"the author's 409", personSignedIn, VoteFlags{Claim: 212, Position: "corroborate"}, 409,
			`{"statusCode":409,"message":"Posting this already counted as your vote. It needs a vote from someone other than you."}`,
			map[string]any{"ok": false, "error": "You authored this finding, so your position is already counted."}, "Bearer oauth-tok"},
		{"the person's own agent posted it", personSignedIn, VoteFlags{Claim: 212, Position: "contest"}, 409,
			`{"message":"Your agent posted this, so it already counts as your vote. It needs a vote from someone other than you."}`,
			map[string]any{"ok": false, "error": "Your agent posted this finding, so your position is already counted."}, "Bearer oauth-tok"},
		{"a closed incident", personSignedIn, VoteFlags{Claim: 212, Position: "corroborate"}, 409, `{"message":"engagement is closed; admission is frozen"}`,
			map[string]any{"ok": false, "error": "The incident is closed, so its findings take no more votes."}, "Bearer oauth-tok"},
		{"a lost race", personSignedIn, VoteFlags{Claim: 212, Position: "corroborate"}, 409, `{"message":"could not append claim event after retries"}`,
			map[string]any{"ok": false, "error": "The room was busy and the vote did not land. Try again."}, "Bearer oauth-tok"},
		{"no such finding", personSignedIn, VoteFlags{Claim: 999, Position: "corroborate"}, 404, `{"message":"no claim at seq 999"}`,
			map[string]any{"ok": false, "error": "There is no finding #999 in this room."}, "Bearer oauth-tok"},
		{"an expired sign-in", personSignedIn, VoteFlags{Claim: 212, Position: "corroborate"}, 401, `{}`,
			map[string]any{"ok": false, "error": "Your sign-in expired. Run landfall login."}, "Bearer oauth-tok"},
		{"an expired room session", personOwnLink, VoteFlags{Claim: 212, Position: "corroborate"}, 401, `{}`,
			map[string]any{"ok": false, "error": "The room session expired. Run landfall login, then try again."}, "Bearer room-tok"},
		{"refused for the account", personSignedIn, VoteFlags{Claim: 212, Position: "corroborate"}, 403, `{}`,
			map[string]any{"ok": false, "error": "Landfall refused this for your account. Check you are signed in to the organization this room belongs to."}, "Bearer oauth-tok"},
		{"rate limited", personSignedIn, VoteFlags{Claim: 212, Position: "corroborate"}, 429, `{}`,
			map[string]any{"ok": false, "error": "Too many requests just now. Wait a minute and try again."}, "Bearer oauth-tok"},
		{"a server fault", personSignedIn, VoteFlags{Claim: 212, Position: "corroborate"}, 500, `{}`,
			map[string]any{"ok": false, "error": "Landfall could not record that just now. Try again in a moment."}, "Bearer oauth-tok"},
		{"a position that is neither", personSignedIn, VoteFlags{Claim: 212, Position: "approve"}, 202, `{}`,
			map[string]any{"ok": false, "error": "Say corroborate or contest with --position."}, ""},
		{"no claim named", personSignedIn, VoteFlags{Claim: -1, Position: "corroborate"}, 202, `{}`,
			map[string]any{"ok": false, "error": "Name the finding with --claim and its number."}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newPersonFakeLandfall(t, c.status, c.body)
			got := personRoundTrip(t, RunVote(context.Background(), c.flags, personDeps(f, c.who)))
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
			if r.Method != http.MethodPost || r.Path != "/o/acme/incidents/inc-1/claims/"+jsonNum(c.flags.Claim)+"/position" || r.Auth != c.wantAuth {
				t.Fatalf("request = %+v", r)
			}
			if _, named := r.Body["agentInstanceId"]; named {
				t.Fatalf("a person's vote must not name an agent instance: %v", r.Body)
			}
			if r.Body["position"] != strings.ToLower(c.flags.Position) {
				t.Fatalf("body = %v", r.Body)
			}
			if c.flags.Reason != "" && r.Body["reason"] != "the deploy was at 15:48Z" {
				t.Fatalf("reason = %v", r.Body["reason"])
			}
		})
	}
}

func TestVoteRoomAndTransportErrors(t *testing.T) {
	deps := PersonDeps{
		Room: func(string) (client.Config, error) {
			return client.Config{}, errors.New("This folder is not in a room. Pass --room, or join one first.")
		},
		OAuthToken:   func(context.Context) string { return "oauth-tok" },
		OAuthSlug:    func() string { return "" },
		OAuthSubject: func() string { return "" },
	}
	got := RunVote(context.Background(), VoteFlags{Claim: 1, Position: "corroborate"}, deps)
	if got["ok"] != false || got["error"] != "This folder is not in a room. Pass --room, or join one first." {
		t.Fatalf("answer = %v", got)
	}

	f := newPersonFakeLandfall(t, 202, `{}`)
	f.srv.Close() // nobody answers
	got = RunVote(context.Background(), VoteFlags{Claim: 1, Position: "corroborate"}, personDeps(f, personSignedIn))
	if got["error"] != "Landfall did not answer. Check your connection and try again." {
		t.Fatalf("answer = %v", got)
	}
}

func TestVoteCommandPrintsOneLineAndExitsZero(t *testing.T) {
	var out strings.Builder
	prev := stdout
	stdout = &out
	defer func() { stdout = prev }()
	if err := printPersonAnswer(failAnswer("Sign in to vote as yourself: run landfall login.")); err != nil {
		t.Fatalf("printAnswer returned %v; the command must exit 0", err)
	}
	if out.String() != `{"error":"Sign in to vote as yourself: run landfall login.","ok":false}`+"\n" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestJWTSubjectAndPlainSentence(t *testing.T) {
	if got := jwtSubject(fakeJWT("u-alice")); got != "u-alice" {
		t.Fatalf("sub = %q", got)
	}
	for _, bad := range []string{"", "x.y", "a.!!!.c", "a." + base64.RawURLEncoding.EncodeToString([]byte("[]")) + ".c"} {
		if got := jwtSubject(bad); got != "" {
			t.Fatalf("jwtSubject(%q) = %q", bad, got)
		}
	}
	if got := plainSentence("that line is held by Dana — ask them"); got != "That line is held by Dana: ask them." {
		t.Fatalf("plain = %q", got)
	}
}

func TestPickPersonRoom(t *testing.T) {
	rooms := []daemon.RoomView{
		{RoomKey: "https://api/o/acme/inc-a", IncidentID: "aaaa-1111", DisplayID: "Acme 1", Readers: []*daemon.Reader{{WorkspaceKey: "other"}}},
		{RoomKey: "https://api/o/acme/inc-b", IncidentID: "bbbb-2222", DisplayID: "Acme 2", Readers: []*daemon.Reader{{WorkspaceKey: "ws"}}},
	}
	cases := map[string]string{
		"":                         "https://api/o/acme/inc-b",
		"https://api/o/acme/inc-a": "https://api/o/acme/inc-a",
		"aaaa-1111":                "https://api/o/acme/inc-a",
		"aaaa":                     "https://api/o/acme/inc-a",
		"Acme 2":                   "https://api/o/acme/inc-b",
		"zzz":                      "",
	}
	for sel, want := range cases {
		if got := pickPersonRoom(rooms, "ws", sel); got != want {
			t.Errorf("pickPersonRoom(%q) = %q, want %q", sel, got, want)
		}
	}
	if got := pickPersonRoom(rooms, "nowhere", ""); got != "" {
		t.Errorf("a folder in no room picked %q", got)
	}
}

// TestWatchRoomsCarryVotes: the stream's rooms carry the person's vote list,
// always as an array, beside the agent's count.
func TestWatchRoomsCarryVotes(t *testing.T) {
	rooms := watchRoomsOf([]daemon.RoomView{
		{RoomKey: "r1", VotesAwaited: 0, Votes: []narrate.Vote{{ClaimSeq: 212, Statement: "5xx at 15:45Z"}},
			Status: &narrate.RoomStatus{Lines: []narrate.Line{{ClaimID: "c-1", Label: "eu-west-1 5xx", LineKey: "eu-west-1-5xx", Owner: "Dana"}}, Focus: "web-edge"}},
		{RoomKey: "r2"},
	})
	body, err := json.Marshal(rooms)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{`"votes":[{"claimSeq":212,"statement":"5xx at 15:45Z","authorIsAgent":false,"positionsSoFar":0,"stale":false,"mine":false}]`,
		`"votes":[]`, `"lines":[{"claimId":"c-1","label":"eu-west-1 5xx","lineKey":"eu-west-1-5xx","owner":"Dana"}]`, `"focus":"web-edge"`, `"votesAwaited":0`} {
		if !strings.Contains(s, want) {
			t.Errorf("watch rooms %s\nlack %s", s, want)
		}
	}
}

// personRoundTrip passes an answer through JSON, as the front end reads it.
func personRoundTrip(t *testing.T, answer map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sameAnswer(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }
