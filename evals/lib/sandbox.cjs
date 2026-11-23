// sandbox.cjs — the isolation half of drv-065's evals: every case runs a headless agent
// in a throwaway clone of this repo at a fixed commit, with its own draiver data root,
// config and cache, and no git remotes. Nothing here may touch the real board, data
// root, branch or remotes; a tripwire (snapshot before, compare after) fails the case
// loudly if the source checkout moved anyway.
'use strict';

const { execFileSync, spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

// The repo under evaluation: the checkout this suite lives in (evals/lib → repo root).
const SOURCE = path.resolve(__dirname, '..', '..');

function git(cwd, ...args) {
  return execFileSync('git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
}

function tryGit(cwd, ...args) {
  try {
    return git(cwd, ...args);
  } catch {
    return '';
  }
}

// snapshot captures what an escaped agent could plausibly damage in the source
// checkout: its HEAD, its working-tree status, and the local main ref. Other attempts'
// branches in the same repo move concurrently, so they are deliberately not compared.
function snapshot() {
  return {
    head: tryGit(SOURCE, 'rev-parse', 'HEAD'),
    status: tryGit(SOURCE, 'status', '--porcelain'),
    main: tryGit(SOURCE, 'rev-parse', '--verify', '-q', 'refs/heads/main'),
  };
}

function compareSnapshots(before, after) {
  const moved = [];
  for (const k of Object.keys(before)) {
    if (before[k] !== after[k]) moved.push(k);
  }
  return moved;
}

// realCacheDirs resolves the caches a case must keep sharing (a cold Go build cache or
// a missing browser would make every task case minutes slower or impossible) BEFORE
// XDG_CACHE_HOME is pointed at the throwaway dir.
function realCacheDirs() {
  let gocache = process.env.GOCACHE || '';
  if (!gocache) {
    try {
      gocache = execFileSync('go', ['env', 'GOCACHE'], { encoding: 'utf8' }).trim();
    } catch {
      gocache = '';
    }
  }
  const xdg = process.env.XDG_CACHE_HOME || path.join(os.homedir(), '.cache');
  return {
    GOCACHE: gocache,
    PLAYWRIGHT_BROWSERS_PATH: process.env.PLAYWRIGHT_BROWSERS_PATH || path.join(xdg, 'ms-playwright'),
  };
}

// create clones SOURCE at ref into a fresh temp dir, checks it out on `branch`, strips
// every remote, and links the source's Playwright node_modules so BDD steps can shoot.
function create({ ref = process.env.EVALS_REF || 'HEAD', branch = 'evals/case' } = {}) {
  const sha = git(SOURCE, 'rev-parse', '--verify', `${ref}^{commit}`);
  const dir = fs.mkdtempSync(path.join(sandboxRoot(), 'case-'));
  const repo = path.join(dir, 'repo');
  execFileSync('git', ['clone', '-q', '--no-checkout', SOURCE, repo], { stdio: 'ignore' });
  for (const r of git(repo, 'remote').split('\n').filter(Boolean)) {
    git(repo, 'remote', 'remove', r);
  }
  git(repo, 'checkout', '-q', '-B', branch, sha);
  git(repo, 'config', 'user.email', 'evals@draiver.invalid');
  git(repo, 'config', 'user.name', 'draiver evals');
  // main must exist in the clone (cases compare against it); pin it to the case's base.
  git(repo, 'branch', '-f', 'main', sha);

  const nm = path.join(SOURCE, 'features', 'screenshots', 'node_modules');
  if (fs.existsSync(nm)) {
    fs.symlinkSync(nm, path.join(repo, 'features', 'screenshots', 'node_modules'));
    // keep the symlink out of `git status` so it never reads as agent work
    fs.appendFileSync(path.join(repo, '.git', 'info', 'exclude'), '/features/screenshots/node_modules\n');
  }
  fs.mkdirSync(path.join(dir, 'data'));
  fs.mkdirSync(path.join(dir, 'cache'));
  fs.mkdirSync(path.join(dir, 'tmp'));
  return { dir, repo, sha, branch };
}

// sandboxRoot is where throwaway clones go: EVALS_SANDBOX_DIR, else ~/.cache/draiver-evals.
// Deliberately NOT os.tmpdir() — /tmp is often a small tmpfs, and a task case's clone
// plus its `go test ./...` temp files run to gigabytes — and NOT inside the source
// checkout, where an agent's upward CLAUDE.md/AGENTS.md discovery would read the real
// repo's context instead of the clone's.
function sandboxRoot() {
  const root = process.env.EVALS_SANDBOX_DIR || path.join(os.homedir(), '.cache', 'draiver-evals');
  fs.mkdirSync(root, { recursive: true });
  return root;
}

// env builds the agent's environment: the caller's, minus everything that ties it to a
// supervising draiver/Claude session (so it works when the rung is run from inside one,
// and cannot message or log into the parent), plus a throwaway data root/config/cache.
function env(box) {
  const out = {};
  const keepClaude = new Set(['CLAUDE_CONFIG_DIR', 'CLAUDE_CODE_OAUTH_TOKEN', 'CLAUDE_CODE_USE_BEDROCK', 'CLAUDE_CODE_USE_VERTEX']);
  for (const [k, v] of Object.entries(process.env)) {
    if (k.startsWith('DRAIVER_')) continue;
    if (k === 'CLAUDECODE' || k === 'CLAUDE_PID') continue;
    if (k.startsWith('CLAUDE_CODE_') && !keepClaude.has(k)) continue;
    if (k.startsWith('PROMPTFOO_')) continue;
    out[k] = v;
  }
  return {
    ...out,
    ...realCacheDirs(),
    DRAIVER_DATA: path.join(box.dir, 'data'),
    DRAIVER_CONFIG: path.join(box.dir, 'config.json'),
    DRAIVER_ACTOR: 'agent:evals',
    XDG_CACHE_HOME: path.join(box.dir, 'cache'),
    TMPDIR: path.join(box.dir, 'tmp'),
    GIT_TERMINAL_PROMPT: '0',
  };
}

function destroy(box) {
  if (process.env.EVALS_KEEP === '1') return;
  fs.rmSync(box.dir, { recursive: true, force: true });
}

// Children run in their own process group (so a timeout can kill the agent AND every
// build/test it started). That also means they would outlive promptfoo if it is
// stopped — e.g. a cancelled `draiver test evals --only` — so kill every live group on
// exit or on a terminating signal, then let the signal take its normal course.
const live = new Set();

function killLive() {
  for (const pid of live) {
    try {
      process.kill(-pid, 'SIGKILL');
    } catch {
      /* already gone */
    }
  }
  live.clear();
}

process.on('exit', killLive);
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.once(sig, () => {
    killLive();
    process.kill(process.pid, sig);
  });
}

// run spawns a command in the sandbox and resolves {code, stdout, stderr, timedOut};
// it never rejects, so a case's checks always get to report.
function run(cmd, args, { cwd, env: e, timeoutMs = 20 * 60 * 1000, input } = {}) {
  return new Promise((resolve) => {
    const child = spawn(cmd, args, { cwd, env: e, stdio: ['pipe', 'pipe', 'pipe'], detached: true });
    if (child.pid) live.add(child.pid);
    let stdout = '';
    let stderr = '';
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      try {
        process.kill(-child.pid, 'SIGKILL');
      } catch {
        /* already gone */
      }
    }, timeoutMs);
    child.stdout.on('data', (d) => (stdout += d));
    child.stderr.on('data', (d) => (stderr += d));
    child.on('error', (err) => {
      clearTimeout(timer);
      live.delete(child.pid);
      resolve({ code: -1, stdout, stderr: stderr + String(err), timedOut });
    });
    child.on('close', (code) => {
      clearTimeout(timer);
      // reap anything the command left running in its group (an agent's background
      // shell, a stray test binary) before the checks inspect the checkout
      try {
        process.kill(-child.pid, 'SIGKILL');
      } catch {
        /* group already empty */
      }
      live.delete(child.pid);
      resolve({ code, stdout, stderr, timedOut });
    });
    child.stdin.end(input || '');
  });
}

module.exports = { SOURCE, git, tryGit, snapshot, compareSnapshots, create, env, destroy, run };
