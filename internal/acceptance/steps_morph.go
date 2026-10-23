package acceptance

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/webshot"
)

// steps_morph.go realises drvweb-025's acceptance criteria
// (features/polling-morph/selection.feature) against the live internal/web server.
// Each scenario seeds a real board + attempt (reusing the drv-021 tabs seeder),
// asserts the server-rendered morph contract — both polling regions swap via the
// vendored idiomorph extension rather than innerHTML — and attaches a Playwright
// screenshot of the live webui. The dynamic selection-survival proof lives in the
// Playwright e2e; this rung complements it with rendered-page evidence. The steps
// register themselves into the shared registry from an init(), leaving the other
// step files untouched.

func init() {
	drvweb025 := map[string]StepFunc{
		"a board and a running attempt detail page": givenMorphRunning,
		"the board region polls with a morph swap, not an innerHTML replace":                     thenBoardMorphs,
		"the idiomorph extension is served locally and enabled, with no external script":         thenIdiomorphLocal,
		"the attempt live region polls with a morph swap, not an innerHTML replace":              thenLiveRegionMorphs,
		"the live fragment still carries its out-of-band state badge and log count":              thenLiveFragmentOOB,
		"a Done attempt detail page":                                                             givenMorphDone,
		"the live endpoint answers 286 so htmx stops polling":                                    thenLiveSelfCancels,
		"the detail page ships no innerHTML-era focus-snapshot or is-target workaround":          thenWorkaroundsRetired,
	}
	for text, fn := range drvweb025 {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registration: " + text)
		}
		registry[text] = fn
	}
}

func givenMorphRunning(w *World, sr *StepRun) error { return setBoard(w, "", false) }
func givenMorphDone(w *World, sr *StepRun) error    { return setBoard(w, "done", false) }

// getHTML GETs path on the current board and returns its body, asserting a 200.
func getHTML(w *World, path string) (string, error) {
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
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// assertMorphSwap checks that the element carrying marker (a data-testid=) polls
// every 3s and swaps via morph:innerHTML under the morph extension — never the old
// innerHTML replace that clobbered selection.
func assertMorphSwap(html, marker, region string) error {
	tag, ok := openingTag(html, marker)
	if !ok {
		return fmt.Errorf("%s region (%s) not found", region, marker)
	}
	if !strings.Contains(tag, `hx-trigger="every 3s"`) {
		return fmt.Errorf("%s region does not poll every 3s: %s", region, tag)
	}
	if !strings.Contains(tag, `hx-ext="morph"`) {
		return fmt.Errorf("%s region does not enable the morph extension: %s", region, tag)
	}
	if !strings.Contains(tag, `hx-swap="morph:innerHTML"`) {
		return fmt.Errorf("%s region does not swap via morph:innerHTML: %s", region, tag)
	}
	if strings.Contains(tag, `hx-swap="innerHTML"`) {
		return fmt.Errorf("%s region still does the innerHTML replace that clobbers selection: %s", region, tag)
	}
	return nil
}

func thenBoardMorphs(w *World, sr *StepRun) error {
	html, err := getHTML(w, "/")
	if err != nil {
		return err
	}
	if err := assertMorphSwap(html, `data-testid="board"`, "board"); err != nil {
		return err
	}
	return shootMorph(w, sr, "/", "board-morph-poll", `[data-testid="board"]`)
}

func thenIdiomorphLocal(w *World, sr *StepRun) error {
	html, err := getHTML(w, "/")
	if err != nil {
		return err
	}
	// Self-hosted: the page references the vendored extension by a local /static
	// path, and no <script> is loaded from an external origin (the CSP/offline
	// constraint — no CDN).
	if !strings.Contains(html, `<script src="/static/idiomorph-ext.min.js">`) {
		return errors.New("board page does not load the local idiomorph extension")
	}
	if strings.Contains(html, `<script src="http://`) || strings.Contains(html, `<script src="https://`) || strings.Contains(html, `<script src="//`) {
		return errors.New("board page loads a script from an external origin; assets must be self-hosted")
	}
	// The asset is actually served and is the htmx extension (registers "morph").
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	resp, err := http.Get(b.url("/static/idiomorph-ext.min.js"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /static/idiomorph-ext.min.js = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), "defineExtension") {
		return errors.New("served idiomorph asset does not register an htmx extension")
	}
	return nil
}

func thenLiveRegionMorphs(w *World, sr *StepRun) error {
	html, err := getHTML(w, "/ticket/"+tabTicket+"/"+tabAtt)
	if err != nil {
		return err
	}
	if err := assertMorphSwap(html, `data-testid="log-region"`, "attempt live"); err != nil {
		return err
	}
	return shootMorph(w, sr, "/ticket/"+tabTicket+"/"+tabAtt, "live-region-morph-poll", `[data-testid="ticket-detail"]`)
}

func thenLiveFragmentOOB(w *World, sr *StepRun) error {
	// The morph swap handles the primary content; the badge/count still ride the
	// response as htmx out-of-band swaps, untouched by the extension.
	frag, err := getHTML(w, "/ticket/"+tabTicket+"/"+tabAtt+"/live")
	if err != nil {
		return err
	}
	for _, want := range []string{`id="state-badge"`, `hx-swap-oob="true"`, `id="log-count"`} {
		if !strings.Contains(frag, want) {
			return fmt.Errorf("live fragment missing out-of-band marker %q", want)
		}
	}
	if strings.Contains(frag, "<html") {
		return errors.New("live fragment leaked full-page chrome; it must be a fragment")
	}
	return nil
}

func thenLiveSelfCancels(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	resp, err := http.Get(b.url("/ticket/" + tabTicket + "/" + tabAtt + "/live"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 286 {
		return fmt.Errorf("terminal attempt /live status = %d, want 286 (htmx poll self-cancel)", resp.StatusCode)
	}
	return shootMorph(w, sr, "/ticket/"+tabTicket+"/"+tabAtt, "terminal-poll-self-cancel", `[data-testid="ticket-detail"]`)
}

func thenWorkaroundsRetired(w *World, sr *StepRun) error {
	html, err := getHTML(w, "/ticket/"+tabTicket+"/"+tabAtt)
	if err != nil {
		return err
	}
	// Morph preserves nodes by id, so native :target survives the poll and the
	// active input keeps focus/caret — the innerHTML-era JS mirrors are gone.
	for _, gone := range []string{"is-target", "setSelectionRange"} {
		if strings.Contains(html, gone) {
			return fmt.Errorf("detail page still ships the retired workaround marker %q", gone)
		}
	}
	return shootMorph(w, sr, "/ticket/"+tabTicket+"/"+tabAtt, "workarounds-retired", `[data-testid="ticket-detail"]`)
}

// shootMorph captures a full-page Playwright screenshot of the current board's page
// at path (waiting for waitSel) and attaches it as the step's artefact.
func shootMorph(w *World, sr *StepRun, path, name, waitSel string) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	png, err := webshot.Capture(ctx, b.url(path), webshot.Options{
		Width: 1280, Height: 1000, FullPage: true, WaitSelector: waitSel,
	})
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png",
		Name:     name,
		Data:     base64.StdEncoding.EncodeToString(png),
	})
	return nil
}
