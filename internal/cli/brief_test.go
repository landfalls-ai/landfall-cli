package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

const (
	briefFrameURL  = "/o/acme/incidents/inc-1/edge/context/frame"
	briefClaimsURL = "/o/acme/incidents/inc-1/claims"
)

// briefFrame is a room with something in every block.
func briefFrame() map[string]any {
	return map[string]any{
		"asOfSeq": 214, "version": 9, "freshnessMs": 3999.6,
		"incident": map[string]any{"displayId": "Landfall 168", "title": "cloudfront-5xx-high", "severity": "SEV1", "status": "investigating"},
		"brief": map[string]any{
			"established": []any{
				map[string]any{"seq": 205, "statement": "Origin pool us-east-1 lost two hosts.", "by": "carol",
					"admission": map[string]any{"trigger": "bar-met", "corroborators": []any{
						map[string]any{"by": "bob", "kind": "member"},
						map[string]any{"by": "alice's agent", "kind": "agent"},
					}}},
				map[string]any{"seq": 190, "statement": "5xx started at 15:02.", "by": "Beacon"},
			},
			"workingTheory": []any{map[string]any{"seq": 210, "statement": "A deploy drained the pool.", "by": "bob"}},
			"open": []any{
				map[string]any{"seq": 211, "statement": "Health checks flap every 30s.", "by": "alice"},
				map[string]any{"seq": 212, "statement": "TLS renewals failed.", "by": "dave"},
				map[string]any{"seq": 213, "statement": "Not a claim the projection knows.", "by": "dave"},
			},
			"disproved": []any{},
		},
		"participants": []any{
			map[string]any{"displayName": "carol", "edgeAgentLabel": "", "active": true, "kind": "member", "humanActorId": "u-carol", "agentInstanceId": "web:u-carol:t"},
			map[string]any{"displayName": "bob", "edgeAgentLabel": "bob-claude-code", "active": false, "kind": "member", "humanActorId": "u-bob", "agentInstanceId": "i-1"},
			map[string]any{"displayName": "eve", "edgeAgentLabel": "eve-codex", "kind": "guest", "humanActorId": "u-eve", "agentInstanceId": "i-2"},
		},
		"attachments": []any{
			map[string]any{"seq": 100, "kind": "component", "ref": map[string]any{"elementId": "web-edge"}, "label": "web-edge", "by": map[string]any{"humanActorId": "u-carol", "displayName": "carol"}},
		},
		"focus":        map[string]any{"focus": "the origin pool in us-east-1", "by": map[string]any{"humanActorId": "u-carol", "displayName": "carol"}},
		"instructions": map[string]any{"version": 3, "body": "Page the CDN owner before any failover.", "componentSections": map[string]any{}},
		"listening":    false,
	}
}

func briefClaims() map[string]any {
	return map[string]any{"gateEnabled": true, "admittedSeqs": []any{205}, "claims": []any{
		map[string]any{"seq": 211, "class": "observation", "statement": "Health checks flap every 30s.", "state": "staged",
			"author":    map[string]any{"humanActorId": "u-alice"},
			"positions": []any{map[string]any{"position": "contest", "actor": map[string]any{"humanActorId": "u-bob"}}}},
		map[string]any{"seq": 212, "class": "observation", "statement": "TLS renewals failed.", "state": "staged",
			"author": map[string]any{"humanActorId": "u-dave"}},
		map[string]any{"seq": 210, "class": "causal", "statement": "A deploy drained the pool.", "state": "staged",
			"author": map[string]any{"humanActorId": "u-bob"}},
	}}
}

