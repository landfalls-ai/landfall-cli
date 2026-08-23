package tools

// Ported from `test/edge-bridge.test.mjs` (the tool-surface half) and
// `test/attention.test.mjs` (the piggyback half). The composition-order test
// is the one that matters most here: an earlier draft of this feature's own
// contract wrote the formula as `voteRequests + result + divergenceNudge +
// delta`, which contradicts the source.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/mcp"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// --- a recording fake -------------------------------------------------------

type fakeClient struct {
	mu    sync.Mutex
	calls []callRecord

	attention  *client.Attention
	divergence *client.Divergence
	frame      *client.ContextFrame
	frameErr   error
	delta      *client.FrameDelta
	deltaErr   error
	search     *client.SearchResult
	events     []client.Event

	uploadErr error
	flagErr   error
	stageErr  error
	posErr    error
}

type callRecord struct {
	name string
	args []any
}

func (c *fakeClient) record(name string, args ...any) {
	c.mu.Lock()
	c.calls = append(c.calls, callRecord{name: name, args: args})
	c.mu.Unlock()
}

func (c *fakeClient) recorded(name string) []callRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []callRecord
	for _, r := range c.calls {
		if r.name == name {
			out = append(out, r)
		}
	}
	return out
}

func (c *fakeClient) Join(context.Context) (*client.JoinResult, error) {
	c.record("join")
	return &client.JoinResult{AgentInstanceID: "a-1"}, nil
}
func (c *fakeClient) Leave(context.Context) error { c.record("leave"); return nil }
func (c *fakeClient) Heartbeat(_ context.Context, doing string) error {
	c.record("heartbeat", doing)
	return nil
}
func (c *fakeClient) Contribute(_ context.Context, kind string, body map[string]any) error {
	c.record("contribute", kind, body)
	return nil
}
func (c *fakeClient) UploadArtifact(_ context.Context, filename, contentType, data string) (*client.ArtifactResult, error) {
	c.record("upload", filename, contentType, data)
	if c.uploadErr != nil {
		return nil, c.uploadErr
	}
	return &client.ArtifactResult{Filename: filename, ContentType: contentType}, nil
}
func (c *fakeClient) FlagContext(_ context.Context, seq int64, reason, kind string) error {
	c.record("flag", seq, reason, kind)
	return c.flagErr
}
func (c *fakeClient) PositionClaim(_ context.Context, seq int64, position, reason string) error {
	c.record("position", seq, position, reason)
	return c.posErr
}
func (c *fakeClient) StageClaim(_ context.Context, body map[string]any) error {
	c.record("stage", body)
	return c.stageErr
}
func (c *fakeClient) GetBrief(context.Context) ([]client.Event, error) {
	c.record("brief")
	return c.events, nil
}
func (c *fakeClient) GetUpdates(context.Context, int64) ([]client.Event, error) {
	c.record("updates")
	return c.events, nil
}
func (c *fakeClient) GetContextFrame(context.Context) (*client.ContextFrame, error) {
	c.record("frame")
	if c.frameErr != nil {
		return nil, c.frameErr
	}
	if c.frame != nil {
		return c.frame, nil
	}
	return &client.ContextFrame{}, nil
}
func (c *fakeClient) GetContextDelta(_ context.Context, since int64) (*client.FrameDelta, error) {
	c.record("delta", since)
	if c.deltaErr != nil {
		return nil, c.deltaErr
	}
	if c.delta != nil {
		return c.delta, nil
	}
	return &client.FrameDelta{SinceVersion: since, ToVersion: &since}, nil
}
func (c *fakeClient) SearchContext(_ context.Context, q string) (*client.SearchResult, error) {
	c.record("search", q)
	if c.search != nil {
		return c.search, nil
	}
	return &client.SearchResult{}, nil
}
func (c *fakeClient) GetAttention(context.Context) (*client.Attention, error) {
	c.record("attention")
	if c.attention != nil {
		return c.attention, nil
	}
	return &client.Attention{}, nil
}
func (c *fakeClient) GetDivergence(context.Context) (*client.Divergence, error) {
	c.record("divergence")
	if c.divergence != nil {
		return c.divergence, nil
	}
	return &client.Divergence{}, nil
}
func (c *fakeClient) AgentInstanceID() string { return "a-1" }
func (c *fakeClient) Config() client.Config {
	return client.Config{Slug: "acme", IncidentID: "inc-1"}
}

