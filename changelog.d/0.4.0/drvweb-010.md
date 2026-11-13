---
section: Added
---
**"Merged elsewhere — close & pull" review action (drvweb-010).** A third
review action joins Decision and Done on the attempt page —
**"Merged elsewhere — close & pull `<base>`"** — the board affordance for a PR
landed on the forge. It is shown only for a Review attempt that records **both**
a base and a repo, and `POST /ticket/{id}/{attempt}/merge-remote` shells
**`draiver ctl merge --remote`** (drvctl-029): the CLI owns the `git fetch` +
containment proof + `done` and the best-effort local fast-forward, so the web
layer never touches git, per the package write invariant. Unlike the other web
writes, the handler returns the verb's **combined stdout** on success — the
best-effort pull's result or warning is printed there, not recorded in the
`done` event — and pairs the attempt-live fragment (the log region flips to
Done, OOB badge/count) with an OOB result banner carrying that message. A
zero-exit-with-warning (e.g. a dirty or diverged local base that skipped the
pull) is treated as success and displayed; a nonzero exit (a failed containment
or fetch) surfaces as a 500 like the other writes.
