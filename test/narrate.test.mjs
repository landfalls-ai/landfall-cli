// narrate.test.mjs — feature 20260812-010632 (US5/T050a, FR-029): the
// no-evidence marker on templated content, in the one renderer both the
// in-band tool-result block and the idle-path digest share.
import test from 'node:test';
import assert from 'node:assert/strict';
import { eventText, formatEventLine, isTemplated } from '../src/narrate.mjs';

test('isTemplated reads the explicit disclosedAsTemplated marker, not just synthesized:false', () => {
  assert.equal(isTemplated({ synthesized: false, disclosedAsTemplated: true }), true);
  // synthesized:false ALONE (no explicit marker) must not be treated as
  // templated — the server sets both together deliberately (see
  // narrate.mjs's own doc comment); reading synthesized alone would be a
  // second, drifting interpretation of a field this feature does not own.
  assert.equal(isTemplated({ synthesized: false }), false);
  assert.equal(isTemplated({}), false);
  assert.equal(isTemplated(undefined), false);
});

test('formatEventLine appends the no-evidence marker for templated content', () => {
  const line = formatEventLine({
    seq: 25,
    type: 'agent.message',
    payload: { text: 'current hypothesis (50%): …', synthesized: false, disclosedAsTemplated: true },
  });
  assert.match(line, /#25 agent\.message/);
  assert.match(line, /\(no evidence — templated, not analysis\)$/);
});

test('formatEventLine carries no marker for ordinary, non-templated content', () => {
  const line = formatEventLine({
    seq: 26,
    type: 'edge.finding',
    payload: { text: 'origin pool unhealthy', displayName: 'Dana' },
  });
  assert.equal(line.includes('no evidence'), false);
});

test('formatEventLine still reads correctly with no payload at all', () => {
  assert.equal(formatEventLine({ seq: 1, type: 'incident.opened' }), '#1 incident.opened');
});

test('eventText is unaffected by the new field — templated content still shows its actual text', () => {
  assert.equal(
    eventText({ text: 'Re: "what are you doing?" — …', synthesized: false, disclosedAsTemplated: true }),
    'Re: "what are you doing?" — …',
  );
});