func seq(n int64) *int64 { return &n }

func newSession(c session.EdgeClient) *session.Session {
	return session.New(session.Options{Client: c, AgentLabel: "Claude Code"})
}

func find(t *testing.T, list []mcp.Tool, name string) mcp.Tool {
	t.Helper()
	for _, tool := range list {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool named %q", name)
	return mcp.Tool{}
}

func callTool(t *testing.T, list []mcp.Tool, name string, args map[string]any) string {
	t.Helper()
	out, err := find(t, list, name).Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

// --- the surface ------------------------------------------------------------

func TestTheFifteenToolsAreExposed(t *testing.T) {
	list := Build(newSession(&fakeClient{}))
	want := []string{
		"join_war_room", "get_updates", "get_brief", "read_timeline", "search_context",
		"post_finding", "note", "post_widget", "upload_artifact", "propose_action",
		"flag_context", "corroborate_claim", "contest_claim", "stage_claim", "record_activity",
	}
	if len(list) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(list), len(want))
	}
	for i, name := range want {
		if list[i].Name != name {
			t.Errorf("tool %d = %q, want %q", i, list[i].Name, name)
		}
	}
}

func TestToolDescriptionsStateThatAVoteIsVisibilityOnlyAndNeedsAHumanQuorum(t *testing.T) {
	list := Build(newSession(&fakeClient{}))
	byName := map[string]string{}
	for _, tool := range list {
		byName[tool.Name] = tool.Description
	}
	checks := []struct{ tool, substring string }{
		{"flag_context", "VISIBILITY, never truth"},
		{"flag_context", "agents alone can never quarantine"},
		{"corroborate_claim", "requires a human in the chain"},
		{"corroborate_claim", "never counts the claim author corroborating themselves"},
		{"contest_claim", "decides nothing"},
		{"stage_claim", "NOT in the room feed"},
	}
	for _, c := range checks {
		if !strings.Contains(byName[c.tool], c.substring) {
			t.Errorf("%s description is missing %q", c.tool, c.substring)
		}
	}
	if !strings.Contains(EdgeAgentInstructions, "POSITION, never a decision") {
		t.Error("the standing instructions must carry the same rule")
	}
	for _, want := range []string{"get_brief", "propose-only", "never mid-turn, unprompted", "realtime"} {
		if !strings.Contains(EdgeAgentInstructions, want) {
			t.Errorf("EdgeAgentInstructions is missing %q", want)
		}
	}
}

func TestBeforeJoiningTheInvestigationToolsFailClosedWithAHelpfulError(t *testing.T) {
	s := session.New(session.Options{}) // no client
	list := Build(s)

	res := mcp.HandleMessage(context.Background(),
		&mcp.Request{JSONRPC: "2.0", ID: []byte("1"), Method: "tools/call",
			Params: []byte(`{"name":"get_brief"}`)},
		mcp.Options{Tools: list})

	result := res.Result.(mcp.CallToolResult)
	if !result.IsError {
		t.Fatal("an unjoined tool call must surface as isError:true, not a JSON-RPC error")
	}
	if !strings.Contains(result.Content[0].Text, "join_war_room") {
		t.Errorf("text = %q", result.Content[0].Text)
	}
}

func TestRequireClientRunsBeforeTheHeartbeat(t *testing.T) {
	c := &fakeClient{}
	s := session.New(session.Options{}) // no client yet
	list := Build(s)
	if _, err := find(t, list, "post_finding").Handler(context.Background(), map[string]any{"text": "x"}); err == nil {
		t.Fatal("expected the fail-closed error")
	}
	if len(c.recorded("heartbeat")) != 0 {
		t.Error("an unjoined session must narrate nothing anywhere")
	}
}

// --- narration --------------------------------------------------------------

func TestAPostFindingCallHeartbeatsAndPostsAFindingContribution(t *testing.T) {
	c := &fakeClient{}
	list := Build(newSession(c))
	out := callTool(t, list, "post_finding", map[string]any{"text": "origin regressed"})

	if out != "Finding posted to the war room." {
		t.Errorf("result = %q", out)
	}
	hb := c.recorded("heartbeat")
	if len(hb) != 1 || !strings.Contains(hb[0].args[0].(string), "posting a finding") {
		t.Errorf("heartbeat = %v", hb)
	}
	contrib := c.recorded("contribute")
	if len(contrib) != 1 || contrib[0].args[0] != "finding" {
		t.Fatalf("contribute = %v", contrib)
	}
	if body := contrib[0].args[1].(map[string]any); body["text"] != "origin regressed" {
		t.Errorf("body = %v", body)
	}
}

