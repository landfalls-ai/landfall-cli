// narrate.mjs — turn a local agent's tool call into a human "doing" line (and,
// where it carries a durable artifact, a timeline contribution). This is what
// makes a teammate's edge investigation narrate itself into the war room: every
// action the agent takes maps to a presence heartbeat + optional contribution,
// with NO extra effort from the teammate. Pure + node-testable.

/** A friendly present-tense phrase for what the agent is doing right now. */
export function narrateDoing(toolName, args = {}) {
  const a = args ?? {};
  switch (toolName) {
    case 'get_brief': return 'reviewing the incident brief';
    case 'get_updates': return 'checking for new shared context';
    case 'read_timeline': return 'reading the incident timeline';
    case 'search_context': return `searching context${a.query ? ` for "${a.query}"` : ''}`;
    case 'post_finding': return `posting a finding${a.text ? `: ${truncate(a.text)}` : ''}`;
    case 'note': return `noting: ${truncate(a.text ?? '')}`;
    case 'propose_action': return `proposing a remediation${a.description ? `: ${truncate(a.description)}` : ''}`;
    case 'post_widget': return `building a dashboard widget${a.title ? `: ${truncate(a.title)}` : ''}`;
    case 'upload_artifact': return `sharing an artifact${a.filename ? `: "${truncate(a.filename)}"` : ''}`;
    case 'record_activity': return String(a.doing ?? 'investigating');
    default: return `using ${toolName}`;
  }
}

/**
 * If a tool call produces a durable artifact for the shared timeline, return the
 * contribution to post ({kind, body}); otherwise null (read-only actions only
 * update presence via the heartbeat). Keeps the shared timeline signal-rich, not
 * noisy with every read.
 */
export function contributionFor(toolName, args = {}) {
  const a = args ?? {};
  switch (toolName) {
    case 'post_finding':
    case 'note':
      return { kind: 'finding', body: { text: String(a.text ?? ''), resource: a.resource } };
    case 'search_context':
      return { kind: 'query', body: { source: 'edge', operation: 'search', resource: String(a.query ?? '') } };
    case 'propose_action':
      return { kind: 'action', body: { description: String(a.description ?? ''), dryRunPreview: String(a.dryRunPreview ?? '') } };
    case 'post_widget':
      return { kind: 'widget', body: { widgetType: a.widgetType, title: String(a.title ?? ''), data: a.data } };
    case 'upload_artifact':
      // The upload endpoint itself appends the durable `artifact.shared` event
      // (feature 025), so the generic contribution path MUST NOT double-post.
      // The heartbeat still narrates presence ("sharing an artifact: …").
      return null;
    default:
      return null; // get_brief / read_timeline / record_activity → presence only
  }
}

function truncate(s, n = 80) {
  s = String(s);
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
}
