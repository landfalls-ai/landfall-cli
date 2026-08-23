package mcp

// Ported from the MCP-protocol half of `test/edge-bridge.test.mjs`. The
// easy-to-get-backwards case is the last group: a handler that fails is a
// SUCCESSFUL result carrying isError:true, never a JSON-RPC error.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testTools() []Tool {
	return []Tool{
		{
			Name:        "get_brief",
			Description: "Get the current incident brief.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			Handler: func(context.Context, map[string]any) (string, error) {
				return "the brief", nil
			},
		},
		{
			Name:        "post_finding",
			Description: "Post a finding.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				return "posted: " + args["text"].(string), nil
			},
		},
		{
			Name:        "boom",
			Description: "Always fails.",
			InputSchema: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (string, error) {
				return "", errors.New("not connected — call join_war_room with a Landfall agent share link first")
			},
		},
	}
}

// call runs one raw frame through the pure handler and returns the marshalled
// response, so the assertions are about the WIRE shape, not a Go struct.
func call(t *testing.T, frame string, opts Options) map[string]any {
	t.Helper()
	out, ok := HandleLine(context.Background(), []byte(frame), opts)
	if !ok {
		t.Fatalf("expected a response for %s", frame)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshalling response: %v", err)
	}
	return decoded
}

func TestInitializePinsTheProtocolVersionAndServerInfo(t *testing.T) {
	res := call(t, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		Options{Tools: testTools(), ServerInfo: &ServerInfo{Name: "landfall", Version: "9.9.9"}})

	result := res["result"].(map[string]any)
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	caps := result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Error("capabilities must advertise tools")
	}
	info := result["serverInfo"].(map[string]any)
	if info["name"] != "landfall" || info["version"] != "9.9.9" {
		t.Errorf("serverInfo = %v — the version must be the real build version, never a literal", info)
	}
}

// The fallback is a faithful port of mcp.mjs's `serverInfo ?? { name:
// 'landfall', version: '0.1.0' }` and must keep working for a caller that
// genuinely has no identity to declare — but it is a LAST RESORT, not the
// value the real `landfall serve` path should ever report. bin/landfall.mjs
// passes the actual release version explicitly (bin/landfall.mjs:487), and the
// Go serve command must do the same rather than inheriting 0.1.0 by omission.
// This test pins the fallback so the day someone sees "0.1.0" in an MCP client
// they can tell "no ServerInfo was wired" apart from "the build says 0.1.0".
func TestInitializeFallsBackToTheStubIdentityOnlyWhenNoServerInfoIsWired(t *testing.T) {
	res := call(t, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, Options{Tools: testTools()})
	info := res["result"].(map[string]any)["serverInfo"].(map[string]any)
	if info["name"] != "landfall" || info["version"] != "0.1.0" {
		t.Errorf("fallback serverInfo = %v, want the landfall/0.1.0 stub", info)
	}
}

func TestInitializeOmitsInstructionsEntirelyWhenNoneAreSupplied(t *testing.T) {
	// Omitted, never null and never "" — the client folds this straight into
	// the model's context, and an empty string is not the same as no guidance.
	bare := call(t, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, Options{Tools: testTools()})
	result := bare["result"].(map[string]any)
	if _, present := result["instructions"]; present {
		t.Fatalf("instructions must be absent, got %#v", result["instructions"])
	}

	withInstr := call(t, `{"jsonrpc":"2.0","id":2,"method":"initialize"}`,
		Options{Tools: testTools(), Instructions: "standing guidance"})
	if got := withInstr["result"].(map[string]any)["instructions"]; got != "standing guidance" {
		t.Errorf("instructions = %v", got)
	}
}

func TestToolsListCarriesNameDescriptionAndSchema(t *testing.T) {
	res := call(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, Options{Tools: testTools()})
	listed := res["result"].(map[string]any)["tools"].([]any)
	if len(listed) != 3 {
		t.Fatalf("tools = %d, want 3", len(listed))
	}
	first := listed[0].(map[string]any)
	for _, key := range []string{"name", "description", "inputSchema"} {
		if _, ok := first[key]; !ok {
			t.Errorf("listed tool is missing %q", key)
		}
	}
	if _, leaked := first["handler"]; leaked {
		t.Error("the handler must never be serialized onto the wire")
	}
}

func TestPingAnswersAnEmptyObject(t *testing.T) {
	res := call(t, `{"jsonrpc":"2.0","id":3,"method":"ping"}`, Options{Tools: testTools()})
	result, ok := res["result"].(map[string]any)
	if !ok || len(result) != 0 {
		t.Fatalf("ping result = %#v, want {}", res["result"])
	}
}

func TestInitializedNotificationsGetNoResponse(t *testing.T) {
	for _, frame := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialized"}`,
	} {
		if _, ok := HandleLine(context.Background(), []byte(frame), Options{Tools: testTools()}); ok {
			t.Errorf("%s must produce no response", frame)
		}
	}
}

