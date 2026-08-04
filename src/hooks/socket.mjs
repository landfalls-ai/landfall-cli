// socket.mjs — the local query socket that makes `landfall serve` answerable by
// a lifecycle hook (#225; the operator decision recorded on
// landfalls-ai/landfall#225).
//
// The problem it solves: a Stop/FileChanged hook is a SEPARATE, short-lived
// process spawned by the agent harness. The two things it needs — the queue of
// room events the session has not consumed, and that session's cursor — are
// in-process state on the serve session (`pending`, `cursor` in ../tools.mjs).
// A hook cannot reach that memory.
//
// So `landfall serve` binds a listener and answers questions about state it
// already maintains. Deliberately NOT a new daemon: nothing to supervise, no
// PID file, no start/stop command — `landfall serve` is the process, made
// queryable.
//
//   Claude Code ──stdio──► landfall serve ◄──socket── landfall hooks stop
//    (host)                  │  session.pending[]      (short-lived process)
//                            │  session.cursor
//                            ▼
//                      watchIncident() ──ws──► core-api /realtime
//
// WHY A SOCKET AND NOT A STATE FILE. It is the only option that yields exact
// per-session cursors. Under a state file, "what has THIS session consumed?" is
// a snapshot race between processes; over a socket it has one correct answer —
// which, for a hook whose whole job is "you have not seen this yet", is the
// difference between correct and approximately correct.
//
// SECURITY. There is no network listener: a filesystem socket has no port and
// nothing routable. Access control is the 0700 DIRECTORY, not the socket file —
// some BSD-derived kernels (macOS included) historically ignore permission bits
// on the socket node itself. Node exposes no SO_PEERCRED, so the connecting
// process's uid cannot be verified in-process; the directory mode is the entire
// enforcement story, and that is stated rather than glossed.
//
// The verb set is the other half of the boundary. `status`, `peek` and
// `consume` read state and move a cursor. There is deliberately NO verb that
// writes to the room, posts a finding, or returns the session token — this
// socket must never become a second way to ACT in a war room, only to ask
// about one.
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { promises as fs, realpathSync } from 'node:fs';
import { formatEventLine } from '../narrate.mjs';

/** Bumped only on a breaking change to the request/response shapes below. */
export const SOCKET_PROTOCOL_VERSION = 1;

/** How long a hook waits on one socket before giving up on it. */
export const SOCKET_TIMEOUT_MS = 250;

/**
 * The join key between a hook process and a serve process is the WORKSPACE
 * directory: the harness runs hooks with cwd = the workspace and starts the MCP
 * server there too, so cwd pairs them with no session id to thread anywhere.
 */
export function workspaceKey(cwd = process.cwd()) {
  let real = String(cwd);
  try {
    real = realpathSync(real);
  } catch {
    /* not resolvable (deleted/permissions) — hash what we were given */
  }
  return createHash('sha256').update(real).digest('hex').slice(0, 16);
}

/**
 * Where this workspace's sockets live, and how they are named.
 *
 * `XDG_RUNTIME_DIR` is already per-user and 0700 on Linux. macOS does not set
 * it, so fall back to `~/.local/state/landfall/run/`. Windows takes a named
 * pipe through the same `net` API; pipes have no directory to mode-protect and
 * are per-user by default ACL.
 *
 * One socket PER SERVE PROCESS, named by pid, because two agent windows on one
 * repo are two sessions with two cursors.
 */
export function socketLocation({ cwd = process.cwd(), env = process.env, platform = process.platform } = {}) {
  const key = workspaceKey(cwd);
  if (platform === 'win32') {
    const prefix = `landfall-${key}-`;
    return {
      platform,
      key,
      dir: '\\\\.\\pipe\\',
      prefix,
      nameFor: (pid) => `${prefix}${pid}`,
      pathFor: (name) => `\\\\.\\pipe\\${name}`,
      isOurs: (name) => name.startsWith(prefix),
    };
  }
  const base = env.XDG_RUNTIME_DIR
    ? path.join(env.XDG_RUNTIME_DIR, 'landfall')
    : path.join(env.HOME ?? os.homedir(), '.local', 'state', 'landfall', 'run');
  const dir = path.join(base, key);
  return {
    platform,
    key,
    dir,
    prefix: '',
    nameFor: (pid) => `${pid}.sock`,
    pathFor: (name) => path.join(dir, name),
    isOurs: (name) => name.endsWith('.sock'),
  };
}

