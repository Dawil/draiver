---
section: Fixed
---
**Escalation resolve textarea no longer loses focus every 3s (drvweb-026).**
Typing into an open escalation's resolve box (`#resolve-input-{seq}`) on the
attempt page was interrupted on every 3s poll — the browser pulled focus out
mid-keystroke. Root cause: the resolve textarea was the one polled input that
*also* carried `hx-preserve="true"`. htmx runs its own `hx-preserve`
node-retention on **every** swap, *before* handing off to the morph extension —
it moved the live focused textarea out of the log region (parking it in a pantry
after `</body>`, or, on engines without `Element.moveBefore()`, detaching it with
`replaceChild`, which blurs it outright) and then moved it back, colliding with
the fresh empty server textarea of the same id. The fix removes `hx-preserve` and
lets idiomorph alone keep the field: its `restoreFocus` holds focus + caret, and a
global `Idiomorph.defaults.ignoreActiveValue = true` (set after the extension
loads, on both the attempt and board polling pages) stops the empty server value
from clobbering the half-typed answer. A **dynamic** Playwright regression test
(`e2e/tests/detail.spec.ts`) now types into the resolve box, waits out a real
`/live` poll, and asserts focus, caret, and value all survive — closing the gap
the static-only drvweb-025 coverage left.
