# Prompt caching and the 1-hour TTL

Draiver leans hard on Anthropic's **prompt cache**: every session on a repo shares
a long, stable prefix (system prompt, tool definitions, the repo's shape). Reusing
that cached prefix instead of re-reading it each turn is most of why running a
fleet is affordable. The risk is not a cold *first* turn — that is unavoidable —
but the prefix going **cold between uses** and having to be paid for again.

## When the prefix goes cold

The cache entry has a **time-to-live**. Two gaps in draiver routinely exceed the
default TTL:

- **Between tickets on a repo.** The shared per-repo prefix sits idle from the end
  of one attempt to the start of the next.
- **A session parked on Needs-me.** An attempt that escalated (`draiver escalate`)
  waits on a human, who resolves on their own time — often far longer than any
  cache TTL.

Across either gap, a short default TTL means the next turn re-reads and re-pays for
the whole prefix.

## The TTL depends on auth mode — so we pin it

The TTL is not a single fixed number; it depends on how the `claude` process is
authenticated and on account state:

| Auth mode                     | Default TTL | In usage-credit overage |
| ----------------------------- | ----------- | ----------------------- |
| Claude.ai subscription (OAuth) | 1 hour (automatic) | **silently drops to 5m** |
| API key (`sk-ant-…`)          | 5 minutes   | 5 minutes               |

Setting **`ENABLE_PROMPT_CACHING_1H=1`** in the session environment holds the 1h
TTL in *all* of these cases — including overage on a subscription and on an API
key. It is a belt-and-suspenders opt-in, not a redundant no-op, precisely because
the subscription default silently degrades.

### Where it is set

`newReconciler` in `cmd/ctl.go` puts it on the daemon-level `BaseSpec.Env`:

```go
BaseSpec: agent.SessionSpec{
    ...
    Env: []string{"ENABLE_PROMPT_CACHING_1H=1"},
},
```

`BaseSpec` is the template every session is brought up with. It flows unchanged
through `reconcile` → `manage` (`Handle.spec` copies the spec, overwriting only
`WorkDir` and `Model`) → the Claude Code adapter, which layers `spec.Env` on top of
`os.Environ()` for the `claude` process (`internal/agent/claudecode/claudecode.go`).
So the variable reaches every spawned and resumed session.

## Adapter auth mode (confirmed)

The adapter authenticates via a **Claude.ai subscription (OAuth login)**, *not* an
API key. Verified on the daemon host (drvctl-035):

- `~/.claude/.credentials.json` contains a single `claudeAiOauth` key — the
  subscription OAuth token, not an `sk-ant-…` API key.
- `ANTHROPIC_API_KEY` is **not set** in the daemon environment.
- The adapter (`internal/agent/claudecode/claudecode.go`) sets no auth env of its
  own; it inherits `os.Environ()` and layers `spec.Env` on top, so `claude` uses
  whatever the host is logged in as.

Consequence: on subscription auth the 1h TTL is automatic but drops to 5m in
usage-credit overage — which is exactly the case `ENABLE_PROMPT_CACHING_1H=1`
guards against. If the daemon host is ever switched to API-key auth (the default
becomes 5m), the same drop-in keeps the TTL at 1h with no further change.
