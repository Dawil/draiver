// tasks.cjs — setup and deterministic checks for the task evals (drv-065 E1, E3).
// A setup shapes the throwaway checkout before the agent runs; a check inspects the
// checkout (git state, files, a test command) and the answer after it finishes. Every
// check returns {pass, detail}; assertions in the cases read them from the output JSON.
'use strict';

const fs = require('node:fs');
const path = require('node:path');
const sandbox = require('./sandbox.cjs');

const { git, tryGit } = sandbox;

function write(repo, rel, content) {
  fs.mkdirSync(path.dirname(path.join(repo, rel)), { recursive: true });
  fs.writeFileSync(path.join(repo, rel), content);
}

function append(repo, rel, content) {
  fs.appendFileSync(path.join(repo, rel), content);
}

function commit(repo, msg) {
  git(repo, 'add', '-A');
  git(repo, 'commit', '-q', '-m', msg);
  return git(repo, 'rev-parse', 'HEAD');
}

// changedPaths: every path differing between the working tree and `base` (tracked),
// plus untracked, non-ignored files — i.e. everything the agent added or changed,
// committed or not.
function changedPaths(repo, base) {
  const tracked = tryGit(repo, 'diff', '--name-only', base).split('\n').filter(Boolean);
  const untracked = tryGit(repo, 'ls-files', '--others', '--exclude-standard').split('\n').filter(Boolean);
  return [...new Set([...tracked, ...untracked])];
}

