#!/bin/sh
# review-env/up.sh — bring up a REAL draiver webui (drv-020, resolution #51) over a
# throwaway data dir, so a reviewer can click around the UI changes under review without
# touching — or clashing with — the operator's own running draiver.
#
# It IS the UI under review: the webui is built FROM this detached review worktree (the
# reviewed commit), so the instance a reviewer drives reflects exactly the change.
#
# Isolation (the "must not clash with the current running environment" requirement):
#   - binds the draiver-allocated DRAIVER_REVIEW_PORT, never the default 7777;
#   - exports DRAIVER_DATA / DRAIVER_CONFIG → a per-instance .review-env-run/ work area
#     inside this worktree, so every board write lands in dummy data, never in
#     ~/.draiver. Concurrent review envs each own a worktree ⇒ own work area ⇒ no
#     collision; draiver force-removes the worktree on teardown, discarding it all.
#
# ENV_NAME — the web-UI-settable param (resolution #45) draiver injects alongside
# DRAIVER_REVIEW_INSTANCE/PORT — titles the seeded dummy tickets, so the custom value
# visibly parameterises the running instance.
set -e
cd "$(dirname "$0")/.."            # worktree root (go.mod lives here)

RUN=.review-env-run
DATA="$PWD/$RUN/data"
CONFIG="$PWD/$RUN/config.json"
BIN="$PWD/$RUN/draiver"
LOG="$PWD/$RUN/webui.log"
PID="$PWD/$RUN/webui.pid"
ENV_NAME="${ENV_NAME:-review}"

mkdir -p "$DATA"
echo '{}' >"$CONFIG"

# Build the webui from the reviewed commit, so the running instance is the change.
go build -o "$BIN" .

export DRAIVER_DATA="$DATA"
export DRAIVER_CONFIG="$CONFIG"

# Seed a little dummy data across the board's states so there is something to click.
# Best-effort: the webui must still come up over an empty board if a seed hiccups, and
# `escalate` exits non-zero (it signals a block) so it must not trip `set -e`.
"$BIN" new    DUMMY-1 --title "$ENV_NAME: running ticket" --repo "$PWD" --base main >/dev/null 2>&1 || true
"$BIN" new    DUMMY-2 --title "$ENV_NAME: in review"      --repo "$PWD" --base main >/dev/null 2>&1 || true
"$BIN" review DUMMY-2 "seeded review claim for $ENV_NAME" --url "http://example.test/compare/main...dummy" >/dev/null 2>&1 || true
"$BIN" new    DUMMY-3 --title "$ENV_NAME: needs a human"  --repo "$PWD" --base main >/dev/null 2>&1 || true
"$BIN" escalate DUMMY-3 "seeded question for $ENV_NAME?" >/dev/null 2>&1 || true

