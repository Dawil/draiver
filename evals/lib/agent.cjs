// agent.cjs — runs one headless agent turn (`claude -p`) inside a sandbox and returns
// its answer plus the turn/token/cost accounting Claude's JSON output exposes.
'use strict';

const sandbox = require('./sandbox.cjs');

const TOOLSETS = {
  // knowledge checks read the agent context; they have no business editing it
  read: ['Read', 'Glob', 'Grep', 'Bash'],
  // task checks change the throwaway checkout
  write: ['Read', 'Glob', 'Grep', 'Bash', 'Edit', 'Write'],
};

function claudeArgs(prompt, { tools = 'read', model = process.env.EVALS_MODEL } = {}) {
  const toolset = TOOLSETS[tools] || TOOLSETS.read;
  const args = [
    '-p', prompt,
    '--output-format', 'json',
    '--no-session-persistence',
    // Evaluate THIS repo's agent context: project settings/plugins only, never the
    // operator's user-level settings, hooks or plugins.
    '--setting-sources', 'project,local',
    '--permission-mode', 'acceptEdits',
    '--tools', toolset.join(','),
    '--allowedTools', ...toolset,
  ];
  if (model) args.push('--model', model);
  if (process.env.EVALS_MAX_BUDGET_USD) args.push('--max-budget-usd', process.env.EVALS_MAX_BUDGET_USD);
  return args;
}

// ask runs the agent in box.repo. It never throws: a crash, timeout or unparseable
// output comes back as an answer of '' with the reason in `error`, so the case fails
// its assertions instead of erroring the whole eval.
async function ask(box, prompt, { tools, timeoutMs } = {}) {
  const res = await sandbox.run('claude', claudeArgs(prompt, { tools }), {
    cwd: box.repo,
    env: sandbox.env(box),
    timeoutMs,
  });
  const out = {
    answer: '',
    exit: res.code,
    timedOut: res.timedOut,
    turns: null,
    cost: 0,
    tokens: { total: 0, prompt: 0, completion: 0, cached: 0 },
    denials: 0,
    error: '',
  };
  let j;
  try {
    j = JSON.parse(res.stdout);
  } catch {
    out.error = `unparseable agent output (exit ${res.code}${res.timedOut ? ', timed out' : ''}): ${(res.stderr || res.stdout).slice(-2000)}`;
    return out;
  }
  out.answer = typeof j.result === 'string' ? j.result : '';
  out.turns = j.num_turns ?? null;
  out.cost = Number(j.total_cost_usd) || 0;
  const u = j.usage || {};
  const cached = (u.cache_read_input_tokens || 0) + (u.cache_creation_input_tokens || 0);
  out.tokens = {
    prompt: (u.input_tokens || 0) + cached,
    completion: u.output_tokens || 0,
    cached,
    total: (u.input_tokens || 0) + cached + (u.output_tokens || 0),
  };
  out.denials = Array.isArray(j.permission_denials) ? j.permission_denials.length : 0;
  if (j.is_error) out.error = `agent reported an error: ${j.subtype || ''} ${out.answer}`.trim();
  return out;
}

module.exports = { ask, claudeArgs };
