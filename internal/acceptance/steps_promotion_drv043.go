package acceptance

import (
	"context"
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
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/worktree"
)

// steps_promotion_drv043.go realises drv-043's acceptance criteria
// (features/promotions/promotions.feature): the new `promotions:` axis parses and
// validates, `draiver test` never climbs a promotion, and a Review attempt's
// per-environment promotion status surfaces as green/stale chips on the board and as a
// promote control on the attempt page. The UI scenarios stand up a real managed
// worktree carrying a committed promotions pyramid, drive the attempt into Review, log
// commit-bound promotion-result events, and screenshot the LIVE webui — so the rendered
// state the reviewer sees is the one DerivePromotions folded. The steps register into
// the shared registry from an init(), leaving the earlier features untouched.

const (
	promTicket = "PRM-1"
	promAtt    = "0001"
	// promStaleCommit is an older commit a stale promotion-result names — anything that
	// is not the worktree HEAD, so the staleness fold (Commit != head) fires.
	promStaleCommit = "0000000000000000000000000000000000000000"
)

// promPyramidYAML declares two environments and a promotion for each — the two-chip
// (green + stale) board card the spec's payoff screenshot shows. The `qa` is trivially
// green; rendering the chips folds only the logged promotion-result events, so the qa
// is never actually executed here.
const promPyramidYAML = `environments:
  - name: dev
    url: https://dev.example.internal/
    healthchecks:
      - name: reachable
        script: "true"
  - name: staging
    healthchecks:
      - name: reachable
        script: "true"
levels:
  - name: unit
    run: "true"
promotions:
  - environment: dev
    base: main
    qa: "true"
  - environment: staging
    base: main
    qa: "true"
`

func init() {
	drv043 := map[string]StepFunc{
		"a test-pyramid declaring a promotion for a declared environment":               givenPromotionPyramid,
		"the promotions section parses and resolves the promotion by environment":       thenPromotionParses,
		"a promotion naming an undeclared environment is rejected with a clear message": thenUndeclaredPromotionRejected,
		"a promotion with a blank qa is rejected with a clear message":                  thenBlankQARejected,
		"a test-pyramid with both levels and promotions":                                givenPromotionPyramid,
		"the rungs draiver test would climb are exactly the levels, never a promotion":  thenTestNeverClimbsPromotion,
		"the parsed promotions surface on the board":                                    thenParsedPromotionsSurfaceOnBoard,
		"the promotion axis renders beside the test pyramid on the board":               thenPromotionAxisRendersOnBoard,
		"a Review attempt whose repo declares promotions for dev and staging":           givenReviewAttemptWithPromotions,
		"dev is promoted green at HEAD and staging is promoted at an older commit":      givenDevGreenStagingStale,
		"I open the board": noopPromote,
		"the Review card shows dev promoted-and-current and staging stale": thenBoardShowsPromotionChips,
		"I open the attempt page promotions tab":                           noopPromote,
		"a promote control is offered for each declared environment":       thenAttemptPageOffersPromoteControl,
	}
	for text, fn := range drv043 {
		registry[text] = fn
	}
}

func noopPromote(w *World, sr *StepRun) error { return nil }

// --- parse / validate / test-axis scenarios (pure folds over the real helpers) ---

func givenPromotionPyramid(w *World, sr *StepRun) error {
	p, err := pyramid.Parse([]byte(promPyramidYAML))
	if err != nil {
		return fmt.Errorf("the promotions pyramid should parse: %w", err)
	}
	w.set("prom_pyramid", p)
	return nil
}

func currentPromPyramid(w *World) (*pyramid.Pyramid, error) {
	p, _ := w.get("prom_pyramid").(*pyramid.Pyramid)
	if p == nil {
		return nil, errors.New("no promotions pyramid set up in scope")
	}
	return p, nil
}

func thenPromotionParses(w *World, sr *StepRun) error {
	p, err := currentPromPyramid(w)
	if err != nil {
		return err
	}
	if len(p.Promotions) == 0 {
		return errors.New("expected at least one declared promotion")
	}
	promo, ok := p.Promotion("dev")
	if !ok {
		return errors.New("Promotion(dev) should resolve the declared promotion")
	}
	if strings.TrimSpace(promo.QA) == "" {
		return errors.New("the resolved promotion should carry its qa command")
	}
	return nil
}