# drv-045: seed the in-review DUMMY-2 attempt's per-attempt system-event journal
# (session/system.jsonl) so the reviewer sees a POPULATED System Logs panel live — the
# new second streaming panel, separate from Agent Logs — not just a green unit test.
# One record per operator/daemon source (provenance edit, git control, review-env
# up/down, promotion check) plus a draiverctld lifecycle line, each carrying its OWN
# recorded ts in the (deliberately fixed, past) order the board renders — proving the
# panel stamps lines with their recorded time, never render-time now(). system.jsonl is
# rebuildable runtime state written directly at the action site, so seeding it is just
# writing the JSON lines the handlers/reconciler would have appended. Best-effort like
# every seed above (the webui must still come up if this hiccups); the dummy attempt is
# 0001, matching the promotions block below.
DUMMY2_SESSION="$DATA/DUMMY-2/attempts/0001/session"
mkdir -p "$DUMMY2_SESSION" 2>/dev/null && cat >"$DUMMY2_SESSION/system.jsonl" <<'SYS' 2>/dev/null || true
{"ts":"2026-11-03T09:00:00Z","class":"ctl-session","message":"ctl: session started — worktree cut for DUMMY-2/0001","fields":{"actor":"draiverctld"}}
{"ts":"2026-11-03T09:05:12Z","class":"provenance","message":"provenance updated: base `master` → `main`","fields":{"from":"master","to":"main","actor":"human:review"}}
{"ts":"2026-11-03T09:07:33Z","class":"git","message":"git push: ok — pushed draiver/DUMMY-2/0001","fields":{"verb":"push","outcome":"ok"}}
{"ts":"2026-11-03T09:09:48Z","class":"review-env-up","message":"review-env up: `review` → starting","fields":{"env":"review"}}
{"ts":"2026-11-03T09:14:21Z","class":"promotion-check","message":"promotion check: `staging` @ `abc1234` — pass","fields":{"env":"staging","commit":"abc1234","verdict":"pass"}}
{"ts":"2026-11-03T09:18:02Z","class":"review-env-down","message":"review-env down: `review` → down","fields":{"env":"review"}}
SYS
# drv-039: a couple of `type: codebase` ledgers so the reviewer can see the new ledger
# rendering — a resting one (gated to Parked by drv-037, so it only shows in the Ledgers
# view) and an escalated one (Needs-me, surfacing into the code view and lighting the
# Ledgers attention dot). Exercises both the default-hidden and the attention path.
"$BIN" new    LEDGER-1 --title "$ENV_NAME: resting codebase ledger" --repo "$PWD" --base main --type codebase >/dev/null 2>&1 || true
"$BIN" new    LEDGER-2 --title "$ENV_NAME: ledger needs a human"    --repo "$PWD" --base main --type codebase >/dev/null 2>&1 || true
"$BIN" escalate LEDGER-2 "which trunk is authoritative for $ENV_NAME?" >/dev/null 2>&1 || true

# drv-043/drv-046: two Review tickets carrying real promotion-results so the reviewer
# sees the board card's INTERACTIVE promotion controls live — the per-environment chips
# (drv-046 makes them clickable, grey until first run) and the all-green "mark done" tick.
# draiver only VERIFIES a promotion (runs the repo's `qa`); it never performs one. Each
# fixture is a tiny throwaway repo whose committed `.test-pyramid.yaml` declares its
# promotions, checked out ON the attempt branch so the board's no-live-worktree fold
# (branchPyramidState) reads the pyramid and HEAD from draiver/<ticket>/0001.
#
#   DUMMY-PROMO       — the MIXED card: dev GREEN at HEAD, staging FAILED (red), prod
#                       NEVER RUN (grey not-run). So a reviewer sees grey+green+red chips
#                       and, because not all are green, NO mark-done tick.
#   DUMMY-PROMO-GREEN — the ALL-GREEN card: dev+staging both green at HEAD, so the live
#                       "mark done" tick appears and the ticket can be ratified to Done
#                       straight from the board.
#
# A fail is recorded only by the web handler (`draiver promote --log` refuses a failed qa
# and writes nothing), so staging's red verdict is seeded AFTER the webui is up, by
# POSTing the drv-046 board promote route against the live board (see the readiness loop
# below) — which also dogfoods the exact path the chip click drives. Best-effort like
# every seed above; the webui must still come up if any step hiccups.
PROMO="$PWD/$RUN/promorepo"
PROMO_GREEN="$PWD/$RUN/promorepo-green"
if command -v git >/dev/null 2>&1; then
  mkdir -p "$PROMO"
  cat >"$PROMO/.test-pyramid.yaml" <<'PY'
environments:
  - name: dev
    healthchecks:
      - name: ready
        script: "true"
  - name: staging
    healthchecks:
      - name: ready
        script: "true"
  - name: prod
    healthchecks:
      - name: ready
        script: "true"
levels:
  - name: unit
    run: "true"
promotions:
  - environment: dev
    base: main
    qa: "true"
  - environment: staging
    base: main
    qa: "false"
  - environment: prod
    base: main
    qa: "true"