func TestToolsCallDispatchesByName(t *testing.T) {
	res := call(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"post_finding","arguments":{"text":"origin regressed"}}}`,
		Options{Tools: testTools()})
	result := res["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	if content["type"] != "text" || content["text"] != "posted: origin regressed" {
		t.Fatalf("content = %#v", content)
	}
	if _, present := result["isError"]; present {
		t.Error("a successful call must not carry isError at all")
	}
}

func TestUnknownToolIsAJSONRPCError(t *testing.T) {
	res := call(t, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope"}}`,
		Options{Tools: testTools()})
	rpcErr, ok := res["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %#v", res)
	}
	if rpcErr["code"].(float64) != -32602 {
		t.Errorf("code = %v, want -32602", rpcErr["code"])
	}
	if !strings.Contains(rpcErr["message"].(string), "nope") {
		t.Errorf("message = %v — it must name the tool that was asked for", rpcErr["message"])
	}
}

// The one that is easy to get backwards.
func TestAFailingHandlerIsASuccessfulResultWithIsErrorNotAJSONRPCError(t *testing.T) {
	res := call(t, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"boom"}}`,
		Options{Tools: testTools()})
	if _, isRPCError := res["error"]; isRPCError {
		t.Fatal("a throwing handler must NOT become a JSON-RPC error — the model has to be able to read it")
	}
	result := res["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("isError = %v, want true", result["isError"])
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "error: ") {
		t.Errorf("text = %q, want an `error: ` prefix", text)
	}
	if !strings.Contains(text, "join_war_room") {
		t.Errorf("text = %q — the fail-closed message must reach the caller intact", text)
	}
}

func TestAnUnknownMethodIs32601UnlessItIsANotification(t *testing.T) {
	res := call(t, `{"jsonrpc":"2.0","id":7,"method":"resources/list"}`, Options{Tools: testTools()})
	if code := res["error"].(map[string]any)["code"].(float64); code != -32601 {
		t.Errorf("code = %v, want -32601", code)
	}
	// No id → a notification → nothing is written at all.
	if _, ok := HandleLine(context.Background(), []byte(`{"jsonrpc":"2.0","method":"resources/list"}`), Options{Tools: testTools()}); ok {
		t.Error("an unknown NOTIFICATION must produce no response")
	}
	// An explicit null id is a notification too.
	if _, ok := HandleLine(context.Background(), []byte(`{"jsonrpc":"2.0","id":null,"method":"resources/list"}`), Options{Tools: testTools()}); ok {
		t.Error("id:null is a notification — no response")
	}
}

func TestANonTwoPointZeroEnvelopeIs32600(t *testing.T) {
	for _, frame := range []string{
		`{"jsonrpc":"1.0","id":8,"method":"ping"}`,
		`{"id":9,"method":"ping"}`,
		`5`,
		`"nope"`,
	} {
		res := call(t, frame, Options{Tools: testTools()})
		rpcErr, ok := res["error"].(map[string]any)
		if !ok {
			t.Fatalf("%s → %#v, want an error", frame, res)
		}
		if rpcErr["code"].(float64) != -32600 {
			t.Errorf("%s → code %v, want -32600", frame, rpcErr["code"])
		}
	}
}

func TestAnUnparseableLineIsSilentlySkipped(t *testing.T) {
	for _, frame := range []string{`{not json`, `{"jsonrpc":`, ``} {
		if _, ok := HandleLine(context.Background(), []byte(frame), Options{Tools: testTools()}); ok {
			t.Errorf("%q must be skipped silently, not answered", frame)
		}
	}
}

func TestTheIDIsEchoedBackVerbatim(t *testing.T) {
	res := call(t, `{"jsonrpc":"2.0","id":"abc-1","method":"ping"}`, Options{Tools: testTools()})
	if res["id"] != "abc-1" {
		t.Errorf("id = %#v — a string id must survive the round trip", res["id"])
	}
	// An invalid envelope with no id answers with a null id, never a missing one.
	out, _ := HandleLine(context.Background(), []byte(`{"jsonrpc":"1.0"}`), Options{Tools: testTools()})
	if !strings.Contains(string(out), `"id":null`) {
		t.Errorf("response = %s, want an explicit null id", out)
	}
}

func TestServeReadsNewlineDelimitedFramesAndWritesOnePerResponse(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`not json at all`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_brief","arguments":{}}}`,
		"",
	}, "\n"))
	var out strings.Builder

	if err := Serve(context.Background(), in, &out, Options{Tools: testTools()}); err != nil {
		t.Fatalf("serve: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines, want 2 (the skipped line and the notification write nothing): %q", len(lines), out.String())
	}
	if !strings.Contains(lines[1], "the brief") {
		t.Errorf("second response = %s", lines[1])
	}
	// Framing is newline-delimited — never Content-Length.
	if strings.Contains(out.String(), "Content-Length") {
		t.Error("this transport must not emit Content-Length framing")
	}
}
