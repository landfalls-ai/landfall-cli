// mcp.mjs — a minimal, dependency-free MCP server over stdio (newline-delimited
// JSON-RPC 2.0). Enough for an MCP-capable local agent (Claude Code, Cursor, …) to
// connect to `warroom serve`, list the incident-scoped tools, and call them. The
// message handler is pure (request → response) so it is node-testable without any
// stream. (No @modelcontextprotocol/sdk dependency — the protocol surface we need
// is small and stable.)

const PROTOCOL_VERSION = '2024-11-05';

/**
 * Handle one JSON-RPC request. Returns a response object, or null for
 * notifications (no id / initialized). `tools` = [{name, description, inputSchema,
 * handler(args)->Promise<string>}].
 */
export async function handleMcpMessage(msg, { tools, serverInfo }) {
  if (!msg || msg.jsonrpc !== '2.0') return errorResponse(msg?.id ?? null, -32600, 'invalid request');
  const { id, method, params } = msg;
  const isNotification = id === undefined || id === null;

  switch (method) {
    case 'initialize':
      return result(id, {
        protocolVersion: PROTOCOL_VERSION,
        capabilities: { tools: {} },
        serverInfo: serverInfo ?? { name: 'landfall-warroom', version: '0.1.0' },
      });
    case 'notifications/initialized':
    case 'initialized':
      return null; // notification — no response
    case 'ping':
      return result(id, {});
    case 'tools/list':
      return result(id, { tools: tools.map((t) => ({ name: t.name, description: t.description, inputSchema: t.inputSchema })) });
    case 'tools/call': {
      const name = params?.name;
      const tool = tools.find((t) => t.name === name);
      if (!tool) return errorResponse(id, -32602, `unknown tool "${name}"`);
      try {
        const text = await tool.handler(params?.arguments ?? {});
        return result(id, { content: [{ type: 'text', text: String(text) }] });
      } catch (e) {
        return result(id, { content: [{ type: 'text', text: `error: ${e.message}` }], isError: true });
      }
    }
    default:
      return isNotification ? null : errorResponse(id, -32601, `method not found: ${method}`);
  }
}

function result(id, res) { return { jsonrpc: '2.0', id, result: res }; }
function errorResponse(id, code, message) { return { jsonrpc: '2.0', id, error: { code, message } }; }

/**
 * Run the stdio MCP server: read newline-delimited JSON-RPC from `input`, write
 * responses to `output`. Returns a stop() function. Used by `warroom serve`.
 */
export function runStdioServer(tools, { input = process.stdin, output = process.stdout, serverInfo } = {}) {
  let buffer = '';
  const onData = async (chunk) => {
    buffer += chunk.toString('utf8');
    let nl;
    while ((nl = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, nl).trim();
      buffer = buffer.slice(nl + 1);
      if (!line) continue;
      let msg;
      try { msg = JSON.parse(line); } catch { continue; }
      const res = await handleMcpMessage(msg, { tools, serverInfo });
      if (res) output.write(`${JSON.stringify(res)}\n`);
    }
  };
  input.on('data', onData);
  input.resume?.();
  return () => input.off('data', onData);
}