func TestVettingToolsNeverDoublePostAGenericContribution(t *testing.T) {
	c := &fakeClient{}
	list := Build(newSession(c))
	callTool(t, list, "flag_context", map[string]any{"targetSeq": float64(3), "reason": "r"})
	callTool(t, list, "corroborate_claim", map[string]any{"claimSeq": float64(48)})
	callTool(t, list, "contest_claim", map[string]any{"claimSeq": float64(49), "reason": "bisect says otherwise"})
	callTool(t, list, "stage_claim", map[string]any{"claimClass": "observation", "statement": "p99 rose at 14:02"})

	if got := c.recorded("contribute"); len(got) != 0 {
		t.Fatalf("the vetting endpoints append their OWN durable events; got %v", got)
	}
	// …and the same rule, asserted directly against the real decision this
	// wrapper delegates to, so a change there cannot quietly re-introduce the
	// double-post here.
	for _, name := range []string{"flag_context", "corroborate_claim", "contest_claim", "stage_claim", "upload_artifact"} {
		if narrate.ContributionFor(name, narrate.Args{}) != nil {
			t.Errorf("narrate.ContributionFor(%s) must be nil", name)
		}
	}
}

func TestCorroborateAndContestAreOneEndpointWithOppositeStances(t *testing.T) {
	c := &fakeClient{}
	list := Build(newSession(c))
	yes := callTool(t, list, "corroborate_claim", map[string]any{"claimSeq": float64(48), "reason": "reproduced"})
	no := callTool(t, list, "contest_claim", map[string]any{"claimSeq": float64(48), "reason": "not on my build"})

	positions := c.recorded("position")
	if len(positions) != 2 {
		t.Fatalf("positions = %v", positions)
	}
	if positions[0].args[1] != "corroborate" || positions[1].args[1] != "contest" {
		t.Errorf("stances = %v / %v", positions[0].args, positions[1].args)
	}
	for _, out := range []string{yes, no} {
		if !strings.Contains(out, "needs a human in the chain") {
			t.Errorf("result = %q — it must tell the agent its position is not the decision", out)
		}
	}
}

// The one failure this surface must never have.
func TestAnAbsentSeqIsRefusedLocallyAndNeverReachesTheServer(t *testing.T) {
	c := &fakeClient{}
	list := Build(newSession(c))

	if out := callTool(t, list, "flag_context", map[string]any{"targetSeq": "twelve", "reason": "r"}); !strings.Contains(out, "Nothing flagged") {
		t.Errorf("a non-numeric seq → %q", out)
	}
	if out := callTool(t, list, "stage_claim", map[string]any{"claimClass": "causal", "statement": "  "}); !strings.Contains(out, "Nothing staged") {
		t.Errorf("a blank statement → %q", out)
	}
	// `Number(null)` and `Number('')` are both 0, and 0 is a REAL seq — so a
	// call that simply omitted the argument must never coerce into a position
	// on the incident's first event.
	for _, missing := range []any{nil, "", map[string]any{}, true} {
		if out := callTool(t, list, "corroborate_claim", map[string]any{"claimSeq": missing}); !strings.Contains(out, "No position recorded") {
			t.Errorf("claimSeq=%#v → %q", missing, out)
		}
		if out := callTool(t, list, "flag_context", map[string]any{"targetSeq": missing, "reason": "r"}); !strings.Contains(out, "Nothing flagged") {
			t.Errorf("targetSeq=%#v → %q", missing, out)
		}
	}
	// Also the key being absent entirely.
	if out := callTool(t, list, "corroborate_claim", map[string]any{}); !strings.Contains(out, "No position recorded") {
		t.Errorf("an omitted claimSeq → %q", out)
	}
	if len(c.recorded("position")) != 0 || len(c.recorded("flag")) != 0 {
		t.Fatal("a malformed seq must never reach the server")
	}

	// ...while seq 0 itself, explicitly given, is a legitimate target.
	if out := callTool(t, list, "corroborate_claim", map[string]any{"claimSeq": float64(0)}); !strings.Contains(out, "claim #0") {
		t.Errorf("seq 0 → %q", out)
	}
	positions := c.recorded("position")
	if len(positions) != 1 || positions[0].args[0].(int64) != 0 {
		t.Fatalf("positions = %v", positions)
	}
}

