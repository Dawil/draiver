package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/worktree"
)

// steps_boardcontrols_drv046.go realises drv-046's acceptance criteria
// (features/promotions/board-controls.feature): the board card becomes the promotion
// control surface. A chip renders for EVERY declared promotion (grey until first run);
// clicking a chip POSTs the env-scoped check with ?surface=card and the whole card
// re-renders with the chip flipped; an all-green Review card shows a "mark done" tick
// that POSTs `done` and relocates the card to the Done column; and each check writes a
// `started` then a terminal system-log line. Every UI scenario stands up a real managed
// worktree carrying a committed promotions pyramid, drives the LIVE webui end to end
// (real handlePromote → promote.Run, real handleMarkDone → gw.Done), and screenshots the
// rendered state. The steps register into the shared registry from init().

const (
	bcTicket      = "BCB-1" // the mixed-chip Review attempt (dev green, staging fail, prod un-run)
	bcGreenTicket = "BCB-2" // the all-green Review attempt (mark-done tick lit)
	bcAtt         = "0001"
	// bcStaleCommit is an older commit a stale/failed result names — anything that is not
	// the worktree HEAD.
	bcStaleCommit = "0000000000000000000000000000000000000000"
)

func init() {
	drv046 := map[string]StepFunc{
		"a Review attempt whose repo declares dev, staging and prod promotions":                       givenBCThreePromotions,
		"dev is green at HEAD, staging has failed, and prod has never been checked":                   givenBCMixedResults,
		"the card shows a green dev chip, a red staging chip, and a grey not-run prod chip":            thenBCMixedChips,
		"I click the grey prod chip on the card":                                                      whenBCClickProd,
		"the check runs and the card comes back with prod flipped green, without a full page reload":  thenBCProdFlippedGreen,
		"the chip carries the spinner and aria-busy mechanism shown while the check is in flight":     thenBCSpinnerMechanism,
		"a Review attempt whose every declared promotion is green at HEAD":                            givenBCAllGreen,
		"the card shows the mark-done tick, distinct from the archive accept tick":                    thenBCDoneTickShown,
		"I click the mark-done tick":                                                                  whenBCClickDone,
		"a done event is appended and the card moves to the Done column, without opening the attempt page": thenBCMovedToDone,
		"the card shows no mark-done tick":                               thenBCNoDoneTick,
		"a done POST for that attempt is refused":                        thenBCDonePostRefused,
		"the attempt's System Logs show a started line and then a pass line for prod": thenBCSystemLogStartedThenPass,
	}
	for text, fn := range drv046 {
		registry[text] = fn
	}
	// "I open the board" / "When I open the board" is already registered by drv-043
	// (noopPromote) — the board is served live, so opening it is a no-op in-process.
}

// bcBoard is a live board over a Review attempt whose committed pyramid declares
// promotions, plus the worktree HEAD the chips and the mark-done gate fold.
type bcBoard struct {
	*board
	ticket string
	head   string
}

func currentBCBoard(w *World) (*bcBoard, error) {
	b, _ := w.get("bcboard").(*bcBoard)
	if b == nil {
		return nil, errors.New("no board-controls board set up in scope")
	}
	return b, nil
}

// bcPyramidYAML declares the named environments and a trivially-green promotion for each
// (qa "true", an always-green healthcheck), so a real promote.Run against any of them
// passes without an external environment — exactly what the chip click exercises.
func bcPyramidYAML(envs ...string) string {
	var b strings.Builder
	b.WriteString("environments:\n")
	for _, e := range envs {
		fmt.Fprintf(&b, "  - name: %s\n    healthchecks:\n      - name: reachable\n        script: \"true\"\n", e)
	}
	b.WriteString("levels:\n  - name: unit\n    run: \"true\"\npromotions:\n")
	for _, e := range envs {
		fmt.Fprintf(&b, "  - environment: %s\n    base: main\n    qa: \"true\"\n", e)
	}
	return b.String()
}

