---
section: Changed
---
**Favicon badge decoupled from the DOM.** The Stuck badge now flips off an htmx
`HX-Trigger` event carrying the board state, instead of scraping a
`data-testid` count out of the page (task-010).
