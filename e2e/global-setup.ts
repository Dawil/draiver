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

  // PROJ-101 -> Needs me (open escalation)
  draiver("new", "PROJ-101", "--title", "Payment webhook", "--assignee", "dave");
  draiver("log", "PROJ-101", "Stripe test keys only work in test mode.", "--type", "gotcha");
  draiver("escalate", "PROJ-101", "Which currency rounding rule for JPY?");

  // PROJ-102 -> Review (agent claims done)
  draiver("new", "PROJ-102", "--title", "Search index", "--assignee", "dave");
  draiver("log", "PROJ-102", "Chose server-side pagination over client-side.", "--type", "decision");
  draiver("review", "PROJ-102", "PR #142 open; tests green.");

  // PROJ-103 -> Running (freshly created)
  draiver("new", "PROJ-103", "--title", "Nightly report job");
}