// seedBCBoard stands up a real repo + managed worktree carrying the given pyramid, an
// attempt driven into Review, and a live web server — mirroring seedPromBoard. HEAD is
// the pyramid commit; a promotion-result logged at HEAD renders green, one at an older
// commit renders stale, a fail renders red, and a declared env with no result renders a
// grey not-run chip.
func seedBCBoard(w *World, ticket, pyramidYAML string) (*bcBoard, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not on PATH")
	}
	cache, err := os.MkdirTemp("", "bc-cache-")
	if err != nil {
		return nil, err
	}
	prev, had := os.LookupEnv("XDG_CACHE_HOME")
	os.Setenv("XDG_CACHE_HOME", cache)
	w.defer_(func() {
		if had {
			os.Setenv("XDG_CACHE_HOME", prev)
		} else {
			os.Unsetenv("XDG_CACHE_HOME")
		}
		os.RemoveAll(cache)
	})

	repo, err := os.MkdirTemp("", "bc-repo-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(repo) })
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if err := gitc(repo, args...); err != nil {
			return nil, err
		}
	}

	m, err := worktree.NewManager(repo)
	if err != nil {
		return nil, err
	}
	key := worktree.Key{Ticket: ticket, Attempt: bcAtt}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: key})
	if err != nil {
		return nil, err
	}
	checkout := wt.Path
	if err := os.WriteFile(filepath.Join(checkout, ".test-pyramid.yaml"), []byte(pyramidYAML), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", ".test-pyramid.yaml"}, {"commit", "-q", "-m", "add pyramid"}} {
		if err := gitc(checkout, args...); err != nil {
			return nil, err
		}
	}
	head, err := worktree.HeadSHA(context.Background(), checkout)
	if err != nil {
		return nil, err
	}

	dataDir, err := os.MkdirTemp("", "bc-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(ticket, bcAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + ticket + "\ntitle: drv-046 board controls\n---\n\n# drv-046 board controls\n\n## Acceptance criteria\n\n- Promotions are driveable from the card.\n"
	if err := os.WriteFile(root.SpecPath(ticket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + bcAtt + "\nticket: " + ticket + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(ticket, bcAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	for _, e := range []event.Event{
		{Type: "created", Actor: "agent:acceptance", Body: "start"},
		{Type: "review", Actor: "agent:acceptance", Body: "Claiming review."},
	} {
		if _, err := ticketlog.Append(root, ticket, bcAtt, e); err != nil {
			return nil, err
		}
	}

	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	bb := &bcBoard{board: &board{root: root, wd: repo, srv: ts}, ticket: ticket, head: head}
	w.set("board", bb.board) // shoot() resolves the live server via the shared key
	w.set("bcboard", bb)
	return bb, nil
}

func givenBCThreePromotions(w *World, sr *StepRun) error {
	_, err := seedBCBoard(w, bcTicket, bcPyramidYAML("dev", "staging", "prod"))
	return err
}

// givenBCMixedResults logs a pass for dev at HEAD (green), a fail for staging at HEAD
// (red), and nothing for prod (grey not-run) — the three-state card the spec's mixed
// screenshot shows.
func givenBCMixedResults(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	for _, e := range []event.Event{
		{Type: "promotion-result", Actor: "agent:acceptance", Environment: "dev", Commit: b.head, Outcome: "pass", Body: "Promotion to `dev` **verified** at HEAD."},
		{Type: "promotion-result", Actor: "agent:acceptance", Environment: "staging", Commit: b.head, Outcome: "fail", Body: "Promotion to `staging` **failed** at HEAD."},
	} {
		if _, err := ticketlog.Append(b.root, b.ticket, bcAtt, e); err != nil {
			return err
		}
	}
	return nil
}

func thenBCMixedChips(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.url("/"))
	if err != nil {
		return err
	}
	checks := []struct{ env, class string }{{"dev", "promo-ok"}, {"staging", "promo-fail"}, {"prod", "promo-none"}}
	for _, c := range checks {
		testid := "promo-" + b.ticket + "-" + bcAtt + "-" + c.env
		if !strings.Contains(body, testid) {
			return fmt.Errorf("board should render a %q promotion chip (data-testid %q)\n%s", c.env, testid, body)
		}
	}
	// Each chip class must be present on its chip (grey not-run included).
	if !strings.Contains(body, "promo-none") {
		return fmt.Errorf("the never-run prod promotion must render a grey not-run chip (promo-none)")
	}
	if !strings.Contains(body, "promo-ok") || !strings.Contains(body, "promo-fail") {
		return fmt.Errorf("the card must render the green (ok) and red (fail) chips too")
	}
	return shoot(w, sr, "/", "board-controls-mixed-chips", true, "", "")
}

// whenBCClickProd drives a real board-origin promote of the grey prod chip: an htmx
// POST to the env-scoped route carrying ?surface=card, exactly as the chip's form does.
// A same-origin request with no Origin header passes the guard, so this runs the real
// handlePromote → promote.Run → system-log path end to end.
func whenBCClickProd(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	resp, err := http.Post(b.url("/ticket/"+b.ticket+"/"+bcAtt+"/promote/prod?surface=card"), "application/x-www-form-urlencoded", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST promote/prod?surface=card: status %d\n%s", resp.StatusCode, raw)
	}
	w.set("bcprodresp", string(raw))
	return nil
}

func thenBCProdFlippedGreen(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	body, _ := w.get("bcprodresp").(string)
	// The board-origin response is a card fragment (not a full page): it must be the
	// re-rendered card for this attempt, with prod now green at HEAD.
	if !strings.Contains(body, "card-"+b.ticket+"-"+bcAtt) {
		return fmt.Errorf("the ?surface=card response should be the re-rendered card fragment, got:\n%s", body)
	}
	prodChip := "promo-" + b.ticket + "-" + bcAtt + "-prod"
	if !strings.Contains(body, prodChip) {
		return fmt.Errorf("the re-rendered card should still carry the prod chip")
	}
	// The durable verdict must be a pass at HEAD, so the folded chip is green.
	a, err := project.LoadAttempt(b.root, b.ticket, bcAtt)
	if err != nil {
		return err
	}
	st := project.DerivePromotions(a.Events)["prod"]
	if st.Outcome != "pass" || st.Commit != b.head {
		return fmt.Errorf("prod should be recorded pass@HEAD, got outcome=%q commit=%q (head %q)", st.Outcome, st.Commit, b.head)
	}
	if !strings.Contains(body, "promo-ok") {
		return fmt.Errorf("the re-rendered card should show prod flipped to the green (ok) class, got:\n%s", body)
	}
	return nil
}

// thenBCSpinnerMechanism asserts the actuating chip carries the loading affordance: an
// inline spinner element bound to htmx's hx-indicator and an aria-busy toggle, so a
// reviewer sees the check is in flight. (The spinner is CSS-hidden until htmx adds
// .htmx-request to it for the request's duration; catching that sub-second flash in a
// screenshot is impractical, so the mechanism is asserted structurally and corroborated
// by the mixed-chip screenshot.)
func thenBCSpinnerMechanism(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.url("/"))
	if err != nil {
		return err
	}
	needles := []string{
		"promo-spin-" + b.ticket + "-" + bcAtt + "-prod", // the inline spinner element
		`hx-indicator="find .promo-spin"`,                // htmx shows it only mid-request
		"aria-busy",                                      // the chip announces busy while in flight
	}
	for _, n := range needles {
		if !strings.Contains(body, n) {
			return fmt.Errorf("the chip is missing its loading mechanism %q\n%s", n, body)
		}
	}
	// The CSS that reveals the spinner only on the in-flight request must ship too.
	if err := bcAssertStyleContains(b, ".promo-spin.htmx-request"); err != nil {
		return err
	}
	return nil
}

func givenBCAllGreen(w *World, sr *StepRun) error {
	b, err := seedBCBoard(w, bcGreenTicket, bcPyramidYAML("dev", "staging"))
	if err != nil {
		return err
	}
	for _, env := range []string{"dev", "staging"} {
		if _, err := ticketlog.Append(b.root, bcGreenTicket, bcAtt, event.Event{
			Type: "promotion-result", Actor: "agent:acceptance", Environment: env, Commit: b.head, Outcome: "pass",
			Body: "Promotion to `" + env + "` **verified** at HEAD.",
		}); err != nil {
			return err
		}
	}
	return nil
}

func thenBCDoneTickShown(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.url("/"))
	if err != nil {
		return err
	}
	doneTick := "markdone-btn-" + b.ticket + "-" + bcAtt
	archiveTick := "archive-btn-" + b.ticket + "-" + bcAtt
	if !strings.Contains(body, doneTick) {
		return fmt.Errorf("an all-green Review card should show the mark-done tick (data-testid %q)\n%s", doneTick, body)
	}
	// Distinct from the archive accept tick: a Review card still carries the archive
	// affordance, and the two must be separate controls with separate testids.
	if !strings.Contains(body, archiveTick) {
		return fmt.Errorf("the card should still carry the archive affordance (%q), distinct from mark-done", archiveTick)
	}
	if doneTick == archiveTick {
		return fmt.Errorf("the mark-done and archive testids must differ")
	}
	return shoot(w, sr, "/", "board-controls-all-green-done-tick", true, "", "")
}

