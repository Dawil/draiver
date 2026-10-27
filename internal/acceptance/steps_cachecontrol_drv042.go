package acceptance

import (
	"fmt"
	"net/http"
	"strings"
)

// steps_cachecontrol_drv042.go realises drv-042's acceptance criteria
// (features/board-cache/no-store.feature) against the live internal/web server.
// The full board page (handleBoardPage) must answer with `Cache-Control: no-store`
// so the browser's back/forward cache cannot restore a stale board snapshot on
// Back; the /board partial (handleBoardPartial), served by the same shared
// render() helper, must NOT carry it — proving the header is scoped to the handler,
// not smuggled into render() (which serves many templates). The poll contract
// (every 3s, morph swap) must be untouched. The Background step
// ("a board and a running attempt detail page") is reused from steps_morph.go.
func init() {
	drv042 := map[string]StepFunc{
		"the board page response carries the Cache-Control no-store header so Back re-fetches fresh": thenBoardPageNoStore,
		"the board partial response does not carry the Cache-Control no-store header":                thenBoardPartialNoNoStore,
		"the board region still polls every 3s and swaps via morph, unchanged":                       thenBoardPollUnchangedByNoStore,
	}
	for text, fn := range drv042 {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registration: " + text)
		}
		registry[text] = fn
	}
}

// cacheControl GETs path on the current board and returns its Cache-Control header,
// asserting a 200 so a routing/handler regression surfaces as a clear failure.
func cacheControl(w *World, path string) (string, error) {
	b, err := currentBoard(w)
	if err != nil {
		return "", err
	}
	resp, err := http.Get(b.url(path))
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s status = %d, want 200", path, resp.StatusCode)
	}
	return resp.Header.Get("Cache-Control"), nil
}

func thenBoardPageNoStore(w *World, sr *StepRun) error {
	cc, err := cacheControl(w, "/")
	if err != nil {
		return err
	}
	if !strings.Contains(cc, "no-store") {
		return fmt.Errorf("board page Cache-Control = %q, want it to include \"no-store\" (defeats bfcache on Back)", cc)
	}
	return shootMorph(w, sr, "/", "board-no-store", `[data-testid="board"]`)
}

func thenBoardPartialNoNoStore(w *World, sr *StepRun) error {
	// The /board partial is an htmx fetch, never a bfcache navigation target, so it
	// is out of scope — and because it rides the shared render() helper, asserting it
	// does NOT carry no-store proves the header was set in handleBoardPage, not in
	// render() (which would leak it onto every template).
	cc, err := cacheControl(w, "/board")
	if err != nil {
		return err
	}
	if strings.Contains(cc, "no-store") {
		return fmt.Errorf("board partial Cache-Control = %q, want no \"no-store\": the header must be scoped to the full-page handler, not the shared render()", cc)
	}
	return nil
}

func thenBoardPollUnchangedByNoStore(w *World, sr *StepRun) error {
	html, err := getHTML(w, "/")
	if err != nil {
		return err
	}
	// Reuse drvweb-025's morph contract: the no-store header must not disturb the
	// board's unconditional 3s poll or its idiomorph swap.
	return assertMorphSwap(html, `data-testid="board"`, "board")
}
