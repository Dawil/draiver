#!/bin/sh
# evals/run.sh — the `evals` rung's run: (drv-065). Runs the promptfoo suite and writes
# its report to evals/_artifacts/ (the rung's declared artifacts path). The exit code is
# promptfoo's: non-zero when the pass rate is below the threshold in promptfooconfig.yaml.
#
# Knobs (all optional):
#   EVALS_REPEAT=N         repeats per case (config default: 5)
#   EVALS_FILTER=k=v       only cases whose metadata matches, e.g. kind=knowledge, ticket=drv-059,
#                          weight=light (everything but the heavy E1)
#   EVALS_REF=<rev>        evaluate the agent context at <rev> instead of HEAD
#   EVALS_MODEL=<model>    agent model (default: the claude CLI's default)
#   EVALS_GRADER_MODEL=<m> llm-rubric grader model (default: haiku)
#   EVALS_MAX_BUDGET_USD=x per-case agent spend cap
#   EVALS_E1_UNIT_CMD=...  E1's unit-rung command (default: go test ./...)
#   EVALS_KEEP=1           keep each case's throwaway clone for debugging
set -u
cd "$(dirname "$0")"

art=_artifacts
rm -rf "$art" && mkdir -p "$art"

# promptfoo's own state (sqlite DB, logs) goes to a throwaway dir, never ~/.promptfoo.
PROMPTFOO_CONFIG_DIR=$(mktemp -d "${TMPDIR:-/tmp}/draiver-evals-promptfoo-XXXXXX")
export PROMPTFOO_CONFIG_DIR
# promptfoo drops a request after 300s in its scheduler queue by default; a 20-minute E1
# holding both slots would error every row queued behind it. 0 disables the queue timeout.
export PROMPTFOO_SCHEDULER_QUEUE_TIMEOUT_MS="${PROMPTFOO_SCHEDULER_QUEUE_TIMEOUT_MS:-0}"
export PROMPTFOO_DISABLE_TELEMETRY=1 PROMPTFOO_DISABLE_UPDATE=1 PROMPTFOO_DISABLE_SHARING=1
trap 'rm -rf "$PROMPTFOO_CONFIG_DIR"' EXIT

set -- eval -c promptfooconfig.yaml --no-cache --no-write --no-share --no-table \
  -o "$art/report.json" "$art/report.html"
[ -n "${EVALS_REPEAT:-}" ] && set -- "$@" --repeat "$EVALS_REPEAT"
[ -n "${EVALS_FILTER:-}" ] && set -- "$@" --filter-metadata "$EVALS_FILTER"

node_modules/.bin/promptfoo "$@"
rc=$?

if [ -f "$art/report.json" ]; then
  node lib/summary.cjs "$art/report.json" | tee "$art/summary.md"
fi
exit $rc