// whenBCClickDone drives a real board-origin ratify: an htmx POST to the new
// /ticket/{id}/{att}/done route the mark-done form targets. Same-origin with no Origin
// header passes the guard, running the real handleMarkDone → gw.Done end to end.
func whenBCClickDone(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	resp, err := http.Post(b.url("/ticket/"+b.ticket+"/"+bcAtt+"/done"), "application/x-www-form-urlencoded", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST done: status %d\n%s", resp.StatusCode, raw)
	}
	w.set("bcdoneresp", string(raw))
	return nil
}

func thenBCMovedToDone(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	// The gateway must have appended a `done` event, moving the attempt to Done.
	a, err := project.LoadAttempt(b.root, b.ticket, bcAtt)
	if err != nil {
		return err
	}
	if a.State != project.Done {
		return fmt.Errorf("after the mark-done tick the attempt should be Done, got %q", a.State)
	}
	hasDone := false
	for _, e := range a.Events {
		if e.Type == "done" {
			hasDone = true
		}
	}
	if !hasDone {
		return fmt.Errorf("a `done` event should have been appended")
	}
	// The response is the re-rendered board region; the card must now sit in the Done
	// column, not Review.
	body, _ := w.get("bcdoneresp").(string)
	if !bcCardInColumn(body, "col-done", b.ticket, bcAtt) {
		return fmt.Errorf("the re-rendered board should place the card in the Done column\n%s", body)
	}
	return shoot(w, sr, "/", "board-controls-card-moved-to-done", true, "", "")
}

