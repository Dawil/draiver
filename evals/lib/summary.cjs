// summary.cjs — render promptfoo's JSON report as a small Markdown table: pass rate,
// turns, tokens and cost per case, plus totals. This is what a before/after comparison
// (docs/evals.md) diffs, and what gets logged on drv-055.
'use strict';

const fs = require('node:fs');

const file = process.argv[2];
const report = JSON.parse(fs.readFileSync(file, 'utf8'));
const rows = (report.results && report.results.results) || [];

const byCase = new Map();
for (const r of rows) {
  const name = (r.testCase && r.testCase.description) || `test ${r.testIdx}`;
  const c = byCase.get(name) || { n: 0, pass: 0, errors: 0, turns: 0, tokens: 0, cost: 0 };
  c.n++;
  if (r.success) c.pass++;
  if (r.error && !r.gradingResult) c.errors++;
  let v = null;
  try {
    v = JSON.parse((r.response && r.response.output) || '');
  } catch {
    /* error rows have no verdict */
  }
  c.turns += (v && v.agent && v.agent.turns) || 0;
  c.tokens += (r.response && r.response.tokenUsage && r.response.tokenUsage.total) || 0;
  c.cost += (r.response && r.response.cost) || 0;
  byCase.set(name, c);
}

const pct = (a, b) => (b ? `${((100 * a) / b).toFixed(0)}%` : '-');
const k = (n) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(0)}K` : String(n));
const lines = [];
const ref = rows.find((r) => r.response && r.response.metadata && r.response.metadata.ref);
lines.push(`# Agent-context evals${ref ? ` @ ${ref.response.metadata.ref.slice(0, 12)}` : ''}`);
lines.push('');
lines.push('| case | pass | errors | avg turns | avg tokens | cost |');
lines.push('|---|---|---|---|---|---|');
let n = 0, pass = 0, cost = 0, tokens = 0;
for (const [name, c] of byCase) {
  lines.push(`| ${name} | ${c.pass}/${c.n} (${pct(c.pass, c.n)}) | ${c.errors} | ${(c.turns / c.n).toFixed(1)} | ${k(Math.round(c.tokens / c.n))} | $${c.cost.toFixed(2)} |`);
  n += c.n; pass += c.pass; cost += c.cost; tokens += c.tokens;
}
lines.push(`| **total** | **${pass}/${n} (${pct(pass, n)})** | | | ${k(tokens)} | **$${cost.toFixed(2)}** |`);
const thr = report.config && report.config.env && report.config.env.PROMPTFOO_PASS_RATE_THRESHOLD;
if (thr) lines.push('', `Threshold: ${thr}% — ${n && (100 * pass) / n >= Number(thr) ? 'met' : 'NOT met'}.`);
process.stdout.write(lines.join('\n') + '\n');
