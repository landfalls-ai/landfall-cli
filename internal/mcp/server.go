// Package mcp is a minimal, dependency-free MCP server over stdio
// (newline-delimited JSON-RPC 2.0) — a Go port of `src/mcp.mjs`. Enough for an
// MCP-capable local agent (Claude Code, Cursor, …) to connect to
// `landfall serve`, list the incident-scoped tools, and call them.
//
// HandleMessage is pure (request → response), so it is testable without any
// stream — the same property the Node original was written for.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// ProtocolVersion is the MCP revision this server speaks. Pinned, not
// negotiated — the Node original pins the identical literal.
const ProtocolVersion = "2024-11-05"

// Handler runs one tool call. A returned error becomes a SUCCESSFUL JSON-RPC
// result carrying `isError: true`, never a JSON-RPC error — see HandleMessage.
type Handler func(ctx context.Context, args map[string]any) (string, error)

// Tool is one entry of the `tools/list` surface plus its handler.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Handler     Handler        `json:"-"`
}

// ServerInfo is the `initialize` result's server identity. Version MUST be
// wired to the real build version (`-ldflags -X`), never a hardcoded literal.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Options is everything HandleMessage needs to answer.
type Options struct {
	Tools      []Tool
	ServerInfo *ServerInfo
	// Instructions carries the fixed standing guidance the MCP client folds
	// into the model's context on connect. The key is OMITTED entirely when
	// empty — never emitted as null or "".
	Instructions string
}

// Request is one inbound JSON-RPC envelope. ID stays raw so a string id, a
// numeric id and an absent id are all preserved exactly as sent.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// Response is one outbound JSON-RPC envelope.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// InitializeResult is the `initialize` answer.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	// Omitted entirely when no instructions were supplied.
	Instructions string `json:"instructions,omitempty"`
}

// Capabilities is the (currently tools-only) capability advertisement.
type Capabilities struct {
	Tools map[string]any `json:"tools"`
}

// ToolsListResult is the `tools/list` answer.
type ToolsListResult struct {
	Tools []listedTool `json:"tools"`
}

type listedTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Content is one block of a tool result.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallToolResult is the `tools/call` answer. IsError is omitted when false, so
// an ordinary success is byte-identical to the Node original's.
type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type callParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// isNotification reports whether an envelope carries no usable id — a JS
// `undefined` (absent key) or an explicit `null`.
func isNotification(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	return string(id) == "null"
}

// HandleMessage answers one JSON-RPC request. It returns nil for
// notifications (no id, or `initialized`), matching `handleMcpMessage`'s
// "return null → write nothing" contract.
func HandleMessage(ctx context.Context, msg *Request, opts Options) *Response {
	if msg == nil || msg.JSONRPC != "2.0" {
		var id json.RawMessage
		if msg != nil {
			id = msg.ID
		}
		return errorResponse(id, -32600, "invalid request")
	}

	switch msg.Method {
	case "initialize":
		info := ServerInfo{Name: "landfall", Version: "0.1.0"}
		if opts.ServerInfo != nil {
			info = *opts.ServerInfo
		}
		return result(msg.ID, InitializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities:    Capabilities{Tools: map[string]any{}},
			ServerInfo:      info,
			Instructions:    opts.Instructions,
		})

	case "notifications/initialized", "initialized":
		return nil // notification — no response

	case "ping":
		return result(msg.ID, map[string]any{})

	case "tools/list":
		listed := make([]listedTool, 0, len(opts.Tools))
		for _, t := range opts.Tools {
			listed = append(listed, listedTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
		return result(msg.ID, ToolsListResult{Tools: listed})

	case "tools/call":
		var p callParams
		if len(msg.Params) > 0 {
			_ = json.Unmarshal(msg.Params, &p)
		}
		var tool *Tool
		for i := range opts.Tools {
			if opts.Tools[i].Name == p.Name {
				tool = &opts.Tools[i]
				break
			}
		}
		if tool == nil || tool.Handler == nil {
			return errorResponse(msg.ID, -32602, fmt.Sprintf("unknown tool %q", p.Name))
		}
		args := p.Arguments
		if args == nil {
			args = map[string]any{}
		}
		text, err := tool.Handler(ctx, args)
		if err != nil {
			// A handler that fails is a SUCCESSFUL result carrying
			// isError:true — the MCP client shows it to the model, which can
			// then correct itself. A JSON-RPC error would instead look like a
			// broken server.
			return result(msg.ID, CallToolResult{
				Content: []Content{{Type: "text", Text: "error: " + err.Error()}},
				IsError: true,
			})
		}
		return result(msg.ID, CallToolResult{Content: []Content{{Type: "text", Text: text}}})

	default:
		if isNotification(msg.ID) {
			return nil
		}
		return errorResponse(msg.ID, -32601, "method not found: "+msg.Method)
	}
}

// HandleLine parses one newline-delimited frame and renders the response.
// `ok` is false when nothing should be written: an unparseable line (silently
// skipped, exactly as the Node original's `catch { continue }`) or a
// notification.
func HandleLine(ctx context.Context, line []byte, opts Options) (out []byte, ok bool) {
	if !json.Valid(line) {
		return nil, false // unparseable — skip, write nothing
	}
	var msg *Request
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(line, &envelope); err != nil {
		// Valid JSON, but not an object (`5`, `"x"`, `[]`): JS would still
		// reach the `jsonrpc !== '2.0'` check and answer -32600.
		msg = nil
	} else {
		msg = &Request{}
		if err := json.Unmarshal(line, msg); err != nil {
			msg = nil
		}
	}
	res := HandleMessage(ctx, msg, opts)
	if res == nil {
		return nil, false
	}
	encoded, err := json.Marshal(res)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// Serve runs the stdio MCP server: read newline-delimited JSON-RPC from `in`,
// write responses to `out`. It returns when `in` is exhausted or `ctx` is done.
func Serve(ctx context.Context, in io.Reader, out io.Writer, opts Options) error {
	scanner := bufio.NewScanner(in)
	// A rendered context frame or a large tool result can exceed bufio's 64KB
	// default line cap, and a silently truncated frame is worse than a slow one.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := trimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		res, ok := HandleLine(ctx, line, opts)
		if !ok {
			continue
		}
		if _, err := out.Write(append(res, '\n')); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpace(b[start]) {
		start++
	}
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func result(id json.RawMessage, res any) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Result: res}
}

func errorResponse(id json.RawMessage, code int, message string) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: message}}
}
