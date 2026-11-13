---
section: Added
---
**Flow-metrics grid on the board, with an age-sort toggle on Oldest
(drvweb-027).** A borderless 2x2 grid floats at the top right of the board, level
with the Draiver header, showing four flow metrics, each with a hover `(i)` info
icon: **Oldest** (age and name of the oldest unresolved code ticket), **Expected
Dev-Cycle** (p85, nearest-rank, of resolved created→done durations), **Total
Unresolved**, and **4-Week Throughput** (tickets done within the last 28 days).
The metrics (`internal/web/metrics.go`) are a pure projection over code tickets —
codebase ledgers are excluded — computed against an injectable clock (`s.now`) so
they are unit-testable; the grid lives inside the polled `/board` region so the 3s
poll refreshes it, and falls back to static flow on narrow screens. A ticket that
reached Done and was then archived still counts as resolved (Dev-Cycle,
Throughput); only an archived ticket that never reached Done counts nowhere.
Clicking **Oldest** toggles a per-viewer age sort: `POST /board/sort` flips a
`draiver_sort` cookie read on every render, so the order survives the poll.
`applyFaves` became `applyBoardOrder` — favourites still sort first, then within
each partition the age sort orders oldest-created first (else alphabetical), so it
composes with drv-050 favourites. The pressed state shows as an accent-tinted,
underlined tile. BDD coverage lives in `features/flow-metrics/flow-metrics.feature`
(`@drvweb-027`) with screenshots.
