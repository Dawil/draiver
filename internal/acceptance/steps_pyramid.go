package acceptance

import (
	"context"
	"errors"
	"fmt"
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

// steps_pyramid.go realises drv-025's acceptance criteria
// (features/test-pyramid/required-gate.feature): the per-rung `required` flag
// decouples the review gate from the pyramid's top rung. Each scenario stands up a
// real managed worktree carrying a committed .test-pyramid.yaml with an advisory
// rung, logs the attempt's test-result events at HEAD, then evaluates the ACTUAL
// review-gate fold (pyramid.RequiredStatus over those events — the same helper
// cmd/review.go gates on) and asserts admit/refuse. The evidence screenshot is the
// live board's pyramid badge over that worktree, so the rendered state the reviewer
// sees is the one the gate judged. The drv-025 steps register into the shared
// registry from an init(), leaving the earlier features untouched.

const (
	pyrTicket = "PYR-1"
	pyrAtt    = "0001"
)

func init() {
	drv025 := map[string]StepFunc{
		"a pyramid with an advisory integration rung above required unit and acceptance": givenAdvisoryAboveRequired,
		"a pyramid requiring unit and acceptance":                                        givenRequiringUnitAcceptance,
		"a pyramid with required e2e above an advisory integration rung":                 givenRequiredAboveAdvisory,
		"the required rungs unit and acceptance are logged green at HEAD":                loggedGreen("acceptance"),
		"only unit is logged green at HEAD":                                              loggedGreen("unit"),
		"unit and e2e are logged green at HEAD":                                          loggedGreen("e2e"),
		"the review gate evaluates the attempt":                                          whenGateEvaluates,
		"the gate admits the claim with integration never run":                           thenGateAdmits("integration"),
		"the gate admits the claim across the advisory rung":                             thenGateAdmits("integration"),
		"the gate refuses the claim and names acceptance outstanding":                    thenGateRefuses("acceptance"),
	}
	for text, fn := range drv025 {
		registry[text] = fn
	}
}

// pyramidGateBoard is a board plus the git/log facts a drv-025 scenario folds: the
// attempt's checkout, its HEAD commit, and the parsed pyramid whose required set the
// gate enforces.
type pyramidGateBoard struct {
	*board
	checkout string
	head     string
	pyr      *pyramid.Pyramid
}

// The three scenario ladders. Altitude (list order) is unit < acceptance <
// integration and unit < integration < e2e; `required` is the orthogonal
// enforcement flag, so integration is advisory wherever it appears.
const (
	advisoryTopPyramid = `levels:
  - name: unit
    run: "true"
  - name: acceptance
    run: "true"
  - name: integration
    run: "true"
    required: false
`
	bothRequiredPyramid = `levels:
  - name: unit
    run: "true"
  - name: acceptance
    run: "true"
`
	requiredAboveAdvisoryPyramid = `levels:
  - name: unit
    run: "true"
  - name: integration
    run: "true"
    required: false
  - name: e2e
    run: "true"
`
)

// seedPyramidGateBoard builds a live board over a PYR-1/0001 attempt backed by a
// real repo + managed worktree whose committed .test-pyramid.yaml is pyramidYAML.
// XDG_CACHE_HOME is redirected to a scenario-scoped temp dir (restored on cleanup)
// so the managed worktree the web layer resolves is the one seeded here, mirroring
// seedGitBoard. HEAD is the pyramid commit; the tree is clean, so a test-result
// logged at HEAD renders the badge green.
func seedPyramidGateBoard(w *World, pyramidYAML string) (*pyramidGateBoard, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not on PATH")
	}
	p, err := pyramid.Parse([]byte(pyramidYAML))
	if err != nil {
		return nil, fmt.Errorf("seed pyramid invalid: %w", err)
	}

	cache, err := os.MkdirTemp("", "pyr-cache-")
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

	repo, err := os.MkdirTemp("", "pyr-repo-")
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

	// Cut the attempt's managed worktree + branch and commit the pyramid onto it, so
	// the web layer resolves this same worktree and HEAD is the pyramid commit.
	m, err := worktree.NewManager(repo)
	if err != nil {
		return nil, err
	}
	key := worktree.Key{Ticket: pyrTicket, Attempt: pyrAtt}
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

	dataDir, err := os.MkdirTemp("", "pyr-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(pyrTicket, pyrAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + pyrTicket + "\ntitle: drv-025 required gate\n---\n\n# drv-025 required gate\n\n## Acceptance criteria\n\n- Advisory rungs never block the gate.\n"
	if err := os.WriteFile(root.SpecPath(pyrTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + pyrAtt + "\nticket: " + pyrTicket + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(pyrTicket, pyrAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	if _, err := ticketlog.Append(root, pyrTicket, pyrAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return nil, err
	}

	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	b := &pyramidGateBoard{board: &board{root: root, wd: repo, srv: ts}, checkout: checkout, head: head, pyr: p}
	// shoot() resolves the live server via the shared "board" key, so expose the
	// embedded board there too (the drv-025 facts live under "pyrboard").
	w.set("board", b.board)
	return b, nil
}

func currentPyramidBoard(w *World) (*pyramidGateBoard, error) {
	b, _ := w.get("pyrboard").(*pyramidGateBoard)
	if b == nil {
		return nil, errors.New("no pyramid gate board set up in scope")
	}
	return b, nil
}

func givenAdvisoryAboveRequired(w *World, sr *StepRun) error {
	b, err := seedPyramidGateBoard(w, advisoryTopPyramid)
	if err != nil {
		return err
	}
	w.set("pyrboard", b)
	return nil
}

func givenRequiringUnitAcceptance(w *World, sr *StepRun) error {
	b, err := seedPyramidGateBoard(w, bothRequiredPyramid)
	if err != nil {
		return err
	}
	w.set("pyrboard", b)
	return nil
}

func givenRequiredAboveAdvisory(w *World, sr *StepRun) error {
	b, err := seedPyramidGateBoard(w, requiredAboveAdvisoryPyramid)
	if err != nil {
		return err
	}
	w.set("pyrboard", b)
	return nil
}

// loggedGreen records a green test-result for rung at HEAD — exactly what
// `draiver test --log` appends when it climbs to that rung. Logging the highest
// reached rung covers every required rung at or below it, which is what the gate
// fold relies on.
func loggedGreen(rung string) StepFunc {
	return func(w *World, sr *StepRun) error {
		b, err := currentPyramidBoard(w)
		if err != nil {
			return err
		}
		_, err = ticketlog.Append(b.root, pyrTicket, pyrAtt, event.Event{
			Type:   "test-result",
			Actor:  "agent:acceptance",
			Rung:   rung,
			Commit: b.head,
			Body:   fmt.Sprintf("Rung `%s` green at HEAD.", rung),
		})
		return err
	}
}

// whenGateEvaluates folds the attempt's logged test-results over the required set at
// HEAD — the same computation cmd/review.go's reviewGate performs — and stores the
// verdict for the Then step to assert.
func whenGateEvaluates(w *World, sr *StepRun) error {
	b, err := currentPyramidBoard(w)
	if err != nil {
		return err
	}
	events, err := ticketlog.Read(b.root, pyrTicket, pyrAtt)
	if err != nil {
		return err
	}
	var results []pyramid.Result
	for _, e := range events {
		if e.Type == "test-result" {
			results = append(results, pyramid.Result{Rung: e.Rung, Commit: e.Commit})
		}
	}
	satisfied, outstanding := b.pyr.RequiredStatus(results, b.head)
	w.set("gate_satisfied", satisfied)
	w.set("gate_outstanding", outstanding)
	return nil
}

func gateOutstanding(w *World) []pyramid.Level {
	o, _ := w.get("gate_outstanding").([]pyramid.Level)
	return o
}

// shootBadge captures the live board's pyramid badge — the rendered verification
// state the reviewer sees — as the scenario's artefact.
func shootBadge(w *World, sr *StepRun, name string) error {
	return shoot(w, sr, "/ticket/"+pyrTicket+"/"+pyrAtt, name, true, "", "")
}

// thenGateAdmits asserts the gate admits (no outstanding required rung) and that the
// named advisory rung is not what gated — it was not required.
func thenGateAdmits(advisory string) StepFunc {
	return func(w *World, sr *StepRun) error {
		if out := gateOutstanding(w); len(out) != 0 {
			return fmt.Errorf("gate should admit, but %q is outstanding", quotedLevelNames(out))
		}
		b, err := currentPyramidBoard(w)
		if err != nil {
			return err
		}
		for _, lv := range b.pyr.Levels {
			if lv.Name == advisory && lv.IsRequired() {
				return fmt.Errorf("advisory rung %q should not be required", advisory)
			}
		}
		return shootBadge(w, sr, "gate-admitted-"+advisory)
	}
}

// thenGateRefuses asserts the gate refuses and that want is among the outstanding
// required rungs the refusal names.
func thenGateRefuses(want string) StepFunc {
	return func(w *World, sr *StepRun) error {
		out := gateOutstanding(w)
		if len(out) == 0 {
			return errors.New("gate should refuse, but no required rung is outstanding")
		}
		found := false
		for _, lv := range out {
			if lv.Name == want {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("refusal should name %q outstanding, got %s", want, quotedLevelNames(out))
		}
		return shootBadge(w, sr, "gate-refused-"+want)
	}
}

func quotedLevelNames(levels []pyramid.Level) string {
	names := make([]string, len(levels))
	for i, lv := range levels {
		names[i] = lv.Name
	}
	return strings.Join(names, ", ")
}
