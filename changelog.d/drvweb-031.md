---
section: Fixed
---
**Four small board fixes: the compose box clears after a submit, Sync no longer asks for confirmation, card text keeps its width beside the corner buttons, and tab panels no longer repeat their tab's name (drvweb-031).**

- **Compose box clears after a successful submit.** The Review buttons **Decision — reopen to Running** and **Done — close** send the compose box's text but never cleared it, so the reason stayed in the box and looked unsent. They now empty it on success. The compose form's own **Add entry** now clears only the body, tags and supersedes fields. Before, it reset the whole form, which also set the type back to `note` and re-ticked 📬 *Message the agent*. A failed submit leaves everything as typed.
- **Sync runs without a confirm dialog.** The Git controls tab's **Sync — pull remote `<base>` in** runs as soon as it is clicked, because a pull is safe. A dirty checkout still refuses with the same banner. Push, Done, Tear down and Archive keep their confirms.
- **Dashboard cards keep their width.** Only the ticket-id line leaves room for the corner buttons (star, play, mark-done, restart, archive). The title and the lines below it now use the card's full width. Before, the whole card was narrowed by 62–88px.
- **No duplicate tab headings.** The attempt page's Spec, Git controls, Review environment, BDD report and Promotions panels dropped the heading that repeated the tab's label. Each panel is still labelled by its tab through `aria-labelledby`.
- **Browser probes for acceptance tests.** `internal/webshot` gains `CaptureProbe` with `Probe` and `HoverSelector` options. A scenario can run a script in the page and check the live result, such as an input's value or an element's position, as well as taking a screenshot.