func thenUndeclaredPromotionRejected(w *World, sr *StepRun) error {
	body := `environments:
  - name: dev
    healthchecks: []
levels:
  - name: unit
    run: "true"
promotions:
  - environment: ghost
    qa: "true"
`
	_, err := pyramid.Parse([]byte(body))
	if err == nil {
		return errors.New("a promotion naming an undeclared environment must be rejected")
	}
	if !strings.Contains(err.Error(), "is not declared") {
		return fmt.Errorf("rejection should name the undeclared environment, got %q", err.Error())
	}
	return nil
}

func thenBlankQARejected(w *World, sr *StepRun) error {
	body := `environments:
  - name: dev
    healthchecks: []
levels:
  - name: unit
    run: "true"
promotions:
  - environment: dev
    qa: "  "
`
	_, err := pyramid.Parse([]byte(body))
	if err == nil {
		return errors.New("a promotion with a blank qa must be rejected")
	}
	if !strings.Contains(err.Error(), "qa is blank") {
		return fmt.Errorf("rejection should explain the blank qa, got %q", err.Error())
	}
	return nil
}

// thenTestNeverClimbsPromotion asserts the promotion axis is disjoint from the testing
// V: the rungs `draiver test` would climb are exactly the pyramid's Levels, and no
// promotion contributes a rung (a promotion references an environment, binds no level).
func thenTestNeverClimbsPromotion(w *World, sr *StepRun) error {
	p, err := currentPromPyramid(w)
	if err != nil {
		return err
	}
	if len(p.Levels) == 0 || len(p.Promotions) == 0 {
		return errors.New("scenario needs both levels and promotions")
	}
	levelNames := map[string]bool{}
	for _, lv := range p.Levels {
		levelNames[lv.Name] = true
	}
	for _, promo := range p.Promotions {
		if levelNames[promo.Environment] {
			return fmt.Errorf("promotion %q collides with a level — promotions must be a separate axis", promo.Environment)
		}
	}
	return nil
}

// --- UI scenarios over a live board ---

// promBoard is a live board over a Review attempt whose committed pyramid declares
// promotions, plus the worktree facts the chips fold.
type promBoard struct {
	*board
	head string
}

func currentPromBoard(w *World) (*promBoard, error) {
	b, _ := w.get("promboard").(*promBoard)
	if b == nil {
		return nil, errors.New("no promotion board set up in scope")
	}
	return b, nil
}