func TestAServerRefusalIsRelayedWithItsReasonAndClaimsNothingWasRecorded(t *testing.T) {
	c := &fakeClient{flagErr: errors.New("/vetting/flag → HTTP 400: only chat messages and findings can be flagged")}
	list := Build(newSession(c))
	out := callTool(t, list, "flag_context", map[string]any{"targetSeq": float64(5), "reason": "r"})
	if !strings.Contains(out, "only chat messages and findings can be flagged") || !strings.Contains(out, "Nothing flagged") {
		t.Errorf("result = %q", out)
	}
}

// --- upload_artifact --------------------------------------------------------

func TestUploadArtifactSharesInlineContentWithoutADoubleContribution(t *testing.T) {
	c := &fakeClient{}
	list := Build(newSession(c))
	out := callTool(t, list, "upload_artifact", map[string]any{"content": "<html>hi</html>", "filename": "report.html"})

	if !strings.Contains(out, `Shared "report.html" (text/html`) {
		t.Errorf("result = %q", out)
	}
	up := c.recorded("upload")
	if len(up) != 1 || up[0].args[1] != "text/html" {
		t.Fatalf("upload = %v", up)
	}
	if len(c.recorded("heartbeat")) != 1 {
		t.Error("presence is still narrated")
	}
	if len(c.recorded("contribute")) != 0 {
		t.Error("the /artifacts endpoint appends artifact.shared itself — no double-post")
	}
}

func TestUploadArtifactPreCheckRefusesDisallowedEmptyAndOversized(t *testing.T) {
	c := &fakeClient{}
	list := Build(newSession(c))

	bad := callTool(t, list, "upload_artifact", map[string]any{
		"content": "x", "filename": "evil.exe", "contentType": "application/x-msdownload"})
	if !strings.Contains(bad, "not allowed") || !strings.Contains(bad, "Nothing shared") {
		t.Errorf("disallowed type → %q", bad)
	}
	empty := callTool(t, list, "upload_artifact", map[string]any{"content": "", "filename": "a.txt"})
	if !strings.Contains(empty, "empty (0 bytes)") {
		t.Errorf("empty → %q", empty)
	}
	big := callTool(t, list, "upload_artifact", map[string]any{
		"content": strings.Repeat("x", artifactMaxBytes+1), "filename": "big.txt"})
	if !strings.Contains(big, "over the 5 MiB limit") {
		t.Errorf("oversized → %q", big)
	}
	neither := callTool(t, list, "upload_artifact", map[string]any{})
	if !strings.Contains(neither, "Provide either `path`") {
		t.Errorf("neither → %q", neither)
	}
	if len(c.recorded("upload")) != 0 {
		t.Fatal("none of these may reach the server")
	}
}

func TestUploadArtifactReadsAPathAndInfersTheTypeFromTheExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	if err := os.WriteFile(path, []byte("# hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &fakeClient{}
	list := Build(newSession(c))
	out := callTool(t, list, "upload_artifact", map[string]any{"path": path})

	if !strings.Contains(out, `Shared "report.md" (text/markdown`) {
		t.Errorf("result = %q", out)
	}
	missing := callTool(t, list, "upload_artifact", map[string]any{"path": filepath.Join(dir, "nope.txt")})
	if !strings.Contains(missing, "Could not read file") || !strings.Contains(missing, "Nothing shared") {
		t.Errorf("missing file → %q", missing)
	}
}

// --- join_war_room ----------------------------------------------------------

func joinableSession(c *fakeClient, incidentID string) *session.Session {
	return session.New(session.Options{
		AgentLabel: "Claude Code",
		Redeem: func(context.Context, string) (client.Config, error) {
			return client.Config{BaseURL: "http://api.test", Slug: "acme", IncidentID: incidentID, Token: "edge-tok"}, nil
		},
		ClientFactory: func(client.Config) session.EdgeClient { return c },
	})
}

func TestJoinWarRoomIncludesTheBriefInlineAndUnlocksTheOtherTools(t *testing.T) {
	c := &fakeClient{frame: &client.ContextFrame{
		AsOfSeq:  seq(3),
		Incident: client.Incident{Title: "CloudFront 5xx spike", Severity: "sev2", Status: "open"},
		Brief: client.Brief{Established: []client.BriefItem{
			{Seq: 1, Statement: "Origin misconfigured", By: "Alex"}}},
	}}
	s := joinableSession(c, "inc-2")
	list := Build(s)

	out := callTool(t, list, "join_war_room", map[string]any{"shareUrl": "http://api.test/o/acme/incidents/inc-2/agent?ticket=t"})

	for _, want := range []string{
		"Joined war room for incident inc-2", `as "Claude Code"`,
		"CloudFront 5xx spike", "Established:", "Origin misconfigured",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("result is missing %q:\n%s", want, out)
		}
	}
	if len(c.recorded("frame")) != 1 {
		t.Errorf("the inline brief must cost exactly one frame fetch, got %d", len(c.recorded("frame")))
	}
	if s.Cursor() != 3 {
		t.Errorf("cursor = %d, want the frame's own cursor (3)", s.Cursor())
	}
	if len(c.recorded("heartbeat")) != 0 {
		t.Error("join_war_room is session control, not a room-write — it is NOT wrapped")
	}
}

