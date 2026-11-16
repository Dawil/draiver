---
section: Added
---
**Session-liveness dot on dashboard cards (`draiver webui`, drvweb-001).** Each
attempt card gains a coloured runtime-liveness dot: eucalypt green (agent
running — live pid), wattle gold (stopped — session exists, dead pid, still
enabled), ghost-gum grey (disabled — dead pid, not enabled). A never-run or a
Stuck attempt shows no dot (its rust-red signals already carry it). The
read-only web server does its own signal-0 pid probe (mirroring
`reconcile.OSProc.Alive`) and reads `session.json` directly — never
`session.Open`, which would `MkdirAll` a session dir per attempt. The
Australian-bush colours are centralised in `internal/web/palette.go`: favicon
SVGs stay static behind a drift test, and the dot colours reach the browser as
`:root --dot-*` custom properties rendered from the constants, so `style.css`
carries no dot hex.
