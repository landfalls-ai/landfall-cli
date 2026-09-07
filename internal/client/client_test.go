package client

// Ported from the client-contract half of `test/edge-bridge.test.mjs` and the
// `getAttention`/`getDivergence` cases in `test/attention.test.mjs`. No
// network: the transport is a recording fake, exactly as the Node original
// injected `fetch`.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

var cfg = Config{
	BaseURL: "http://api.test", Slug: "acme", IncidentID: "inc-1",
	Token: "tok", AgentLabel: "Claude Code",
}

type recordedCall struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   map[string]any
}

// fakeDoer answers by path, recording every request.
type fakeDoer struct {
	calls  []recordedCall
	routes map[string]fakeResponse
}

type fakeResponse struct {
	status int
	body   string
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	call := recordedCall{
		Method: req.Method,
		Path:   req.URL.Path,
		Query:  req.URL.RawQuery,
		Header: req.Header.Clone(),
	}
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &call.Body)
		}
	}
	f.calls = append(f.calls, call)

	res, ok := f.routes[req.URL.Path]
	if !ok {
		res = fakeResponse{status: 202}
	}
	body := res.body
	if body == "" {
		body = "{}"
	}
	return &http.Response{
		StatusCode: res.status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

func newDoer(routes map[string]fakeResponse) *fakeDoer {
	if routes == nil {
		routes = map[string]fakeResponse{}
	}
	return &fakeDoer{routes: routes}
}

func TestJoinStoresTheServerIssuedAgentInstanceID(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/edge/join": {status: 201, body: `{"agentInstanceId":"a-9"}`},
	})
	c := New(cfg, d)
	if _, err := c.Join(context.Background()); err != nil {
		t.Fatalf("join: %v", err)
	}
	if c.AgentInstanceID() != "a-9" {
		t.Errorf("agentInstanceId = %q", c.AgentInstanceID())
	}
	if d.calls[0].Body["edgeAgentLabel"] != "Claude Code" {
		t.Errorf("body = %v", d.calls[0].Body)
	}
	if got := d.calls[0].Header.Get("authorization"); got != "Bearer tok" {
		t.Errorf("authorization = %q", got)
	}
}

