---
section: Added
---
**Live attempt detail page.** The log timeline and status badge auto-refresh
via htmx polling and stop polling once the attempt is terminal (the live
fragment answers `286` to self-cancel); the whole-board poll, by contrast,
never quiesces, since a new ticket or attempt can appear at any moment
(task-008, task-009).
