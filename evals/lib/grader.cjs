// grader.cjs — the LLM grader for `llm-rubric` assertions: one tool-less `claude -p`
// call in an empty temp dir. Used only where a deterministic assertion can't express
// the rule (e.g. "does not recommend sleep-polling").
'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const sandbox = require('./sandbox.cjs');

class Grader {
  id() {
    return 'draiver-evals-grader';
  }

  async callApi(prompt) {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'draiver-evals-grade-'));
    try {
      const box = { dir, repo: dir };
      fs.mkdirSync(path.join(dir, 'tmp'));
      const args = ['-p', prompt, '--output-format', 'json', '--no-session-persistence', '--setting-sources', 'project', '--tools', ''];
      args.push('--model', process.env.EVALS_GRADER_MODEL || 'haiku');
      const r = await sandbox.run('claude', args, { cwd: dir, env: sandbox.env(box), timeoutMs: 5 * 60 * 1000 });
      let j;
      try {
        j = JSON.parse(r.stdout);
      } catch {
        return { error: `grader output unparseable (exit ${r.code}): ${(r.stderr || r.stdout).slice(-500)}` };
      }
      const u = j.usage || {};
      return {
        output: String(j.result || ''),
        cost: Number(j.total_cost_usd) || 0,
        tokenUsage: { total: (u.input_tokens || 0) + (u.output_tokens || 0), prompt: u.input_tokens || 0, completion: u.output_tokens || 0, numRequests: 1 },
      };
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  }
}

module.exports = Grader;