func TestHeartbeatContributeAndBriefHitTheEdgeContract(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/edge/join": {status: 201, body: `{"agentInstanceId":"a-9"}`},
		"/o/acme/incidents/inc-1/events":    {status: 200, body: `[{"seq":0,"type":"agent.finding"}]`},
	})
	c := New(cfg, d)
	ctx := context.Background()
	if _, err := c.Join(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Heartbeat(ctx, "investigating"); err != nil {
		t.Fatal(err)
	}
	if err := c.Contribute(ctx, "finding", map[string]any{"text": "origin 502s"}); err != nil {
		t.Fatal(err)
	}
	events, err := c.GetBrief(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var seen []string
	for _, call := range d.calls {
		seen = append(seen, call.Method+" "+call.Path)
	}
	for _, want := range []string{
		"POST /o/acme/incidents/inc-1/edge/heartbeat",
		"POST /o/acme/incidents/inc-1/edge/contributions",
		"GET /o/acme/incidents/inc-1/events",
	} {
		if !contains(seen, want) {
			t.Errorf("missing %s in %v", want, seen)
		}
	}
	if len(events) != 1 || events[0].Type != "agent.finding" {
		t.Errorf("events = %v", events)
	}
	contribBody := d.calls[2].Body
	if contribBody["kind"] != "finding" || contribBody["text"] != "origin 502s" || contribBody["agentInstanceId"] != "a-9" {
		t.Errorf("contribution body = %v", contribBody)
	}
}

func TestVettingAndClaimRoutesCarryTheIdentityPairAndNothingElse(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/edge/join": {status: 201, body: `{"agentInstanceId":"a-9"}`},
	})
	c := New(cfg, d)
	ctx := context.Background()
	if _, err := c.Join(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.FlagContext(ctx, 12, "origin was healthy at that timestamp", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.PositionClaim(ctx, 48, "corroborate", "reproduced locally"); err != nil {
		t.Fatal(err)
	}
	if err := c.StageClaim(ctx, map[string]any{"claimClass": "causal", "statement": "the rollback caused the 5xx"}); err != nil {
		t.Fatal(err)
	}

	var seen []string
	for _, call := range d.calls {
		seen = append(seen, call.Method+" "+call.Path)
	}
	for _, want := range []string{
		"POST /o/acme/incidents/inc-1/vetting/flag",
		"POST /o/acme/incidents/inc-1/claims/48/position",
		"POST /o/acme/incidents/inc-1/claims",
	} {
		if !contains(seen, want) {
			t.Errorf("missing %s in %v", want, seen)
		}
	}
	// The server-issued instance id rides along on all three so the server can
	// attribute the position to {human · agent}; nothing asserts an identity.
	for _, call := range d.calls[1:] {
		if call.Body["agentInstanceId"] != "a-9" || call.Body["edgeAgentLabel"] != "Claude Code" {
			t.Errorf("%s body = %v", call.Path, call.Body)
		}
		for _, forbidden := range []string{"humanActorId", "displayName", "kind"} {
			if _, present := call.Body[forbidden]; present {
				t.Errorf("%s must not carry %q: %v", call.Path, forbidden, call.Body)
			}
		}
	}
	if d.calls[1].Body["reason"] != "origin was healthy at that timestamp" {
		t.Errorf("flag body = %v", d.calls[1].Body)
	}
	if d.calls[2].Body["position"] != "corroborate" {
		t.Errorf("position body = %v", d.calls[2].Body)
	}
	if d.calls[3].Body["claimClass"] != "causal" {
		t.Errorf("stage body = %v", d.calls[3].Body)
	}
}

func TestOmittedOptionalFieldsAreAbsentNotEmpty(t *testing.T) {
	d := newDoer(nil)
	c := New(cfg, d)
	c.SetAgentInstanceID("a-9")
	ctx := context.Background()
	if err := c.PositionClaim(ctx, 7, "contest", ""); err != nil {
		t.Fatal(err)
	}
	if _, present := d.calls[0].Body["reason"]; present {
		t.Errorf("an empty reason must be omitted entirely: %v", d.calls[0].Body)
	}
	if err := c.FlagContext(ctx, 7, "r", ""); err != nil {
		t.Fatal(err)
	}
	if _, present := d.calls[1].Body["targetKind"]; present {
		t.Errorf("an empty targetKind must be omitted entirely: %v", d.calls[1].Body)
	}
}

func TestAttentionAndDivergenceAskForTheAgentsQueueNotItsHumans(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/vetting/attention":  {status: 200, body: `{"votesAwaited":[{"claimSeq":48}]}`},
		"/o/acme/incidents/inc-1/vetting/divergence": {status: 200, body: `{"diverging":true,"establishedSubject":"cli-handoff/redeem","observedSubject":"cloudfront/5xxerrorrate"}`},
	})
	c := New(cfg, d)
	c.SetAgentInstanceID("a-9")
	ctx := context.Background()

	att, err := c.GetAttention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(att.VotesAwaited) != 1 || *att.VotesAwaited[0].ClaimSeq != 48 {
		t.Errorf("attention = %+v", att)
	}
	if d.calls[0].Query != "agentInstanceId=a-9" {
		t.Errorf("query = %q", d.calls[0].Query)
	}

	div, err := c.GetDivergence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !div.Diverging || div.EstablishedSubject != "cli-handoff/redeem" {
		t.Errorf("divergence = %+v", div)
	}
	if d.calls[1].Query != "agentInstanceId=a-9" {
		t.Errorf("query = %q", d.calls[1].Query)
	}
}