/**
 * Handle one request against a live bridge session. Pure apart from the cursor
 * move `consume` performs — which is why the whole protocol is testable without
 * a socket, a serve process or a war room.
 */
export function handleSocketRequest(request, session, { pid = process.pid, consume } = {}) {
  const op = request?.op;
  const pending = Array.isArray(session?.pending) ? session.pending : [];
  const base = { ok: true, v: SOCKET_PROTOCOL_VERSION, pid };

  switch (op) {
    case 'status':
      return {
        ...base,
        incidentId: session?.client?.cfg?.incidentId ?? null,
        slug: session?.client?.cfg?.slug ?? null,
        connected: Boolean(session?.client),
        cursor: session?.cursor ?? -1,
        pending: pending.length,
        dropped: session?.pendingDropped ?? 0,
      };

    case 'peek':
      // Non-destructive, deliberately: a hook that crashes between asking and
      // reporting must leave the events queued. See the order note in stop.mjs.
      return {
        ...base,
        count: pending.length,
        dropped: session?.pendingDropped ?? 0,
        cursor: session?.cursor ?? -1,
        maxSeq: pending.length ? pending[pending.length - 1].seq : (session?.cursor ?? -1),
        digest: pending.map((e) => formatEventLine(e)),
      };

    case 'consume': {
      const upTo = request?.upTo;
      if (typeof upTo !== 'number') return { ok: false, v: SOCKET_PROTOCOL_VERSION, error: 'consume requires a numeric upTo' };
      const cursor = consume ? consume(session, upTo) : session?.cursor;
      return { ...base, cursor };
    }

    default:
      return { ok: false, v: SOCKET_PROTOCOL_VERSION, error: `unknown op: ${op ?? '(none)'}` };
  }
}

/** True when something is already listening at `socketPath` (vs. a stale node). */
function probe(socketPath, timeoutMs = SOCKET_TIMEOUT_MS) {
  return new Promise((resolve) => {
    const conn = net.connect(socketPath);
    const done = (alive) => {
      conn.destroy();
      resolve(alive);
    };
    conn.setTimeout(timeoutMs, () => done(false));
    conn.on('connect', () => done(true));
    conn.on('error', () => done(false));
  });
}

/**
 * Bind the query socket for a serve session. Returns `{ socketPath, close() }`,
 * or `null` when binding is impossible — a serve process that cannot offer the
 * socket must still serve MCP, so every failure here is non-fatal by design.
 */
export async function startHookSocket(session, { cwd, env, platform, pid = process.pid, consume, log } = {}) {
  const loc = socketLocation({ cwd, env, platform });
  if (loc.platform !== 'win32') {
    try {
      await fs.mkdir(loc.dir, { recursive: true, mode: 0o700 });
      await fs.chmod(loc.dir, 0o700); // pre-existing dir: 0700 is the access control
    } catch (err) {
      log?.(`hook socket unavailable (${err.message}) — lifecycle hooks will not see this session.`);
      return null;
    }
  }

  const server = net.createServer((conn) => {
    let buffer = '';
    conn.setTimeout(SOCKET_TIMEOUT_MS * 4, () => conn.destroy());
    conn.on('error', () => conn.destroy());
    conn.on('data', (chunk) => {
      buffer += chunk.toString('utf8');
      const nl = buffer.indexOf('\n');
      if (nl < 0) return;
      const line = buffer.slice(0, nl).trim();
      buffer = '';
      let request = null;
      try {
        request = JSON.parse(line);
      } catch {
        /* malformed — answered by the default branch below */
      }
      let response;
      try {
        response = handleSocketRequest(request, session, { pid, consume });
      } catch (err) {
        response = { ok: false, v: SOCKET_PROTOCOL_VERSION, error: err.message };
      }
      // One request per connection: a hook lives for milliseconds, and a
      // long-lived subscription is the second lifecycle this design avoids.
      conn.end(`${JSON.stringify(response)}\n`);
    });
  });

  let socketPath = loc.pathFor(loc.nameFor(pid));
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      await listen(server, socketPath);
      break;
    } catch (err) {
      if (err.code !== 'EADDRINUSE' || attempt === 2) {
        log?.(`hook socket unavailable (${err.message}) — lifecycle hooks will not see this session.`);
        return null;
      }
      // EADDRINUSE: a crashed sibling leaves the node behind. Connecting tells
      // the two apart — dead means unlink and rebind, alive means take the next
      // name rather than evict a live session.
      if (await probe(socketPath)) socketPath = loc.pathFor(loc.nameFor(`${pid}-${attempt + 1}`));
      else await fs.unlink(socketPath).catch(() => {});
    }
  }

  if (loc.platform !== 'win32') await fs.chmod(socketPath, 0o600).catch(() => {});
  server.unref?.(); // never hold the process open on the socket's account

  return {
    socketPath,
    async close() {
      await new Promise((resolve) => server.close(resolve));
      if (loc.platform !== 'win32') await fs.unlink(socketPath).catch(() => {});
    },
  };
}