func TestBriefAnswersTheFrameAsThePerson(t *testing.T) {
	f := newFakeLandfall(t)
	f.json("GET "+briefFrameURL, roomToken, briefFrame())
	f.json("GET "+briefClaimsURL, roomToken, briefClaims())
	ans := roundTrip(t, RunBrief(context.Background(), "rk1", f.deps(false)))
	if ans["ok"] != true {
		t.Fatalf("answer = %v", ans)
	}

	// No agentInstanceId, so no context pull and no cursor move: the reads
	// are exactly these two, on the room's session, with no query at all.
	calls := f.allCalls()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	for _, c := range calls {
		if c.Method != http.MethodGet || c.Query != "" || c.Token != roomToken {
			t.Fatalf("a person's read must be a bare GET on the room's session: %+v", c)
		}
	}
	if calls[0].Path != briefFrameURL || calls[1].Path != briefClaimsURL {
		t.Fatalf("calls = %+v", calls)
	}

	// text is RenderFrame's, byte for byte what get_brief returns.
	var frame client.ContextFrame
	briefDecode(t, briefFrame(), &frame)
	if ans["text"] != narrate.RenderFrame(&frame) {
		t.Fatalf("text = %q", ans["text"])
	}

	if ans["asOfSeq"] != float64(214) || ans["freshnessMs"] != float64(4000) || ans["instructionsVersion"] != float64(3) || ans["listening"] != false {
		t.Fatalf("head = asOfSeq %v freshnessMs %v instructionsVersion %v listening %v", ans["asOfSeq"], ans["freshnessMs"], ans["instructionsVersion"], ans["listening"])
	}
	counts := ans["counts"].(map[string]any)
	if counts["established"] != float64(2) || counts["open"] != float64(4) || counts["participants"] != float64(3) || counts["scope"] != float64(1) || counts["focus"] != true {
		t.Fatalf("counts = %v", counts)
	}

	est := ans["established"].([]any)
	first := est[0].(map[string]any)
	if first["seq"] != float64(205) || first["statement"] != "Origin pool us-east-1 lost two hosts." || first["by"] != "carol" ||
		first["admission"] != "admitted: corroborated by bob (member); alice's agent (agent)" {
		t.Fatalf("established[0] = %v", first)
	}
	if _, has := est[1].(map[string]any)["admission"]; has {
		t.Fatalf("an item with no admission carries none: %v", est[1])
	}

	// The theory first (no state), then the open items with their state.
	open := ans["open"].([]any)
	want := []struct {
		seq    float64
		state  string
		theory bool
	}{{210, "", true}, {211, "contested", false}, {212, "staged", false}, {213, "", false}}
	for i, w := range want {
		row := open[i].(map[string]any)
		state, _ := row["state"].(string)
		theory, _ := row["theory"].(bool)
		if row["seq"] != w.seq || state != w.state || theory != w.theory {
			t.Fatalf("open[%d] = %v, want seq %v state %q theory %v", i, row, w.seq, w.state, w.theory)
		}
	}

	parts := ans["participants"].([]any)
	carol, bob, eve := parts[0].(map[string]any), parts[1].(map[string]any), parts[2].(map[string]any)
	if carol["humanActorId"] != "u-carol" || carol["displayName"] != "carol" || carol["edgeAgentLabel"] != "" || carol["active"] != true {
		t.Fatalf("carol = %v", carol)
	}
	if bob["edgeAgentLabel"] != "bob-claude-code" || bob["active"] != false {
		t.Fatalf("bob = %v", bob)
	}
	if v, has := eve["active"]; !has || v != nil {
		t.Fatalf("unknown presence is null, never made up: %v", eve)
	}

	scope := ans["scope"].([]any)[0].(map[string]any)
	if scope["kind"] != "component" || scope["label"] != "web-edge" || scope["by"] != "carol" {
		t.Fatalf("scope = %v", scope)
	}
	focus := ans["focus"].(map[string]any)
	if focus["text"] != "the origin pool in us-east-1" || focus["by"] != "carol" {
		t.Fatalf("focus = %v", focus)
	}
	inc := ans["incident"].(map[string]any)
	if inc["displayId"] != "Landfall 168" || inc["title"] != "cloudfront-5xx-high" {
		t.Fatalf("incident = %v", inc)
	}
}