func TestTheAgentInstanceParameterIsOmittedEntirelyBeforeOneHasBeenIssued(t *testing.T) {
	// Sending an empty one would be a claim about identity the server must then
	// refuse; sending none is the honest "ask as the human" it already supports.
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/vetting/attention":  {status: 200, body: `{}`},
		"/o/acme/incidents/inc-1/vetting/divergence": {status: 200, body: `{}`},
	})
	c := New(cfg, d)
	ctx := context.Background()
	if _, err := c.GetAttention(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetDivergence(ctx); err != nil {
		t.Fatal(err)
	}
	for _, call := range d.calls {
		if call.Query != "" {
			t.Errorf("%s query = %q, want empty", call.Path, call.Query)
		}
	}
}

// --- 20260906-204144-data-source-sdk / landfall-cli#12: signal catalog + query ---

func TestGetSignalCatalogAcceptsABareArrayAndAppendsTheAgentInstanceQuery(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/plugins": {status: 200, body: `[{"source":"cloudwatch","kinds":["metrics"]}]`},
	})
	c := New(cfg, d)
	c.SetAgentInstanceID("a-9")
	catalog, err := c.GetSignalCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || !strings.Contains(string(catalog[0]), `"source":"cloudwatch"`) {
		t.Errorf("catalog = %v", catalog)
	}
	if d.calls[0].Query != "agentInstanceId=a-9" {
		t.Errorf("query = %q", d.calls[0].Query)
	}
}

func TestGetSignalCatalogUnwrapsAPluginsEnvelope(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/plugins": {status: 200, body: `{"plugins":[{"source":"datadog"}]}`},
	})
	c := New(cfg, d)
	catalog, err := c.GetSignalCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || !strings.Contains(string(catalog[0]), `"source":"datadog"`) {
		t.Errorf("catalog = %v", catalog)
	}
	// No Join happened in this test, so no agentInstanceId is set yet.
	if d.calls[0].Query != "" {
		t.Errorf("query = %q, want empty", d.calls[0].Query)
	}
}

func TestGetSignalCatalogDegradesToEmptyOnAnUnrecognizedShape(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/plugins": {status: 200, body: `{"somethingElse":true}`},
	})
	c := New(cfg, d)
	catalog, err := c.GetSignalCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 0 {
		t.Errorf("catalog = %v, want empty", catalog)
	}
}

func TestQuerySignalsPostsOperationParamsConnectionAccountAndAgentInstance(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/plugins/datadog/invoke": {status: 200, body: `{"partial":false,"points":[1,2,3]}`},
	})
	c := New(cfg, d)
	c.SetAgentInstanceID("a-9")
	result, err := c.QuerySignals(context.Background(), "datadog", "metrics.range",
		map[string]any{"query": "avg:system.cpu"}, "conn-1", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if result["partial"] != false {
		t.Errorf("result = %v", result)
	}
	body := d.calls[0].Body
	if body["operation"] != "metrics.range" || body["connectionId"] != "conn-1" || body["accountId"] != "acct-1" || body["agentInstanceId"] != "a-9" {
		t.Errorf("body = %v", body)
	}
	params, _ := body["params"].(map[string]any)
	if params["query"] != "avg:system.cpu" {
		t.Errorf("params = %v", body["params"])
	}
	// Never the identity()-style {edgeAgentLabel, agentInstanceId:null} pair
	// this route's Edge control-plane counterpart deliberately does not use.
	if _, has := body["edgeAgentLabel"]; has {
		t.Errorf("body must not carry edgeAgentLabel, got %v", body)
	}
}

func TestQuerySignalsOmitsConnectionAccountAndAgentInstanceWhenUnset(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/plugins/cloudwatch/invoke": {status: 200, body: `{}`},
	})
	c := New(cfg, d)
	if _, err := c.QuerySignals(context.Background(), "cloudwatch", "metrics.list", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	body := d.calls[0].Body
	for _, k := range []string{"connectionId", "accountId", "agentInstanceId"} {
		if _, has := body[k]; has {
			t.Errorf("body must omit %s when unset, got %v", k, body)
		}
	}
	if params, ok := body["params"].(map[string]any); !ok || len(params) != 0 {
		t.Errorf("params = %v, want an empty object (never omitted)", body["params"])
	}
}

