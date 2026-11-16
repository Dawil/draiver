---
section: Added
---
**Sanitizer hardening for rendered markdown bodies (drvweb-004).** Audited the
bare `goldmark.New()` renderer: its default already blanks `javascript:`,
`vbscript:`, `file:`, and non-image `data:` link hrefs and omits raw HTML, but it
admits `data:image/{png,gif,jpeg,webp}` links and autolinks. Added a goldmark AST
transformer (`linkPolicy`) that enforces an http/https-or-relative scheme floor on
every body link/image/autolink, closing that carve-out so a `data:`/`javascript:`
link in an event body can never produce a live href.