func TestJoinWarRoomStillSucceedsIfTheInlineBriefFetchFails(t *testing.T) {
	c := &fakeClient{frameErr: errors.New("network blip")}
	list := Build(joinableSession(c, "inc-3"))
	out := callTool(t, list, "join_war_room", map[string]any{"shareUrl": "http://api.test/o/acme/incidents/inc-3/agent?ticket=t"})

	if !strings.Contains(out, "Joined war room for incident inc-3") {
		t.Errorf("the join itself must not fail: %q", out)
	}
	if !strings.Contains(out, "call get_brief") {
		t.Errorf("it must degrade to a parenthetical: %q", out)
	}
}

// --- reads ------------------------------------------------------------------

func TestGetUpdatesRendersTheServerDeltaAndAdvancesTheCursor(t *testing.T) {
	to := int64(2)
	c := &fakeClient{delta: &client.FrameDelta{ToVersion: &to, Items: []client.DeltaItem{
		{Seq: 1, Type: "edge.finding", By: "Dana · Codex", Class: "substantive", Summary: "origin pool unhealthy"},
		{Seq: 2, Type: "edge.hypothesis", By: "Dana", Class: "addressed", Summary: "bad rollout"},
	}}}
	s := newSession(c)
	list := Build(s)

	out := callTool(t, list, "get_updates", map[string]any{})
	if !strings.Contains(out, "⚠ 2 update(s) from other investigators") {
		t.Errorf("result = %q", out)
	}
	if !strings.Contains(out, "#1 edge.finding [Dana · Codex] — origin pool unhealthy") {
		t.Errorf("attribution must use the resolved display name: %q", out)
	}
	if !strings.Contains(out, "➤ #2") {
		t.Errorf("an addressed item must be marked differently: %q", out)
	}
	if s.Cursor() != 2 {
		t.Errorf("cursor = %d, want 2", s.Cursor())
	}

	c.delta = &client.FrameDelta{ToVersion: &to}
	out = callTool(t, list, "get_updates", map[string]any{})
	if !strings.Contains(out, "No new shared context since seq 2") {
		t.Errorf("result = %q", out)
	}
}

func TestGetBriefAdvancesTheCursorFromTheFramesOwnCursor(t *testing.T) {
	c := &fakeClient{frame: &client.ContextFrame{AsOfSeq: seq(1)}}
	s := newSession(c)
	out := callTool(t, Build(s), "get_brief", map[string]any{})
	if !strings.Contains(out, "No findings or open items yet") {
		t.Errorf("result = %q", out)
	}
	if s.Cursor() != 1 {
		t.Errorf("cursor = %d, want 1", s.Cursor())
	}
}

func TestReadTimelineReturnsTheRawEventsVerbatimAndAdvancesTheCursor(t *testing.T) {
	var e client.Event
	if err := e.UnmarshalJSON([]byte(`{"seq":4,"type":"edge.finding","payload":{"text":"x"},"extraServerField":true}`)); err != nil {
		t.Fatal(err)
	}
	c := &fakeClient{events: []client.Event{e}}
	s := newSession(c)
	out := callTool(t, Build(s), "read_timeline", map[string]any{})

	if !strings.Contains(out, "extraServerField") {
		t.Errorf("a field this CLI does not name must survive the round trip: %s", out)
	}
	if s.Cursor() != 4 {
		t.Errorf("cursor = %d, want 4", s.Cursor())
	}
}