func TestCursorAndSearchQueryStrings(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/events":              {status: 200, body: `[]`},
		"/o/acme/incidents/inc-1/edge/context/delta":  {status: 200, body: `{"toVersion":9}`},
		"/o/acme/incidents/inc-1/edge/context/search": {status: 200, body: `{"hits":[]}`},
	})
	c := New(cfg, d)
	c.SetAgentInstanceID("a-9")
	ctx := context.Background()

	if _, err := c.GetUpdates(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if d.calls[0].Query != "sinceSeq=7" {
		t.Errorf("query = %q", d.calls[0].Query)
	}
	if _, err := c.GetContextDelta(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if d.calls[1].Query != "sinceVersion=4&agentInstanceId=a-9" {
		t.Errorf("query = %q", d.calls[1].Query)
	}
	if _, err := c.SearchContext(ctx, "cloud front"); err != nil {
		t.Fatal(err)
	}
	// encodeURIComponent renders a space as %20, never as `+`.
	if d.calls[2].Query != "q=cloud%20front&agentInstanceId=a-9" {
		t.Errorf("query = %q", d.calls[2].Query)
	}
}

func TestAPostErrorSurfacesTheServerProvidedReason(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/artifacts":  {status: 413, body: `{"reason":"artifact exceeds the 5 MiB policy"}`},
		"/o/acme/incidents/inc-1/claims":     {status: 400, body: `{"message":"claimClass is required"}`},
		"/o/acme/incidents/inc-1/edge/leave": {status: 500, body: `not json`},
	})
	c := New(cfg, d)
	ctx := context.Background()

	_, err := c.UploadArtifact(ctx, "big.pdf", "application/pdf", "AAA")
	if err == nil || !strings.Contains(err.Error(), "artifact exceeds the 5 MiB policy") {
		t.Errorf("err = %v — the caller must be able to relay a clear cause", err)
	}
	if err := c.StageClaim(ctx, nil); err == nil || !strings.Contains(err.Error(), "claimClass is required") {
		t.Errorf("err = %v — `message` is the fallback for `reason`", err)
	}
	if err := c.Leave(ctx); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err = %v — an unparseable body still reports the status", err)
	}
}

func TestA202CarriesNoBodyToDecode(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/edge/heartbeat": {status: 202, body: ""},
	})
	if err := New(cfg, d).Heartbeat(context.Background(), "investigating"); err != nil {
		t.Fatalf("a 202 must not be treated as a decode failure: %v", err)
	}
}

func TestTheBaseURLLosesExactlyOneTrailingSlash(t *testing.T) {
	d := newDoer(nil)
	slashed := cfg
	slashed.BaseURL = "http://api.test/"
	if err := New(slashed, d).Heartbeat(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if d.calls[0].Path != "/o/acme/incidents/inc-1/edge/heartbeat" {
		t.Errorf("path = %q", d.calls[0].Path)
	}
}

// --- the magic link ---------------------------------------------------------

func TestParseShareLinkExtractsThePartsAndRejectsNonLinks(t *testing.T) {
	p, err := ParseShareLink("http://api.test/o/acme/incidents/inc-1/agent?ticket=tkt-123")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.BaseURL != "http://api.test" || p.Slug != "acme" || p.IncidentID != "inc-1" || p.Ticket != "tkt-123" {
		t.Errorf("parsed = %+v", p)
	}
	if _, err := ParseShareLink("http://api.test/o/acme/incidents/inc-1"); err == nil ||
		!strings.Contains(err.Error(), "agent share link") {
		t.Errorf("err = %v", err)
	}
	if _, err := ParseShareLink("http://api.test/o/acme/incidents/inc-1/agent"); err == nil ||
		!strings.Contains(err.Error(), "missing its join ticket") {
		t.Errorf("err = %v", err)
	}
	if _, err := ParseShareLink("not a url at all"); err == nil {
		t.Error("a non-URL must be refused")
	}
}

func TestRedeemShareLinkPostsTheTicketAndHonoursABaseURLOverride(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/edge/redeem": {status: 201, body: `{"token":"edge-tok","humanActorId":"u-1"}`},
	})
	got, err := RedeemShareLink(context.Background(),
		"http://api.test/o/acme/incidents/inc-1/agent?ticket=tkt-123", RedeemOptions{Doer: d})
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	want := Config{BaseURL: "http://api.test", Slug: "acme", IncidentID: "inc-1", Token: "edge-tok", HumanActorID: "u-1"}
	if got != want {
		t.Errorf("cfg = %+v, want %+v", got, want)
	}
	if d.calls[0].Body["ticket"] != "tkt-123" {
		t.Errorf("body = %v", d.calls[0].Body)
	}

	d2 := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/edge/redeem": {status: 201, body: `{"token":"t"}`},
	})
	override, err := RedeemShareLink(context.Background(),
		"http://api.test/o/acme/incidents/inc-1/agent?ticket=tkt-123",
		RedeemOptions{BaseURL: "http://elsewhere.test", Doer: d2})
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if override.BaseURL != "http://elsewhere.test" {
		t.Errorf("baseUrl = %q — the override must win over the link origin", override.BaseURL)
	}
}

