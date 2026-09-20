package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestInitializeHandsTheClientInfoToTheServerWhenPresent(t *testing.T) {
	var got *ClientInfo
	opts := Options{OnInitialize: func(ci ClientInfo) { got = &ci }}
	req := &Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1.267"}}`)}
	res := HandleMessage(context.Background(), req, opts)
	if res == nil || res.Error != nil {
		t.Fatalf("initialize failed: %+v", res)
	}
	if got == nil || got.Name != "claude-code" || got.Version != "2.1.267" {
		t.Fatalf("clientInfo not delivered: %+v", got)
	}
}

func TestInitializeWithoutClientInfoOrWithoutAListenerStillAnswers(t *testing.T) {
	// No listener: nothing to call, the answer is unchanged.
	req := &Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{}`)}
	if res := HandleMessage(context.Background(), req, Options{}); res == nil || res.Error != nil {
		t.Fatalf("initialize without a listener failed: %+v", res)
	}
	// A listener but no params: called with a zero ClientInfo, never a panic.
	called := false
	opts := Options{OnInitialize: func(ci ClientInfo) { called = true }}
	bare := &Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "initialize"}
	if res := HandleMessage(context.Background(), bare, opts); res == nil || res.Error != nil {
		t.Fatalf("initialize with no params failed: %+v", res)
	}
	if called {
		t.Fatal("no params means no clientInfo to hand over; the listener must not be called with an invented one")
	}
}