func TestSearchContextRendersRealHitsAndContributesAQuery(t *testing.T) {
	c := &fakeClient{search: &client.SearchResult{Hits: []client.SearchHit{
		{Seq: 3, Type: "edge.finding", By: "Dana", Snippet: "cloudfront 5xx"}}}}
	list := Build(newSession(c))
	out := callTool(t, list, "search_context", map[string]any{"query": "cloudfront"})

	if !strings.Contains(out, `1 matching event(s) for "cloudfront":`) {
		t.Errorf("result = %q", out)
	}
	if !strings.Contains(out, "#3 edge.finding [Dana] — cloudfront 5xx") {
		t.Errorf("result = %q", out)
	}
	contrib := c.recorded("contribute")
	if len(contrib) != 1 || contrib[0].args[0] != "query" {
		t.Fatalf("search_context must contribute a query-kind contribution, got %v", contrib)
	}
}

// --- the wrapper's composition order ----------------------------------------

func TestAResultWithNothingOwedIsLeftExactlyAsTheToolWroteIt(t *testing.T) {
	list := Build(newSession(&fakeClient{}))
	if out := callTool(t, list, "post_finding", map[string]any{"text": "mine"}); out != "Finding posted to the war room." {
		t.Errorf("result = %q", out)
	}
}

func TestTheComposedResultIsAskedThenNudgeThenResultThenDelta(t *testing.T) {
	// An "answer this" ask outranks a soft nudge, which outranks the result,
	// which outranks the digest. Getting this backwards buries the one thing
	// on the result that is addressed TO the agent.
	to := int64(4)
	c := &fakeClient{
		attention: &client.Attention{VotesAwaited: []client.VoteAwaited{
			{ClaimSeq: seq(48), Statement: "the origin rollback at 14:02 caused the 5xx spike", AuthoredBy: "Priya"}}},
		delta: &client.FrameDelta{ToVersion: &to, Items: []client.DeltaItem{
			{Seq: 4, Type: "edge.finding", By: "Dana", Summary: "origin pool unhealthy"}}},
	}
	s := newSession(c)
	// The nudge reads whatever the background refresh last landed — it never
	// forces one of its own — so the snapshot is pre-set here, exactly as an
	// earlier arrival would have left it.
	s.SetDivergence(&client.Divergence{
		Diverging: true, EstablishedSubject: "cli-handoff/redeem", ObservedSubject: "cloudfront/5xxerrorrate"})
	s.EnqueueEvent(context.Background(), client.Event{Seq: seq(4), Type: "edge.finding"})

	out := callTool(t, Build(s), "post_finding", map[string]any{"text": "mine"})

	iAsked := strings.Index(out, "⚠ vote requested: claim #48")
	iNudge := strings.Index(out, "↷ the room's established root cause")
	iResult := strings.Index(out, "Finding posted to the war room.")
	iDelta := strings.Index(out, "⚠ 1 update(s) from other investigators")
	for name, idx := range map[string]int{"asked": iAsked, "nudge": iNudge, "result": iResult, "delta": iDelta} {
		if idx < 0 {
			t.Fatalf("the %s block is missing from:\n%s", name, out)
		}
	}
	if iAsked >= iNudge || iNudge >= iResult || iResult >= iDelta {
		t.Fatalf("order was asked=%d nudge=%d result=%d delta=%d, want strictly ascending:\n%s",
			iAsked, iNudge, iResult, iDelta, out)
	}
	// And the separators are blank lines, as the source composes them.
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("blocks must be joined by exactly one blank line:\n%q", out)
	}
}

