// provider.cjs — promptfoo custom provider for drv-065's agent-context evals.
//
// One call = one case: create a throwaway clone at EVALS_REF (default HEAD) → run the
// case's setup → run the headless agent on the prompt → run the case's deterministic
// checks → tear the clone down. The output is a JSON verdict the cases' assertions
// read; token/cost accounting goes to promptfoo so its report aggregates it.
//
// Case vars: `setup` / `check` name an entry in tasks.cjs (omit both for a knowledge
// check), `tools` is `read` (default) or `write`, `timeout_s` caps the agent turn.
'use strict';

const agent = require('./agent.cjs');
const sandbox = require('./sandbox.cjs');
const tasks = require('./tasks.cjs');

class AgentContextProvider {
  constructor(options = {}) {
    this.options = options;
  }

  id() {
    return this.options.id || 'draiver-agent-context';
  }

  async callApi(prompt, context = {}) {
    const vars = context.vars || {};
    const before = sandbox.snapshot();
    let box;
    try {
      box = sandbox.create({ branch: vars.branch || 'evals/case' });
      if (vars.setup) {
        const setup = tasks.setups[vars.setup];
        if (!setup) throw new Error(`unknown setup ${vars.setup}`);
        setup(box);
      }
      const timeoutMs = (Number(vars.timeout_s) || 900) * 1000;
      const a = await agent.ask(box, prompt, { tools: vars.tools, timeoutMs });
      let checks = {};
      if (vars.check) {
        const check = tasks.checks[vars.check];
        if (!check) throw new Error(`unknown check ${vars.check}`);
        checks = await check(box, a.answer);
      }
      const moved = sandbox.compareSnapshots(before, sandbox.snapshot());
      const verdict = {
        answer: a.answer,
        checks,
        isolation: { ok: moved.length === 0, moved },
        agent: { exit: a.exit, timedOut: a.timedOut, turns: a.turns, cost: a.cost, tokens: a.tokens.total, denials: a.denials, error: a.error },
        ref: box.sha,
      };
      if (moved.length) {
        // Never let a breach pass as a green answer: it is an error row, whatever the
        // agent said. (Committing or adding files in the checkout mid-run trips it too.)
        return { error: `ISOLATION BREACH — the source checkout changed during the case (${moved.join(', ')}). Verdict: ${JSON.stringify(verdict)}` };
      }
      return {
        output: JSON.stringify(verdict),
        tokenUsage: { total: a.tokens.total, prompt: a.tokens.prompt, completion: a.tokens.completion, cached: a.tokens.cached, numRequests: 1 },
        cost: a.cost,
        metadata: { turns: a.turns, ref: box.sha },
      };
    } catch (err) {
      // An infra fault (clone, setup, unknown case) is a promptfoo *error*, distinct
      // from the agent failing its checks.
      return { error: `case harness failed: ${err && err.stack ? err.stack : err}` };
    } finally {
      if (box) sandbox.destroy(box);
    }
  }
}

module.exports = AgentContextProvider;
