---
section: Added
---
**Durable ticket core.** An append-only, hash-chained event log with an on-disk
store layout: each event carries its predecessor's hash, so any edit breaks the
chain. `draiver audit` verifies the chain and exits nonzero if tampered.