function existsAt(repo, rev, rel) {
  try {
    git(repo, 'cat-file', '-e', `${rev}:${rel}`);
    return true;
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------------
// E1 — add a BDD scenario for a small web change.

const E1 = {
  ticket: 'evl-001',
  template: 'internal/web/templates/board_page.html',
  anchor: 'data-testid="cache-rollup-link"',
  link: '<a class="topbar-link" data-testid="board-ledgers-link" href="/?view=ledgers">Ledgers →</a>',
};

function setupE1(box) {
  const file = path.join(box.repo, E1.template);
  const src = fs.readFileSync(file, 'utf8');
  const lines = src.split('\n');
  const i = lines.findIndex((l) => l.includes(E1.anchor));
  if (i < 0) throw new Error(`E1 setup: anchor ${E1.anchor} not found in ${E1.template} — update evals/lib/tasks.cjs`);
  const indent = lines[i].match(/^\s*/)[0];
  lines.splice(i + 1, 0, indent + E1.link);
  fs.writeFileSync(file, lines.join('\n'));
  box.setupSha = commit(box.repo, `${E1.ticket}: board top bar links to the ledgers view`);
}

// focusGo runs only the given feature files through the real acceptance runner and
// prints the cucumber report as JSON, so the check sees per-scenario status and
// artefacts without climbing the whole acceptance rung.
const focusGo = `package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Dawil/draiver/internal/acceptance"
)

func main() {
	res, err := acceptance.Run(".evals-focus/features", ".evals-focus/out")
	if err != nil {
		fmt.Fprintln(os.Stderr, "focus:", err)
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(res.Report)
}
`;

async function focusedRun(box, features) {
  const dir = path.join(box.repo, '.evals-focus');
  fs.rmSync(dir, { recursive: true, force: true });
  for (const f of features) {
    const dest = path.join(dir, 'features', f.replace(/^features\//, ''));
    fs.mkdirSync(path.dirname(dest), { recursive: true });
    fs.copyFileSync(path.join(box.repo, f), dest);
  }
  fs.writeFileSync(path.join(dir, 'main.go'), focusGo);
  const r = await sandbox.run('go', ['run', './.evals-focus'], { cwd: box.repo, env: sandbox.env(box), timeoutMs: 15 * 60 * 1000 });
  fs.rmSync(dir, { recursive: true, force: true });
  if (r.code !== 0 && !r.stdout.trim()) {
    return { focused: { pass: false, detail: `focused run did not complete (exit ${r.code}): ${r.stderr.slice(-1500)}` } };
  }
  let report;
  try {
    report = JSON.parse(r.stdout);
  } catch {
    return { focused: { pass: false, detail: `focused run printed no report: ${r.stderr.slice(-1500)}` } };
  }
  let scenarios = 0;
  const failed = [];
  const bare = [];
  for (const f of report || []) {
    for (const el of f.elements || []) {
      if (el.type === 'background') continue;
      scenarios++;
      const steps = el.steps || [];
      const bad = steps.find((s) => (s.result || {}).status !== 'passed');
      if (bad) failed.push(`${el.name}: ${bad.keyword || ''}${bad.name} → ${(bad.result || {}).status} ${((bad.result || {}).error_message || '').slice(0, 300)}`);
      if (!steps.some((s) => (s.embeddings || []).length > 0)) bare.push(el.name);
    }
  }
  return {
    focused: {
      pass: scenarios > 0 && failed.length === 0,
      detail: scenarios === 0 ? 'no scenarios ran' : failed.length ? failed.join('\n') : `${scenarios} scenario(s) green`,
    },
    artefactRule: {
      pass: scenarios > 0 && bare.length === 0,
      detail: bare.length ? `scenario(s) attach no artefact: ${bare.join('; ')}` : 'every scenario attaches an artefact',
    },
  };
}

async function checkE1(box) {
  const checks = {};
  const changed = changedPaths(box.repo, box.setupSha);
  const features = changed.filter(
    (p) => p.endsWith('.feature') && fs.existsSync(path.join(box.repo, p)) && fs.readFileSync(path.join(box.repo, p), 'utf8').includes('@' + E1.ticket),
  );
  checks.scenarioAdded = {
    pass: features.length > 0,
    detail: features.length ? features.join(', ') : `no new/changed .feature tagged @${E1.ticket} (changed: ${changed.join(', ') || 'nothing'})`,
  };
  const tpl = fs.existsSync(path.join(box.repo, E1.template)) ? fs.readFileSync(path.join(box.repo, E1.template), 'utf8') : '';
  checks.webChangeIntact = { pass: tpl.includes('board-ledgers-link'), detail: 'the committed web change is still present' };
  const zz = changed.filter((p) => /(^|\/)zz_[^/]*_test\.go$/.test(p) && !existsAt(box.repo, box.setupSha, p));
  checks.noScratchTests = { pass: zz.length === 0, detail: zz.length ? `left behind: ${zz.join(', ')}` : 'no zz_*_test.go left behind' };

  if (features.length) {
    Object.assign(checks, await focusedRun(box, features));
  } else {
    checks.focused = { pass: false, detail: 'no scenario to run' };
    checks.artefactRule = { pass: false, detail: 'no scenario to inspect' };
  }

  const unitCmd = process.env.EVALS_E1_UNIT_CMD || 'go test ./...';
  const u = await sandbox.run('sh', ['-c', unitCmd], { cwd: box.repo, env: sandbox.env(box), timeoutMs: 30 * 60 * 1000 });
  checks.unitRung = {
    pass: u.code === 0,
    detail: u.code === 0 ? `\`${unitCmd}\` green` : `\`${unitCmd}\` exit ${u.code}${u.timedOut ? ' (timed out)' : ''}: ${(u.stdout + u.stderr).split('\n').filter((l) => /FAIL|panic|error/i.test(l)).slice(0, 30).join('\n')}`,
  };
  return checks;
}

// ---------------------------------------------------------------------------------
// E3 — main has moved since the branch was cut.

const E3 = {
  branch: 'draiver/evl-003/0001',
  branchFiles: ['docs/evl-glossary.md', 'README.md'],
  mainOnly: ['docs/evl-main-runbook.md', 'internal/evlmain/evlmain.go', 'docs/mvp.md'],
  // what a two-dot diff would wrongly attribute to the branch, as the answer would name it
  mainOnlyNames: ['evl-main-runbook', 'evlmain', 'mvp.md'],
};

function setupE3(box) {
  const { repo } = box;
  const base = git(repo, 'rev-parse', 'HEAD');
  git(repo, 'checkout', '-q', '-b', E3.branch, base);
  write(repo, 'docs/evl-glossary.md', '# Glossary\n\n- **attempt** — one try at a ticket, on its own branch and worktree.\n- **rung** — one level of the test pyramid.\n');
  append(repo, 'README.md', '\nSee [the glossary](docs/evl-glossary.md) for draiver terms.\n');
  commit(repo, 'evl-003: add a glossary and link it from the README');

  git(repo, 'checkout', '-q', 'main');
  write(repo, 'docs/evl-main-runbook.md', '# Runbook\n\nRestart the board with `draiver webui`.\n');
  commit(repo, 'evl-002: add an operator runbook');
  write(repo, 'internal/evlmain/evlmain.go', '// Package evlmain is unrelated work that landed on main after evl-003 was cut.\npackage evlmain\n\n// Answer is unrelated to evl-003.\nfunc Answer() int { return 42 }\n');
  append(repo, 'docs/mvp.md', '\n<!-- evl-004: revisited after the MVP review. -->\n');
  commit(repo, 'evl-004: evlmain helper; mvp note');
  box.mainSha = git(repo, 'rev-parse', 'HEAD');
  git(repo, 'checkout', '-q', E3.branch);
}

async function checkE3(box, answer) {
  const { repo } = box;
  const checks = {};
  const missing = E3.branchFiles.filter((f) => !answer.includes(path.basename(f)));
  checks.namesBranchFiles = { pass: missing.length === 0, detail: missing.length ? `answer omits: ${missing.join(', ')}` : 'names every branch file' };
  const leaked = E3.mainOnlyNames.filter((n) => answer.includes(n));
  checks.namesOnlyBranchFiles = { pass: leaked.length === 0, detail: leaked.length ? `answer attributes main's work to the branch: ${leaked.join(', ')}` : 'names none of main\'s new work' };

  const mb = tryGit(repo, 'merge-base', 'main', 'HEAD') || box.mainSha;
  const changed = changedPaths(repo, mb);
  const restored = changed.filter((p) => E3.mainOnly.includes(p));
  const reverted = changed.filter((p) => !E3.branchFiles.includes(p) && existsAt(repo, mb, p));
  checks.restoresNothing = {
    pass: restored.length === 0 && reverted.length === 0,
    detail: restored.length || reverted.length ? `restored from main: ${restored.join(', ') || '-'}; base files changed: ${reverted.join(', ') || '-'}` : 'branch diff touches only its own files',
  };
  const kept = E3.branchFiles.filter((f) => changed.includes(f));
  checks.branchWorkKept = { pass: kept.length === E3.branchFiles.length, detail: `branch files still changed: ${kept.join(', ') || 'none'}` };
  const mainNow = tryGit(repo, 'rev-parse', 'refs/heads/main');
  checks.mainUntouched = { pass: mainNow === box.mainSha, detail: mainNow === box.mainSha ? 'main not moved' : `main moved ${box.mainSha.slice(0, 8)} → ${mainNow.slice(0, 8)}` };
  return checks;
}

module.exports = {
  setups: { e1: setupE1, e3: setupE3 },
  checks: { e1: checkE1, e3: checkE3 },
  branches: { e3: E3.branch },
};
