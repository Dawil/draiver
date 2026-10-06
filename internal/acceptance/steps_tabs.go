package acceptance

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/webshot"
)

// steps_tabs.go realises drv-021's acceptance criteria (features/attempt_tabs.feature)
// against the live internal/web server. Each scenario seeds a real attempt, drives
// the actual detail-page handler, asserts the server-rendered tabbed-card contract,
// and attaches a real Playwright screenshot of the resulting page as its artefact —
// so draiver captures a genuine BDD report for this layout ticket. The drv-021 steps
// register themselves into the shared registry from an init(), leaving drv-019's
// steps.go untouched.

const (
	tabTicket = "TAB-1"
	tabAtt    = "0001"
)

func init() {
	drv021 := map[string]StepFunc{
		// --- Scenario: tabbed card, Spec default ---
		"an attempt detail page with every tab present":                                   givenEveryTab,
		"the top section renders as a single bordered tabbed card with an accessible tablist": thenTabbedCard,
		"the Spec tab is selected on load and its panel is the only one visible":           thenSpecDefault,

		// --- Scenario: selecting a tab ---
		"I select the Provenance settings tab":                                     whenSelectProvenance,
		"that tab's panel is shown and the Spec panel is hidden, with no page reload": thenProvenanceShown,

		// --- Scenario: roving tabindex ---
		"only the selected tab is in the tab sequence and the rest carry tabindex -1": thenRovingTabindex,
		"the tablist is wired for arrow, Home, and End key navigation":                thenArrowKeysWired,

		// --- Scenario: Git controls gate ---
		"a Done attempt with a base and a repo":                            givenDoneRepo,
		"the Git controls tab is absent":                                   thenGitTabAbsent,
		"a Running attempt with a base and a repo":                         givenRunningRepo,
		"the Git controls tab is present and selecting it shows the git panel": thenGitTabPresent,

		// --- Scenario: BDD report gate ---
		"a Running attempt with no captured BDD run":                         givenRunningNoRun,
		"the BDD report tab is absent":                                       thenBddTabAbsent,
		"a Running attempt with a captured BDD run":                          givenRunningWithRun,
		"the BDD report tab is present and selecting it embeds the report":   thenBddTabPresent,

		// --- Scenario: Agent Logs card ---
		"the Agent Logs section is a bordered card below the tabbed card, collapsed by default": thenAgentLogsCard,
		"the whole header row is the toggle target":                                             thenHeaderToggle,
		"I activate the Agent Logs header":                                                      whenActivateAgentLogs,
		"the Agent Logs section is expanded":                                                    thenAgentLogsExpanded,

		// --- Scenario: Log section last ---
		"a Review attempt detail page with every tab present":                          givenReviewEveryTab,
		"the Log heading, compose box, and polling log region are the last section":    thenLogLast,
		"the Review actions still show on the Review attempt":                          thenReviewActions,
	}
	for text, fn := range drv021 {
		registry[text] = fn
	}
}

// ---- seeding: a live board over an attempt with configurable gating ----

