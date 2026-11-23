# internal/acceptance — writing a BDD step

The repo's own small Gherkin runner (not godog): `Run` (`acceptance.go`) runs every
`*.feature` under a dir through the step `registry` and writes cucumber-JSON plus
artefacts. **Read this instead of the step files**: the helper table lists the shared
fixtures, so you don't need to open the ticket-named `steps_*.go` files.

## Anatomy of a ticket's scenarios

- **Feature:** `features/<capability>/<name>.feature`. Reuse an existing capability
  folder if one fits. Tag the `Feature:` line `@<ticket-id>`, plus `@ui` or `@cli`.
- **Steps:** a new `internal/acceptance/steps_<topic>_<ticket>.go` (for example
  `steps_agentlogs_drv066.go`) that adds its step texts to `registry` in `func init()`.
  Step text is matched **exactly** (keyword-agnostic: `Given`/`And` both work), and
  there are no parameters, so put distinct values in distinct step texts. Don't add to
  the map literal in `steps.go`; it predates the `init()` convention.
- **Drift guard:** `TestFeaturesHaveStepDefinitions` (`acceptance_test.go`, unit
  rung, no browser) fails for any feature step with no registry entry.
- Prefix ticket-local helpers and constants (`d53…`, `alTicket`): the package is flat.

## The `World`: state between steps

Each scenario gets a fresh `*World`. Steps share state with `w.set(key, v)` and
`w.get(key)`: by convention, one typed fixture struct under one key, plus a
`current…(w)` getter that errors when it's missing (see `currentBoard`). Register
every teardown with `w.defer_(fn)`; teardowns run in reverse after the scenario.
Scenarios run sequentially, so swapping a process env var (such as `XDG_CACHE_HOME`)
is safe if a `defer_` restores it.

## Artefacts: every scenario attaches one

`TestEveryScenarioHasAnArtifact` requires **every** scenario, UI or not, to have at
least one step that appends to `sr.Embeddings`:

- **UI:** `shoot(w, sr, path, name, fullPage, waitSel, clickSel)` takes a Playwright
  PNG of `currentBoard(w)` at `path`. `clickSel` clicks first (for example to open a
  tab), and `waitSel` waits for an element.
- **CLI or anything else:** `attachText(sr, name, content)` attaches the captured
  output as `text/plain`.

**Without a browser** (`webshot.Available()` false), `shoot` errors and fails the
scenario; the suite tests in `acceptance_test.go` skip, so only the drift guard runs.
The rung's `bdd` env blocks without one. Locally: `npm install --prefix
features/screenshots`.

## Helpers to reuse

Line numbers are checked by `TestReadmeHelperTableIsCurrent`. If you move a helper,
update its row.

| Helper | Where | What it gives you |
|---|---|---|
| `World` | `steps.go:29` | scenario state: `set`/`get`/`defer_` |
| `seedBoard` | `steps.go:231` | temp data root with attempt `ACC-1/0001` + a live `httptest` webui; `w.set("board", b)` it |
| `currentBoard` | `steps.go:295` | the `*board` under key `"board"`; `b.url(path)`, `b.root` (`store.Root`) |
| `seedRun` | `steps.go:282` | a captured BDD run, so the report panel renders |
| `shoot` | `steps.go:308` | Playwright screenshot artefact of a board page |
| `attachText` | `steps_recall.go:27` | text artefact (CLI output, JSON, HTML excerpt) |
| `getBody` | `steps_recall.go:50` | GET a URL, require 200, return the body |
| `getHTML` | `steps_morph.go:51` | GET a path on `currentBoard`, require 200 |
| `setBoard` | `steps_tabs.go:156` | `seedTabsBoard` (Running/review/done, optional run) stored as `"board"` |
| `shootTabs` | `steps_tabs.go:465` | full-page shot of the attempt page after clicking a tab |
| `seedTypedTicket` | `steps_codebase.go:29` | bare data root + one ticket (optional `type:`), no server |
| `addTypedTicket` | `steps_codebase.go:44` | add another ticket to an existing root |
| `seedLedgerBoard` | `steps_codebase_web.go:42` | live board over a root you build in a callback |
| `seedRerunBoard` | `steps_reportfidelity.go:67` | real git repo + managed worktree + board, for rerun/report flows |
| `seedGitBoard` | `steps_git.go:91` | board over a repo, worktree and bare origin (`advanced`/`uptodate`/`dirty`/`ahead`) |
| `gitc` | `steps_git.go:69` | run `git -C dir …` with a fixed identity, returning an error |
| `overlayBin` | `steps_overlay.go:86` | builds the real `draiver` binary once per run; run it with `--data <root>` |
| `ovRepoRoot` | `steps_overlay.go:109` | the module root (where `go.mod` is) |
| `ovChildEnv` | `steps_overlay.go:293` | an env for a shelled `draiver` with `DRAIVER_*`/`XDG_CACHE_HOME` isolated |
| `mustTemp` | `steps_overlay.go:204` | a temp dir that is removed on cleanup |
| `seedReviewBoard` | `reviewenv_steps.go:263` | review-env fixture: repo + Review attempt + config + board |

To run the CLI, use `bin, err := overlayBin()`, then `exec.Command(bin, "--data",
root.Dir, …)`. Never use `go run`, which remaps exit codes. To write events, use
`ticketlog.Append(root, ticket, att, event.Event{…})`.

## Skeleton

`features/<capability>/<name>.feature`:

```gherkin
@ui @drv-999
Feature: The board shows the widget

  Scenario: The widget renders on the board
    Given a board for the drv-999 widget
    Then the drv-999 widget is on the board page
```

`internal/acceptance/steps_widget_drv999.go`:

```go
package acceptance

import (
	"fmt"
	"strings"
)

func init() {
	for text, fn := range map[string]StepFunc{
		"a board for the drv-999 widget":           givenWidgetBoard,
		"the drv-999 widget is on the board page": thenWidgetOnBoard,
	} {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registration: " + text)
		}
		registry[text] = fn
	}
}

func givenWidgetBoard(w *World, sr *StepRun) error {
	b, err := seedBoard(w, "levels: []\n")
	if err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

func thenWidgetOnBoard(w *World, sr *StepRun) error {
	html, err := getHTML(w, "/")
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="widget"`) {
		return fmt.Errorf("board page has no widget")
	}
	return shoot(w, sr, "/", "widget", true, `[data-testid="widget"]`, "")
}
```

## Running it

```sh
go test ./internal/acceptance -run TestFeaturesHaveStepDefinitions  # drift guard, fast
draiver test acceptance        # the rung: changelog → unit → acceptance (all features)
go run ./features/acceptance   # the whole suite directly; artefacts in features/_artifacts/
```

To run only your feature, use a scratch test, and delete it before you commit:

```go
// internal/acceptance/zz_focus_test.go — DELETE before commit
func TestFocus(t *testing.T) {
	res, err := Run("../../features/<capability>", t.TempDir())
	if err != nil || res.ScenariosFailed != 0 {
		t.Fatalf("err=%v\n%s", err, res.Report.Summary())
	}
}
```

Run it with `go test ./internal/acceptance -run TestFocus -v`. drv-063 adds focused
flags to the runner.