func TestAnExpiredLinkSaysSo(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/o/acme/incidents/inc-1/edge/redeem": {status: 410, body: `{}`},
	})
	_, err := RedeemShareLink(context.Background(),
		"http://api.test/o/acme/incidents/inc-1/agent?ticket=tkt-123", RedeemOptions{Doer: d})
	if err == nil || !strings.Contains(err.Error(), "expired or was already used") {
		t.Errorf("err = %v", err)
	}
}

// --- the short link ----------------------------------------------------------

func TestParseShortLinkExtractsTheCodeAndRejectsNonLinks(t *testing.T) {
	s, ok := ParseShortLink("http://api.test/j/aB3xK9pQ")
	if !ok || s.BaseURL != "http://api.test" || s.Code != "aB3xK9pQ" {
		t.Errorf("parsed = %+v, ok = %v", s, ok)
	}
	for _, notShort := range []string{
		"http://api.test/o/acme/incidents/inc-1/agent?ticket=tkt-123", // the long form
		"http://api.test/j/",
		"not a url at all",
	} {
		if _, ok := ParseShortLink(notShort); ok {
			t.Errorf("ParseShortLink(%q) = ok, want rejected", notShort)
		}
	}
}

// TestRedeemShortLinkPostsToTheCodeEndpointAndReadsSlugAndIncidentBack —
// unlike the long link, the short link carries no slug/incident id of its own
// in the URL text (that is the whole point — an opaque code, resolved
// server-side), so this is the one form where the redeem RESPONSE, not the
// URL, is where Config.Slug/IncidentID come from.
func TestRedeemShortLinkPostsToTheCodeEndpointAndReadsSlugAndIncidentBack(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/j/aB3xK9pQ/redeem": {status: 201, body: `{"token":"edge-tok","humanActorId":"u-1","slug":"acme","incidentId":"inc-1"}`},
	})
	got, err := RedeemShareLink(context.Background(), "http://api.test/j/aB3xK9pQ", RedeemOptions{Doer: d})
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	want := Config{BaseURL: "http://api.test", Slug: "acme", IncidentID: "inc-1", Token: "edge-tok", HumanActorID: "u-1"}
	if got != want {
		t.Errorf("cfg = %+v, want %+v", got, want)
	}
}

func TestAnExpiredShortLinkSaysSo(t *testing.T) {
	d := newDoer(map[string]fakeResponse{
		"/j/aB3xK9pQ/redeem": {status: 410, body: `{}`},
	})
	_, err := RedeemShareLink(context.Background(), "http://api.test/j/aB3xK9pQ", RedeemOptions{Doer: d})
	if err == nil || !strings.Contains(err.Error(), "expired or was already used") {
		t.Errorf("err = %v", err)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
