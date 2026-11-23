// assert.cjs — promptfoo javascript assertions over the provider's JSON verdict.
'use strict';

function verdict(output) {
  try {
    return JSON.parse(output);
  } catch {
    return null;
  }
}

// allChecks: every deterministic task check passed; the reason lists each verdict so a
// failing row in the report says exactly what went wrong.
function allChecks(output) {
  const v = verdict(output);
  if (!v) return { pass: false, score: 0, reason: 'provider output is not a verdict' };
  const entries = Object.entries(v.checks || {});
  if (entries.length === 0) return { pass: false, score: 0, reason: `no checks ran${v.agent && v.agent.error ? ': ' + v.agent.error : ''}` };
  const failed = entries.filter(([, c]) => !c.pass);
  const lines = entries.map(([k, c]) => `${c.pass ? '✓' : '✗'} ${k}: ${c.detail}`);
  return { pass: failed.length === 0, score: (entries.length - failed.length) / entries.length, reason: lines.join('\n') };
}

module.exports = { allChecks };