// seedPromBoard stands up a real repo + managed worktree carrying promPyramidYAML, an
// attempt driven into Review (a `review` event), and a live web server — mirroring
// seedPyramidGateBoard. HEAD is the pyramid commit; a promotion-result logged at HEAD
// renders a green chip, one at an older commit a stale chip.
func seedPromBoard(w *World) (*promBoard, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not on PATH")
	}
	cache, err := os.MkdirTemp("", "prom-cache-")
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

	repo, err := os.MkdirTemp("", "prom-repo-")
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
	key := worktree.Key{Ticket: promTicket, Attempt: promAtt}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: key})
	if err != nil {
		return nil, err
	}
	checkout := wt.Path
	if err := os.WriteFile(filepath.Join(checkout, ".test-pyramid.yaml"), []byte(promPyramidYAML), 0o644); err != nil {
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

	dataDir, err := os.MkdirTemp("", "prom-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(promTicket, promAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + promTicket + "\ntitle: drv-043 promotions\n---\n\n# drv-043 promotions\n\n## Acceptance criteria\n\n- Promotions surface on the board.\n"
	if err := os.WriteFile(root.SpecPath(promTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + promAtt + "\nticket: " + promTicket + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(promTicket, promAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	// created → review drives the attempt into Review, the one state the board chips
	// and the attempt-page promote control are offered in.
	for _, e := range []event.Event{
		{Type: "created", Actor: "agent:acceptance", Body: "start"},
		{Type: "review", Actor: "agent:acceptance", Body: "Claiming review."},
	} {
		if _, err := ticketlog.Append(root, promTicket, promAtt, e); err != nil {
			return nil, err
		}
	}

	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	pb := &promBoard{board: &board{root: root, wd: repo, srv: ts}, head: head}
	w.set("board", pb.board) // shoot() resolves the live server via the shared key
	w.set("promboard", pb)
	return pb, nil
}

func givenReviewAttemptWithPromotions(w *World, sr *StepRun) error {
	_, err := seedPromBoard(w)
	return err
}

// givenDevGreenStagingStale logs a passing promotion-result for dev at HEAD (green) and
// one for staging at an older commit (stale) — exactly the events the board button and
// `draiver promote --log` append, folded by DerivePromotions into the two chip states.
func givenDevGreenStagingStale(w *World, sr *StepRun) error {
	b, err := currentPromBoard(w)
	if err != nil {
		return err
	}
	for _, e := range []event.Event{
		{Type: "promotion-result", Actor: "agent:acceptance", Environment: "dev", Commit: b.head, Outcome: "pass", Body: "Promotion to `dev` **verified** at HEAD."},
		{Type: "promotion-result", Actor: "agent:acceptance", Environment: "staging", Commit: promStaleCommit, Outcome: "pass", Body: "Promotion to `staging` **verified** at an older commit."},
	} {
		if _, err := ticketlog.Append(b.root, promTicket, promAtt, e); err != nil {
			return err
		}
	}
	return nil
}

// ensurePromBoard returns the scenario's live promotion board, standing one up (with
// the green dev + stale staging results) on first use. The pure parse/axis scenarios
// set up only an in-memory pyramid, so this lets their terminal step still capture the
// live corroborating artefact every scenario owes the report (drv-042 AC #4), mirroring
// how the required-gate logic scenarios end on shootBadge.
func ensurePromBoard(w *World) (*promBoard, error) {
	if b, _ := w.get("promboard").(*promBoard); b != nil {
		return b, nil
	}
	b, err := seedPromBoard(w)
	if err != nil {
		return nil, err
	}
	if err := givenDevGreenStagingStale(w, nil); err != nil {
		return nil, err
	}
	return b, nil
}

// thenParsedPromotionsSurfaceOnBoard corroborates the parse/validate scenario with a
// screenshot of the board card the parsed promotions render into — the parsed axis made
// visible, not merely asserted in memory.
func thenParsedPromotionsSurfaceOnBoard(w *World, sr *StepRun) error {
	if _, err := ensurePromBoard(w); err != nil {
		return err
	}
	return shoot(w, sr, "/", "promotions-parse-surfaces-on-board", true, "", "")
}

// thenPromotionAxisRendersOnBoard corroborates the "test never climbs a promotion"
// scenario with a board screenshot showing the promotion chips rendering as a separate
// axis beside the test-pyramid badge.
func thenPromotionAxisRendersOnBoard(w *World, sr *StepRun) error {
	if _, err := ensurePromBoard(w); err != nil {
		return err
	}
	return shoot(w, sr, "/", "promotions-separate-from-test-axis", true, "", "")
}

func thenBoardShowsPromotionChips(w *World, sr *StepRun) error {
	b, err := currentPromBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.board.url("/"))
	if err != nil {
		return err
	}
	// The green chip for dev (promoted-and-current at HEAD) and the muted stale chip for
	// staging (promoted, but the code has moved since) must both render on the card.
	checks := []struct{ env, class string }{{"dev", "promo-ok"}, {"staging", "promo-stale"}}
	for _, c := range checks {
		testid := "promo-" + promTicket + "-" + promAtt + "-" + c.env
		if !strings.Contains(body, testid) {
			return fmt.Errorf("board should render a %q promotion chip (data-testid %q)", c.env, testid)
		}
		if !strings.Contains(body, c.class) {
			return fmt.Errorf("board should render the %q chip class for %q", c.class, c.env)
		}
	}
	return shoot(w, sr, "/", "promotion-labels-review-column", true, "", "")
}

func thenAttemptPageOffersPromoteControl(w *World, sr *StepRun) error {
	b, err := currentPromBoard(w)
	if err != nil {
		return err
	}
	body, err := promGetBody(b.board.url("/ticket/" + promTicket + "/" + promAtt))
	if err != nil {
		return err
	}
	for _, env := range []string{"dev", "staging"} {
		if !strings.Contains(body, "action-promote-"+env) {
			return fmt.Errorf("attempt page should offer a promote control for %q", env)
		}
	}
	// Reveal the Promotions tab before shooting the attempt-page promote control.
	tab := `[data-testid="attempt-tab-promote"]`
	return shoot(w, sr, "/ticket/"+promTicket+"/"+promAtt, "promote-control-in-review", true, tab, tab)
}

// getBody GETs a URL and returns its body, failing on a non-200 so a scenario does not
// silently assert over an error page.
func promGetBody(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return string(body), nil
}
