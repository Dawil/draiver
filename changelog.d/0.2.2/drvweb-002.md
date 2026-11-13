---
section: Changed
---
**Attempt-log timestamps render as relative age, full stamp on hover
(`draiver webui`, drvweb-002).** The attempt timeline showed each event's
timestamp as a raw minute-precision UTC string (`2006-01-02 15:04Z`), forcing
readers to mentally diff wall-clock strings and discarding sub-minute ordering.
Each entry now renders a relative age instead — "just now", "3 minutes ago",
"yesterday", … — inside a `<time>` element whose native `title` tooltip carries
the precise second-precision UTC stamp alongside the operator's local time.
`static/reltime.js` recomputes the age from the `datetime` attribute on load,
after every htmx log-swap, and on a 30s tick, so the age stays live even on a
Done attempt whose log region never polls. The Go `relativeAge` and the JS
share one set of bucket boundaries, each pinned by a test
(`TestRelativeAgeBuckets`, `TestTimelineShowsRelativeTimestamps`).
