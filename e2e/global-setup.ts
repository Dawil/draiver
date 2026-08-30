import { execFileSync } from "node:child_process";
import { rmSync, mkdirSync } from "node:fs";
import path from "node:path";

/**
 * Seeds a deterministic fixture board using the real CLI, so the e2e tests
 * exercise exactly what an agent would produce. Runs before the tests; the
 * read-only server picks up the files on the next request.
 */
export default async function globalSetup() {
  const repoRoot = path.resolve(__dirname, "..");
  const fixtureDir = path.join(__dirname, ".fixture-data");
  const bin = path.join(__dirname, ".bin", "draiver");

  // Build the binary and seed with it (not `go run`, which remaps the exit-3
  // escalation gate to 1 and would hide it from the check below).
  mkdirSync(path.dirname(bin), { recursive: true });
  execFileSync("go", ["build", "-o", bin, "."], { cwd: repoRoot, stdio: "pipe" });

  rmSync(fixtureDir, { recursive: true, force: true });
  mkdirSync(fixtureDir, { recursive: true });

  const draiver = (...args: string[]) => {
    try {
      execFileSync(bin, ["--data", fixtureDir, ...args], {
        stdio: "pipe",
        env: { ...process.env, DRAIVER_ACTOR: "agent:claude-code" },
      });
    } catch (e: any) {
      // `escalate` halts with exit 3 by design — expected, not a seed failure.
      if (e && e.status === 3) return;
      throw e;
    }
  };

  // Attempt creation requires a real git working tree to cut worktrees from
  // (drvctl-021); the fixture points every ticket at this repo. The board is read
  // from the log, so no session is ever cut against it.
  const repo = repoRoot;

  // PROJ-101 attempt 0001 -> Needs me (open escalation), with claude-code.
  draiver("new", "PROJ-101", "--title", "Payment webhook", "--assignee", "dave", "--tool", "claude-code", "--repo", repo);
  draiver("log", "PROJ-101", "Stripe test keys only work in test mode.", "--type", "gotcha");
  draiver("escalate", "PROJ-101", "Which currency rounding rule for JPY?");
  // PROJ-101 attempt 0002 -> Running: a second journey with a different tool, so
  // the same ticket shows twice on the board.
  draiver("attempt", "new", "PROJ-101", "--tool", "aider", "--repo", repo);

  // PROJ-102 -> Review (agent claims done)
  draiver("new", "PROJ-102", "--title", "Search index", "--assignee", "dave", "--repo", repo);
  draiver("log", "PROJ-102", "Chose server-side pagination over client-side.", "--type", "decision");
  draiver("review", "PROJ-102", "PR #142 open; tests green.");

  // PROJ-103 -> Running (freshly created)
  draiver("new", "PROJ-103", "--title", "Nightly report job", "--repo", repo);

  // PROJ-104 -> Pending: enabled + Running with no live agent, and admitted only
  // after PROJ-103 reaches Review (which it has not), so the forward gate holds it
  // out and the board shows the quiet "waiting on PROJ-103" reason. Enabled but
  // never brought up here (no `ctl up`), so no session.json exists and the liveness
  // probe reads dead — exactly the self-resolving wait Pending names (drvweb-015).
  draiver("new", "PROJ-104", "--title", "End-to-end suite", "--repo", repo);
  draiver("depends", "PROJ-104", "--after", "PROJ-103");
  draiver("ctl", "enable", "PROJ-104");
}