func thenBCNoDoneTick(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.url("/"))
	if err != nil {
		return err
	}
	if strings.Contains(body, "markdone-btn-"+b.ticket+"-"+bcAtt) {
		return fmt.Errorf("a card with a non-green promotion must not show the mark-done tick\n%s", body)
	}
	return nil
}

func thenBCDonePostRefused(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	resp, err := http.Post(b.url("/ticket/"+b.ticket+"/"+bcAtt+"/done"), "application/x-www-form-urlencoded", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("a done POST for a non-green card must be refused (400), got %d\n%s", resp.StatusCode, raw)
	}
	// And it must have written nothing: the attempt stays in Review.
	a, err := project.LoadAttempt(b.root, b.ticket, bcAtt)
	if err != nil {
		return err
	}
	if a.State != project.Review {
		return fmt.Errorf("a refused done POST must not change state; want Review, got %q", a.State)
	}
	return nil
}

func thenBCSystemLogStartedThenPass(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	events, err := bcReadSystemLog(b.root, b.ticket, bcAtt)
	if err != nil {
		return err
	}
	// The stream must carry, in order, a `started` line then a `pass` line for prod —
	// the full arc the spinner mirrors locally.
	startIdx, passIdx := -1, -1
	for i, e := range events {
		if e.Fields["env"] != "prod" {
			continue
		}
		switch e.Fields["verdict"] {
		case "started":
			if startIdx < 0 {
				startIdx = i
			}
		case "pass":
			passIdx = i
		}
	}
	if startIdx < 0 {
		return fmt.Errorf("System Logs should carry a `started` line for prod, got %+v", events)
	}
	if passIdx < 0 {
		return fmt.Errorf("System Logs should carry a `pass` line for prod, got %+v", events)
	}
	if startIdx >= passIdx {
		return fmt.Errorf("the `started` line must precede the `pass` line (started@%d, pass@%d)", startIdx, passIdx)
	}
	// Each line owns its own recorded timestamp.
	for _, e := range events {
		if strings.TrimSpace(e.TS) == "" {
			return fmt.Errorf("every System Logs line must be stamped with its recorded time, got %+v", e)
		}
	}
	return nil
}

// --- small helpers ---

// bcSysEvent mirrors systemlog.SystemEvent's wire shape for reading system.jsonl back.
type bcSysEvent struct {
	TS      string            `json:"ts"`
	Class   string            `json:"class"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields"`
	Error   bool              `json:"error"`
}

func bcReadSystemLog(root store.Root, ticket, att string) ([]bcSysEvent, error) {
	f, err := os.Open(root.SessionSystemLogPath(ticket, att))
	if err != nil {
		return nil, fmt.Errorf("open system.jsonl: %w", err)
	}
	defer f.Close()
	var out []bcSysEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e bcSysEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("decode system.jsonl line: %w", err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// bcCardInColumn reports whether the card for ticket/att renders inside the named board
// column section. It slices the board HTML from the column's data-testid to the next
// column boundary and looks for the card testid within that span.
func bcCardInColumn(body, colTestid, ticket, att string) bool {
	marker := `data-testid="` + colTestid + `"`
	start := strings.Index(body, marker)
	if start < 0 {
		return false
	}
	rest := body[start+len(marker):]
	// The next column starts at the next `col col-` section; bound the slice there.
	if next := strings.Index(rest, `class="col col-`); next >= 0 {
		rest = rest[:next]
	}
	return strings.Contains(rest, "card-"+ticket+"-"+att)
}

// bcAssertStyleContains fetches the served stylesheet and asserts it carries the needle,
// so the spinner's reveal rule ships with the board rather than being asserted only in
// the template.
func bcAssertStyleContains(b *bcBoard, needle string) error {
	body, err := promGetBody(b.url("/static/style.css"))
	if err != nil {
		return err
	}
	if !strings.Contains(body, needle) {
		return fmt.Errorf("style.css should carry %q (the in-flight spinner reveal rule)", needle)
	}
	return nil
}
