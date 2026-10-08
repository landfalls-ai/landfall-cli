// context.js: the Context tab of the console (spec §4.10). Stub until built.

// THE CONSOLE CONTRACT (core.js CONSOLE): console.js calls these by name.
export function tab(k, io, nowMs, args) {
  return []
}

export function badge() {
  return null
}

export function warm(io) {}

// Shared with roster.js's person view (spec §4.3.1, §4.10). Stubs until built.
// One artifact row as §4.10 draws it; `a` is a row of `landfall artifacts`.
export function artifactRow(k, a, opts) {
  return null
}

// Reads the artifact and appends it to the person's conversation (§4.10's two shapes), with the
// toast. Answers the deny reason, or null once added.
export async function addArtifact(io, a) {
  return 'Not built yet.'
}

// The post-join add (§2.6, §4.10): the room's shared context into the conversation.
export async function addSharedContext(io) {
  return 'Not built yet.'
}