function listen(server, socketPath) {
  return new Promise((resolve, reject) => {
    const onError = (err) => {
      server.off('listening', onListening);
      reject(err);
    };
    const onListening = () => {
      server.off('error', onError);
      resolve();
    };
    server.once('error', onError);
    server.once('listening', onListening);
    server.listen(socketPath);
  });
}

/** Every socket currently present for this workspace. Empty is the fast path. */
export async function listHookSockets({ cwd, env, platform } = {}) {
  const loc = socketLocation({ cwd, env, platform });
  let names;
  try {
    names = await fs.readdir(loc.dir);
  } catch {
    // No directory means no serve process has ever run here — the common case
    // for a hook, and the one that must cost nothing.
    return [];
  }
  return names
    .filter((n) => loc.isOurs(n))
    .sort()
    .map((n) => loc.pathFor(n));
}

/** Send one request to one socket. Rejects rather than hanging on a stale node. */
export function sendToSocket(socketPath, request, { timeoutMs = SOCKET_TIMEOUT_MS } = {}) {
  return new Promise((resolve, reject) => {
    const conn = net.connect(socketPath);
    let buffer = '';
    let settled = false;
    const fail = (err) => {
      if (settled) return;
      settled = true;
      conn.destroy();
      reject(err);
    };
    conn.setTimeout(timeoutMs, () => fail(new Error(`timed out after ${timeoutMs}ms`)));
    conn.on('error', fail);
    conn.on('connect', () => conn.write(`${JSON.stringify(request)}\n`));
    conn.on('data', (chunk) => {
      buffer += chunk.toString('utf8');
      const nl = buffer.indexOf('\n');
      if (nl < 0) return;
      settled = true;
      conn.end();
      try {
        resolve(JSON.parse(buffer.slice(0, nl)));
      } catch (err) {
        reject(err);
      }
    });
    conn.on('close', () => fail(new Error('socket closed before a response')));
  });
}

/**
 * Ask every socket in this workspace the same question and keep the answers
 * that arrived, each tagged with the socket it came from so a follow-up
 * `consume` reaches the right session.
 *
 * A hook UNIONS across sockets. That over-reports (you may be shown a sibling
 * session's context) and never under-reports — and under-reporting is the exact
 * failure this feature exists to prevent, so it is the right direction to be
 * wrong in. An unreachable socket is silently skipped: it is a crashed session's
 * leftover far more often than a live one that owes us an answer.
 */
export async function queryHookSockets(request, { cwd, env, platform, timeoutMs = SOCKET_TIMEOUT_MS } = {}) {
  const sockets = await listHookSockets({ cwd, env, platform });
  if (!sockets.length) return [];
  const answers = await Promise.all(
    sockets.map((socketPath) =>
      sendToSocket(socketPath, request, { timeoutMs }).then(
        (response) => ({ socketPath, response }),
        () => null,
      ),
    ),
  );
  return answers.filter((a) => a && a.response?.ok);
}