func TestFlushVoteRequestsForcesARefreshWhileTheDivergenceNudgeDoesNot(t *testing.T) {
	c := &fakeClient{
		attention:  &client.Attention{},
		divergence: &client.Divergence{Diverging: true, EstablishedSubject: "a", ObservedSubject: "b"},
	}
	s := newSession(c)
	list := Build(s)

	// A non-query tool: attention IS read (it starts dirty), divergence is NOT.
	callTool(t, list, "get_brief", map[string]any{})
	if got := len(c.recorded("attention")); got != 1 {
		t.Errorf("attention reads = %d, want 1 — step 6 force-refreshes", got)
	}
	if got := len(c.recorded("divergence")); got != 0 {
		t.Fatalf("divergence reads = %d, want 0 — step 7 must never add latency to the fast path", got)
	}
	if s.Divergence() != nil {
		t.Error("nothing should have landed in the divergence snapshot yet")
	}

	// A query-kind contribution IS one of divergence's two triggers.
	callTool(t, list, "search_context", map[string]any{"query": "cloudfront"})
	s.WaitForReads()
	if got := len(c.recorded("divergence")); got != 1 {
		t.Errorf("divergence reads after a query = %d, want 1", got)
	}
}

func TestANudgeIsAnnouncedExactlyOnceForAGivenPair(t *testing.T) {
	c := &fakeClient{divergence: &client.Divergence{
		Diverging: true, EstablishedSubject: "cli-handoff/redeem", ObservedSubject: "cloudfront/5xxerrorrate"}}
	s := newSession(c)
	list := Build(s)

	var results []string
	results = append(results, callTool(t, list, "search_context", map[string]any{"query": "cloudfront"}))
	s.WaitForReads()
	for i := 0; i < 3; i++ {
		results = append(results, callTool(t, list, "get_brief", map[string]any{}))
	}

	withNudge := 0
	for _, r := range results {
		if strings.Contains(r, "cli-handoff/redeem") {
			withNudge++
		}
	}
	if withNudge != 1 {
		t.Fatalf("the nudge appeared %d times across the sequence, want exactly 1", withNudge)
	}
}

func TestAVoteRequestIsAnnouncedOnceAndAgainOnlyIfItLaterGoesStale(t *testing.T) {
	att := &client.Attention{VotesAwaited: []client.VoteAwaited{{ClaimSeq: seq(48), Statement: "x"}}}
	c := &fakeClient{attention: att}
	s := newSession(c)
	list := Build(s)

	first := callTool(t, list, "get_brief", map[string]any{})
	if !strings.Contains(first, "vote requested: claim #48") {
		t.Fatalf("first result = %q", first)
	}
	// Same claim, same state: silence. Training an agent to skim past the
	// marker is the one way this channel can fail permanently.
	s.MarkAttentionDirty()
	if second := callTool(t, list, "get_brief", map[string]any{}); strings.Contains(second, "vote requested") {
		t.Errorf("the same claim was re-announced: %q", second)
	}
	// Now it is about to lapse — genuinely new, and the last moment to act.
	c.attention = &client.Attention{VotesAwaited: []client.VoteAwaited{{ClaimSeq: seq(48), Statement: "x", Stale: true}}}
	s.MarkAttentionDirty()
	third := callTool(t, list, "get_brief", map[string]any{})
	if !strings.Contains(third, "PAST its freshness window") {
		t.Errorf("a lapsing claim must be announced again: %q", third)
	}
}

func TestAFailedAttentionReadNeverBreaksTheToolCall(t *testing.T) {
	c := &failingAttentionClient{fakeClient: &fakeClient{}}
	list := Build(newSession(c))
	out := callTool(t, list, "get_brief", map[string]any{})
	if !strings.Contains(out, "No findings or open items yet") {
		t.Errorf("result = %q", out)
	}
	if strings.Contains(out, "vote requested") {
		t.Errorf("result = %q", out)
	}
}

type failingAttentionClient struct{ *fakeClient }

func (c *failingAttentionClient) GetAttention(context.Context) (*client.Attention, error) {
	c.record("attention")
	return nil, errors.New("network down")
}

// --- seqOf ------------------------------------------------------------------

func TestSeqOfDistinguishesAbsentFromZero(t *testing.T) {
	cases := []struct {
		in   any
		want int64
		ok   bool
	}{
		{float64(0), 0, true},
		{float64(12), 12, true},
		{"12", 12, true},
		{" 12 ", 12, true},
		{nil, 0, false},
		{"", 0, false},
		{"  ", 0, false},
		{"twelve", 0, false},
		{map[string]any{}, 0, false},
		{[]any{}, 0, false},
		{true, 0, false},
		{float64(-1), 0, false},
		{float64(1.5), 0, false},
	}
	for _, c := range cases {
		got, ok := seqOf(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("seqOf(%#v) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