// seedTabsBoard builds a real webui over a seeded TAB-1/0001 attempt. lifecycle is
// "" (Running), "review", or "done"; withRun seeds a captured BDD run so the report
// tab gates on. The attempt always records a base + repo (so the Git-tab gate turns
// only on the Done state), and all temp state + the server are cleaned up on the World.
func seedTabsBoard(w *World, lifecycle string, withRun bool) (*board, error) {
	wd, err := os.MkdirTemp("", "tab-wd-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(wd) })

	dataDir, err := os.MkdirTemp("", "tab-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(tabTicket, tabAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + tabTicket + "\ntitle: drv-021 tabbed attempt page\n---\n\n# drv-021 tabbed attempt page\n\n## Acceptance criteria\n\n- The top section renders as a tabbed card with the Spec tab selected by default.\n"
	if err := os.WriteFile(root.SpecPath(tabTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + tabAtt + "\nticket: " + tabTicket + "\nrepo: " + wd + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(tabTicket, tabAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	if _, err := ticketlog.Append(root, tabTicket, tabAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return nil, err
	}
	switch lifecycle {
	case "review":
		if _, err := ticketlog.Append(root, tabTicket, tabAtt, event.Event{Type: "review", Actor: "agent:acceptance", Body: "claim"}); err != nil {
			return nil, err
		}
	case "done":
		if _, err := ticketlog.Append(root, tabTicket, tabAtt, event.Event{Type: "done", Actor: "human:acceptance", Body: "close"}); err != nil {
			return nil, err
		}
	}
	if withRun {
		if err := seedTabsRun(root, "20260101T100000Z"); err != nil {
			return nil, err
		}
	}

	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	return &board{root: root, wd: wd, srv: ts}, nil
}

// seedTabsRun writes a captured BDD run under TAB-1/0001 so the report panel renders
// (HasRuns) — mirroring drv-017's per-run key layout, reusing drv-019's seed JSON.
func seedTabsRun(root store.Root, stamp string) error {
	dir := filepath.Join(root.ArtefactsDir(tabTicket, tabAtt), report.BDDArtefactSubdir, accRung, accEnv, accCommit, stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cucumber.json"), []byte(seedCucumberJSON), 0o644)
}

func givenEveryTab(w *World, sr *StepRun) error       { return setBoard(w, "", true) }
func givenReviewEveryTab(w *World, sr *StepRun) error  { return setBoard(w, "review", true) }
func givenDoneRepo(w *World, sr *StepRun) error        { return setBoard(w, "done", false) }
func givenRunningRepo(w *World, sr *StepRun) error     { return setBoard(w, "", false) }
func givenRunningNoRun(w *World, sr *StepRun) error    { return setBoard(w, "", false) }
func givenRunningWithRun(w *World, sr *StepRun) error  { return setBoard(w, "", true) }

func setBoard(w *World, lifecycle string, withRun bool) error {
	b, err := seedTabsBoard(w, lifecycle, withRun)
	if err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

// ---- fetching + scoped HTML assertions ----

// tabsHTML GETs the live attempt detail page for the current board.
func tabsHTML(w *World) (string, error) {
	b, err := currentBoard(w)
	if err != nil {
		return "", err
	}
	resp, err := http.Get(b.url("/ticket/" + tabTicket + "/" + tabAtt))
	if err != nil {
		return "", fmt.Errorf("GET attempt page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("attempt page status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// openingTag returns the full `<...>` opening tag that contains marker (e.g. an
// id=/data-testid= attribute), so a caller can inspect the element's own attributes.
func openingTag(html, marker string) (string, bool) {
	i := strings.Index(html, marker)
	if i < 0 {
		return "", false
	}
	start := strings.LastIndex(html[:i], "<")
	end := strings.Index(html[i:], ">")
	if start < 0 || end < 0 {
		return "", false
	}
	return html[start : i+end+1], true
}

// panelHidden reports whether the tabpanel carrying testid is rendered with the
// `hidden` attribute (i.e. not the active panel).
func panelHidden(html, testid string) bool {
	tag, ok := openingTag(html, `data-testid="`+testid+`"`)
	return ok && strings.Contains(tag, " hidden")
}

func thenTabbedCard(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	if !strings.Contains(html, `class="attempt-tabs" data-testid="attempt-tabs"`) {
		return errors.New("top section is not the single bordered tabbed card (attempt-tabs)")
	}
	if !strings.Contains(html, `role="tablist"`) || !strings.Contains(html, `data-testid="attempt-tablist"`) {
		return errors.New("tabbed card has no role=tablist")
	}
	if n := strings.Count(html, `role="tab"`); n < 4 {
		return fmt.Errorf("expected at least 4 role=tab controls, got %d", n)
	}
	// Whole-page default-state evidence (Spec tab, Agent Logs collapsed).
	return shootTabs(w, sr, "page-default-spec", "", "")
}

func thenSpecDefault(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	specTab, ok := openingTag(html, `data-testid="attempt-tab-spec"`)
	if !ok {
		return errors.New("no Spec tab")
	}
	if !strings.Contains(specTab, `aria-selected="true"`) || !strings.Contains(specTab, `tabindex="0"`) {
		return fmt.Errorf("Spec tab is not the selected/roving tab: %s", specTab)
	}
	if panelHidden(html, "attempt-tabpanel-spec") {
		return errors.New("Spec panel is hidden on load; it must be the visible default")
	}
	for _, p := range []string{"attempt-tabpanel-provenance", "attempt-tabpanel-cache"} {
		if !panelHidden(html, p) {
			return fmt.Errorf("panel %q is not hidden while Spec is selected", p)
		}
	}
	return nil
}

func whenSelectProvenance(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	// A tab must switch client-side: it is a <button type="button"> with no htmx /
	// href, so selecting it cannot reload or round-trip to the server.
	tag, ok := openingTag(html, `data-testid="attempt-tab-provenance"`)
	if !ok {
		return errors.New("no Provenance tab")
	}
	if !strings.Contains(tag, `type="button"`) || strings.Contains(tag, "hx-") || strings.Contains(tag, "href=") {
		return fmt.Errorf("Provenance tab is not a client-only button: %s", tag)
	}
	// Drive the real click and screenshot the revealed Provenance panel.
	return shootTabs(w, sr, "tab-provenance", `[data-testid="attempt-tab-provenance"]`, `[data-testid="attempt-tab-provenance"]`)
}

func thenProvenanceShown(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	// The provenance + repo-settings panels live inside the provenance tabpanel.
	if !strings.Contains(html, `data-testid="attempt-tabpanel-provenance"`) {
		return errors.New("no Provenance tabpanel")
	}
	if !strings.Contains(html, `data-testid="provenance"`) || !strings.Contains(html, `data-testid="repo-settings"`) {
		return errors.New("Provenance tabpanel is missing the provenance / repo-settings panels")
	}
	return nil
}

func thenRovingTabindex(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	list, ok := sliceBetween(html, `data-testid="attempt-tablist"`, `</div>`)
	if !ok {
		return errors.New("cannot isolate the tablist")
	}
	tabs := strings.Count(list, `role="tab"`)
	zero := strings.Count(list, `tabindex="0"`)
	minus := strings.Count(list, `tabindex="-1"`)
	if zero != 1 {
		return fmt.Errorf("expected exactly one tab in the tab sequence (tabindex=0), got %d", zero)
	}
	if minus != tabs-1 {
		return fmt.Errorf("expected %d tabs with tabindex=-1, got %d", tabs-1, minus)
	}
	return nil
}

func thenArrowKeysWired(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	for _, key := range []string{"ArrowRight", "ArrowLeft", "Home", "End"} {
		if !strings.Contains(html, key) {
			return fmt.Errorf("tablist script is not wired for %s key navigation", key)
		}
	}
	return shootTabs(w, sr, "tablist-roving", "", "")
}

func thenGitTabAbsent(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	if strings.Contains(html, `data-testid="attempt-tab-git"`) {
		return errors.New("Git controls tab is present on a Done attempt; it must be gated off")
	}
	return shootTabs(w, sr, "git-tab-absent-done", "", "")
}

func thenGitTabPresent(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="attempt-tab-git"`) {
		return errors.New("Git controls tab is absent on a Running attempt with base+repo")
	}
	if !strings.Contains(html, `data-testid="git-controls"`) {
		return errors.New("Git controls panel is missing")
	}
	return shootTabs(w, sr, "tab-git", `[data-testid="attempt-tab-git"]`, `[data-testid="attempt-tab-git"]`)
}

func thenBddTabAbsent(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	if strings.Contains(html, `data-testid="attempt-tab-bdd"`) {
		return errors.New("BDD report tab is present with no captured run; it must be gated off")
	}
	return shootTabs(w, sr, "bdd-tab-absent-norun", "", "")
}

func thenBddTabPresent(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="attempt-tab-bdd"`) {
		return errors.New("BDD report tab is absent despite a captured run")
	}
	if !strings.Contains(html, `id="bdd-report-slot"`) || !strings.Contains(html, `data-testid="bdd-report"`) {
		return errors.New("BDD tabpanel does not embed the report (bdd-report-slot / bdd-report)")
	}
	// Click the BDD tab and screenshot — this also exercises the data-src → src
	// hydration that loads the report iframe on reveal (decision #12/#24). Wait on the
	// visible tab (not the frame, which is hidden until the tab is selected).
	return shootTabs(w, sr, "tab-bdd", `[data-testid="attempt-tab-bdd"]`, `[data-testid="attempt-tab-bdd"]`)
}

func thenAgentLogsCard(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	iTabs := strings.Index(html, `data-testid="attempt-tabs"`)
	iLogs := strings.Index(html, `data-testid="agent-logs"`)
	iLog := strings.Index(html, `class="log"`)
	if iTabs < 0 || iLogs < 0 || iLog < 0 {
		return errors.New("missing the attempt-tabs / agent-logs / log sections")
	}
	if !(iTabs < iLogs && iLogs < iLog) {
		return errors.New("Agent Logs is not a card between the tabbed card and the Log section")
	}
	// Bordered card = the agent-logs <section> wrapping a <details> with no `open`
	// attribute: collapsed by default, no stream yet.
	details, ok := openingTag(html, `data-testid="agent-logs-details"`)
	if !ok {
		return errors.New("no Agent Logs <details>")
	}
	if strings.Contains(details, " open") {
		return errors.New("Agent Logs is expanded by default; it must start collapsed")
	}
	return shootTabs(w, sr, "agent-logs-collapsed", "", "")
}

func thenHeaderToggle(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	// The whole header row is a <summary> (the native disclosure toggle) carrying the
	// agent-logs-summary class, so the full-width row — not a nested label — is the
	// click target.
	if !strings.Contains(html, `class="agent-logs-summary" data-testid="agent-logs-summary"`) {
		return errors.New("Agent Logs header is not a full-width summary toggle")
	}
	return nil
}

func whenActivateAgentLogs(w *World, sr *StepRun) error {
	// Click the summary to expand, and screenshot the expanded card.
	return shootTabs(w, sr, "agent-logs-expanded", `[data-testid="agent-logs-summary"]`, `[data-testid="agent-logs-summary"]`)
}

func thenAgentLogsExpanded(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	// The lazy-SSE contract: expanding opens the stream, collapsing closes it — the
	// toggle listener + EventSource ship in the page script (preserved from drv-021).
	if !strings.Contains(html, "addEventListener('toggle'") || !strings.Contains(html, "EventSource") {
		return errors.New("Agent Logs is not wired to open/close its live stream on toggle")
	}
	return nil
}

func thenLogLast(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	iLogs := strings.Index(html, `data-testid="agent-logs"`)
	iLog := strings.Index(html, `class="log"`)
	if iLog < iLogs {
		return errors.New("the Log section does not come last (after Agent Logs)")
	}
	if !strings.Contains(html, `data-testid="log-compose"`) {
		return errors.New("compose box missing from the Log section")
	}
	region, ok := openingTag(html, `data-testid="log-region"`)
	if !ok {
		return errors.New("no log region")
	}
	if !strings.Contains(region, `hx-trigger="every 3s"`) {
		return fmt.Errorf("log region does not poll every 3s: %s", region)
	}
	return shootTabs(w, sr, "log-section-last", "", "")
}

func thenReviewActions(w *World, sr *StepRun) error {
	html, err := tabsHTML(w)
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="review-actions"`) {
		return errors.New("Review actions are absent on a Review attempt")
	}
	return nil
}

// ---- screenshot helper (click-then-shoot) ----

// shootTabs captures a full-page Playwright screenshot of the current board's attempt
// page, optionally clicking clickSel first (to select a tab / expand the Agent Logs
// card) and waiting for waitSel. The PNG is attached as the step's artefact.
func shootTabs(w *World, sr *StepRun, name, clickSel, waitSel string) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	png, err := webshot.Capture(ctx, b.url("/ticket/"+tabTicket+"/"+tabAtt), webshot.Options{
		Width: 1280, Height: 1000, FullPage: true, WaitSelector: waitSel, ClickSelector: clickSel,
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

// sliceBetween returns the substring of html from the first occurrence of start up to
// the next occurrence of end after it.
func sliceBetween(html, start, end string) (string, bool) {
	i := strings.Index(html, start)
	if i < 0 {
		return "", false
	}
	rest := html[i:]
	j := strings.Index(rest, end)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}
