---
section: Fixed
---
**Markdown pipe tables render as styled HTML tables in the webui (drvweb-024).**
The webui's single shared goldmark instance (`internal/web/web.go`) was built
without the table extension, so GFM pipe tables in event bodies, card reasons and
`spec.md` rendered as literal `|`/`---` text. It now registers
`extension.Table` — deliberately not the full `extension.GFM`, whose autolinks
would widen the destination set `linkPolicy` must guard — alongside the existing
parser options, so the http/https/relative-only link scheme floor is unchanged
(a `javascript:` link inside a cell is still blanked). The extension ships inside
the already-required goldmark v1.8.5, so there is no new module dependency.
Rendered tables get a minimal `.prose` grid in `style.css` — hairline cell
borders, a quiet header row and zebra striping, all keyed to the
`--line`/`--panel-2`/`--ink` theme tokens so they read in light and dark. A BDD
scenario (`features/attempt-page/markdown-tables.feature`) screenshots a rendered
table on the ticket detail page.