PY
  git -C "$PROMO" init -q -b draiver/DUMMY-PROMO/0001 >/dev/null 2>&1 || true
  git -C "$PROMO" config user.email review-env@example.test >/dev/null 2>&1 || true
  git -C "$PROMO" config user.name  "review-env"            >/dev/null 2>&1 || true
  git -C "$PROMO" add .test-pyramid.yaml >/dev/null 2>&1 || true
  git -C "$PROMO" commit -q -m "promotions fixture" >/dev/null 2>&1 || true
  "$BIN" new    DUMMY-PROMO --title "$ENV_NAME: promotions (mixed)" --repo "$PROMO" --base main >/dev/null 2>&1 || true
  "$BIN" review DUMMY-PROMO "seeded promotions for $ENV_NAME" --url "http://example.test/compare/main...dummy" >/dev/null 2>&1 || true
  # dev is verified at HEAD → green-at-HEAD. staging (qa false) is recorded red by the
  # live board after launch; prod is left un-run → grey not-run chip.
  ( cd "$PROMO" && DRAIVER_ATTEMPT=0001 "$BIN" promote dev --log ) >/dev/null 2>&1 || true

  # The all-green companion: both promotions pass at HEAD, so the mark-done tick lights.
  mkdir -p "$PROMO_GREEN"
  cat >"$PROMO_GREEN/.test-pyramid.yaml" <<'PY'
environments:
  - name: dev
    healthchecks:
      - name: ready
        script: "true"
  - name: staging
    healthchecks:
      - name: ready
        script: "true"
levels:
  - name: unit
    run: "true"
promotions:
  - environment: dev
    base: main
    qa: "true"
  - environment: staging
    base: main
    qa: "true"
PY
  git -C "$PROMO_GREEN" init -q -b draiver/DUMMY-PROMO-GREEN/0001 >/dev/null 2>&1 || true
  git -C "$PROMO_GREEN" config user.email review-env@example.test >/dev/null 2>&1 || true
  git -C "$PROMO_GREEN" config user.name  "review-env"            >/dev/null 2>&1 || true
  git -C "$PROMO_GREEN" add .test-pyramid.yaml >/dev/null 2>&1 || true
  git -C "$PROMO_GREEN" commit -q -m "promotions fixture (all green)" >/dev/null 2>&1 || true
  "$BIN" new    DUMMY-PROMO-GREEN --title "$ENV_NAME: promotions (all green)" --repo "$PROMO_GREEN" --base main >/dev/null 2>&1 || true
  "$BIN" review DUMMY-PROMO-GREEN "seeded all-green promotions for $ENV_NAME" --url "http://example.test/compare/main...dummy" >/dev/null 2>&1 || true
  ( cd "$PROMO_GREEN" && DRAIVER_ATTEMPT=0001 "$BIN" promote dev     --log ) >/dev/null 2>&1 || true
  ( cd "$PROMO_GREEN" && DRAIVER_ATTEMPT=0001 "$BIN" promote staging --log ) >/dev/null 2>&1 || true
fi

# Launch the webui backgrounded with output redirected (so draiver's CombinedOutput does
# not block on the inherited pipe), record its pid for down.sh, then wait until the port
# actually answers so confirm-up never races bring-up.
"$BIN" webui --addr "127.0.0.1:$DRAIVER_REVIEW_PORT" >"$LOG" 2>&1 &
echo $! >"$PID"

i=0
while [ "$i" -lt 150 ]; do
  if node -e "require('net').connect(Number(process.env.DRAIVER_REVIEW_PORT),'127.0.0.1').on('connect',function(){process.exit(0)}).on('error',function(){process.exit(1)})" 2>/dev/null; then
    # drv-046: record DUMMY-PROMO's staging RED verdict via the live board — the only
    # path that records a fail (the CLI refuses a failed qa) — so the mixed card shows a
    # red chip. This drives the exact drv-046 board promote route (?surface=card) a chip
    # click uses, dogfooding it; same-origin with no Origin header passes the guard.
    # Best-effort: a hiccup here must never stop the env from coming up.
    node -e "const http=require('http');const req=http.request({host:'127.0.0.1',port:Number(process.env.DRAIVER_REVIEW_PORT),path:'/ticket/DUMMY-PROMO/0001/promote/staging?surface=card',method:'POST'},function(r){r.resume();r.on('end',function(){process.exit(0)})});req.on('error',function(){process.exit(0)});req.end();" 2>/dev/null || true
    echo "up: draiver webui listening on $DRAIVER_REVIEW_PORT (ENV_NAME=$ENV_NAME, data=$DATA)"
    exit 0
  fi
  i=$((i + 1))
  sleep 0.1
done

echo "up: webui never listened on $DRAIVER_REVIEW_PORT" >&2
cat "$LOG" >&2 || true
exit 1