// TestBriefOfAFreshRoom: nothing established or open, no focus, no
// instructions: empty lists and nulls, and no claims read at all.
func TestBriefOfAFreshRoom(t *testing.T) {
	f := newFakeLandfall(t)
	f.json("GET "+briefFrameURL, roomToken, map[string]any{
		"asOfSeq": 3, "freshnessMs": 0, "incident": map[string]any{"title": "checkout-latency"},
		"brief":        map[string]any{"established": []any{}, "workingTheory": []any{}, "open": []any{}, "disproved": []any{}},
		"participants": []any{map[string]any{"displayName": "dave", "humanActorId": "u-dave", "active": true}},
	})
	ans := roundTrip(t, RunBrief(context.Background(), "", f.deps(false)))
	if ans["ok"] != true || ans["focus"] != nil || ans["instructionsVersion"] != nil {
		t.Fatalf("answer = %v", ans)
	}
	if _, has := ans["listening"]; has {
		t.Fatalf("an absent listening switch is not news: %v", ans)
	}
	if len(ans["established"].([]any)) != 0 || len(ans["open"].([]any)) != 0 || len(ans["scope"].([]any)) != 0 {
		t.Fatalf("lists = %v", ans)
	}
	counts := ans["counts"].(map[string]any)
	if counts["established"] != float64(0) || counts["open"] != float64(0) || counts["participants"] != float64(1) || counts["focus"] != false {
		t.Fatalf("counts = %v", counts)
	}
	if len(f.callsTo(briefClaimsURL)) != 0 {
		t.Fatalf("claims were read with nothing open")
	}
}

// TestBriefWithoutClaimsStillAnswers: the claims read is best effort.
func TestBriefWithoutClaimsStillAnswers(t *testing.T) {
	f := newFakeLandfall(t)
	f.json("GET "+briefFrameURL, roomToken, briefFrame())
	f.status("GET "+briefClaimsURL, http.StatusForbidden)
	ans := roundTrip(t, RunBrief(context.Background(), "", f.deps(false)))
	if ans["ok"] != true {
		t.Fatalf("answer = %v", ans)
	}
	for _, r := range ans["open"].([]any) {
		if _, has := r.(map[string]any)["state"]; has {
			t.Fatalf("a state with no claims read: %v", r)
		}
	}
}

func TestBriefErrors(t *testing.T) {
	ctx := context.Background()
	wantFailure(t, RunBrief(ctx, "", noRoomDeps()), msgNoRoom)

	f := newFakeLandfall(t)
	wantFailure(t, RunBrief(ctx, "rk9", f.deps(false)), "No room rk9 is open on this machine. Join it first.")

	f.status("GET "+briefFrameURL, http.StatusUnauthorized)
	wantFailure(t, RunBrief(ctx, "", f.deps(false)), msgRoomExpired)

	f.status("GET "+briefFrameURL, http.StatusForbidden)
	wantFailure(t, RunBrief(ctx, "", f.deps(false)), "Landfall refused to show the shared context to this room's session.")

	f.handle("GET "+briefFrameURL, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[1,2]`)) })
	wantFailure(t, RunBrief(ctx, "", f.deps(false)), "Landfall sent a shared context this CLI could not read.")
}

// briefDecode passes v through JSON into out.
func briefDecode(t *testing.T, v any, out any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
}

// TestBriefCommandAnswersOneJSONLine runs `landfall brief --room rk1 --host
// claude-code` through the real argument handling with no room daemon: one
// JSON failure line, exit 0.
func TestBriefCommandAnswersOneJSONLine(t *testing.T) {
	rt, err := os.MkdirTemp("/tmp", "lfb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LANDFALL_DAEMON", "0")

	var out bytes.Buffer
	prev := stdout
	stdout = &out
	defer func() { stdout = prev }()
	code := run(&UI{Out: &out, Err: io.Discard}, []string{"brief", "--room", "rk1", "--host", "claude-code", "--surprise"}, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one line, got %q", out.String())
	}
	var ans map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ans); err != nil {
		t.Fatalf("not JSON: %q", lines[0])
	}
	if ans["ok"] != false {
		t.Fatalf("answer = %v", ans)
	}
	assertPlainSentence(t, ans["error"].(string))
}
