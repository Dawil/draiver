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

	"github.com/Dawil/draiver/internal/bddexec"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/worktree"
)

// steps_reportfidelity.go realises drv-030's acceptance criteria over a live board: a
// rerun must regenerate the report for the code committed on the attempt's BRANCH (not
// the shared main checkout), stamp the real reviewed commit, and stay non-terminal; and
// the report's tag facet bar must be ordered alphabetically. These steps register
// themselves into the shared registry from an init(), leaving the earlier steps intact.

func init() {
	drv030 := map[string]StepFunc{
		// features/bdd-report/rerun.feature (drv-030)
		"a bound BDD rung whose work is committed only on the attempt branch": givenBranchOnlyRung,
		"the regenerated report matches the reviewed code and names its commit": thenReportMatchesReviewed,
		// features/bdd-report/facets.feature (drv-030)
		"a captured BDD run whose scenarios carry tags in non-alphabetical count order": givenTaggedRun,
		"I view the rendered BDD report":                                                whenViewReport,
		"the tag facet bar is ordered alphabetically by tag name":                       thenFacetsAlphabetical,
	}
	for text, fn := range drv030 {
		registry[text] = fn
	}
}

// taggedBranchCucumber is the cucumber-JSON committed on the attempt BRANCH for the
// fidelity scenario: an acceptance feature tagged @drv-030, present only at the
// reviewed commit. The main checkout carries seedCucumberJSON instead (no such tag), so
// a rerun that honoured the reviewed commit regenerates THIS, and one that ran against
// main would not.
const taggedBranchCucumber = `[{"uri":"features/manager-signal/board.feature","keyword":"Feature",` +
	`"name":"The board surfaces the manager signal","tags":[{"name":"@drv-030"}],` +
	`"elements":[{"keyword":"Scenario","name":"surfaced","type":"scenario","tags":[{"name":"@drv-030"}],` +
	`"steps":[{"keyword":"Then ","name":"it shows","result":{"status":"passed","duration":1000000}}]}]}]`

// seedRerunBoard builds a live board over a Running ACC-1/0001 attempt backed by a real
// repo + managed worktree, with the pyramid committed on main AND the branch, and
// branchCucumber committed as cucumber.json on the attempt branch (main keeps
// seedCucumberJSON). It is the git-backed counterpart of seedBoard: bddexec.Rerun now
// resolves the attempt's branch and cuts a detached worktree at its tip, so a rerun
// needs a real repo, not a loose temp dir.
//
// XDG_CACHE_HOME is redirected to a scenario-scoped temp dir (restored on cleanup) so
// the managed worktree the web layer resolves — and the detached worktree the rerun cuts
// — live under temp, never the real cache. Scenarios run sequentially, so the env swap
// is race-free (the seedGitBoard contract).
func seedRerunBoard(w *World, pyramidYAML, branchCucumber string) (*board, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not on PATH")
	}
	cache, err := os.MkdirTemp("", "rerun-cache-")
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

	repo, err := os.MkdirTemp("", "rerun-repo-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(repo) })
	if err := gitcAcc(repo, "init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	if err := gitcAcc(repo, "config", "user.email", "t@t"); err != nil {
		return nil, err
	}
	if err := gitcAcc(repo, "config", "user.name", "t"); err != nil {
		return nil, err
	}
	// The pyramid + a baseline report on main: the pyramid is what the rerun-button gate
	// (attemptHasBDDRung) reads from the main checkout, and the baseline is what a rerun
	// that ignored the reviewed commit would wrongly capture.
	if err := os.WriteFile(filepath.Join(repo, ".test-pyramid.yaml"), []byte(pyramidYAML), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(repo, "cucumber.json"), []byte(seedCucumberJSON), 0o644); err != nil {
		return nil, err
	}
	if err := gitcAcc(repo, "add", "-A"); err != nil {
		return nil, err
	}
	if err := gitcAcc(repo, "commit", "-q", "-m", "base"); err != nil {
		return nil, err
	}

	// Cut the attempt's managed worktree + branch (from main) and commit the reviewed
	// work — branchCucumber — onto the branch only.
	wm, err := worktree.NewManager(repo)
	if err != nil {
		return nil, err
	}
	key := worktree.Key{Ticket: accTicket, Attempt: accAtt}
	wt, err := wm.Create(context.Background(), worktree.Spec{Key: key})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "cucumber.json"), []byte(branchCucumber), 0o644); err != nil {
		return nil, err
	}
	if err := gitcAcc(wt.Path, "add", "-A"); err != nil {
		return nil, err
	}
	// --allow-empty: when branchCucumber matches main's baseline (the green/red
	// scenarios) there is no file diff, but the branch still gets its own commit — a
	// real reviewed-commit SHA distinct from main, which the rerun must stamp.
	if err := gitcAcc(wt.Path, "commit", "-q", "--allow-empty", "-m", "attempt work"); err != nil {
		return nil, err
	}

	dataDir, err := os.MkdirTemp("", "rerun-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(accTicket, accAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + accTicket + "\ntitle: drv-030 rerun fidelity\n---\n\n# drv-030 rerun fidelity\n"
	if err := os.WriteFile(root.SpecPath(accTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + accAtt + "\nticket: " + accTicket + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(accTicket, accAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	if _, err := ticketlog.Append(root, accTicket, accAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return nil, err
	}

	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	return &board{root: root, wd: repo, srv: ts}, nil
}

// givenBranchOnlyRung seeds a green-rung board whose reviewed acceptance feature
// (tagged @drv-030) lives only on the attempt branch — the drv-030 bug-1 setup.
func givenBranchOnlyRung(w *World, sr *StepRun) error {
	b, err := seedRerunBoard(w, greenPyramid, taggedBranchCucumber)
	if err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

// thenReportMatchesReviewed proves the regenerated report reflects the branch commit:
// the @drv-030-tagged feature (present only on the branch) appears, the capture is
// stamped with the real reviewed commit (never "nocommit"), and the attempt stays
// Running (non-terminal).
func thenReportMatchesReviewed(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	out, _ := w.get("outcome").(bddexec.Outcome)
	if out.Status != bddexec.StatusOK {
		return fmt.Errorf("status = %q, want ok (%v)", out.Status, out.Lines)
	}

	runs, err := report.DiscoverRuns(b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	var sawTaggedFeature bool
	var realCommit bool
	for _, run := range runs {
		if run.Commit != "" && run.Commit != "nocommit" {
			realCommit = true
		}
		rep, err := run.LoadReport()
		if err != nil {
			return err
		}
		for _, f := range rep.Features {
			if f.Name != "The board surfaces the manager signal" {
				continue
			}
			for _, tg := range f.Tags {
				if tg.Name == "@drv-030" {
					sawTaggedFeature = true
				}
			}
		}
	}
	if !sawTaggedFeature {
		return errors.New("regenerated report lacks the branch-only @drv-030 feature — the rerun ran against the wrong tree")
	}
	if !realCommit {
		return errors.New("no captured run is stamped with a real reviewed commit (got only nocommit)")
	}

	// Non-terminal: a rerun never flips the Review gate.
	a, err := project.LoadAttempt(b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	if a.State != project.Running {
		return fmt.Errorf("rerun is non-terminal, but the attempt is %q (want Running)", a.State)
	}

	// Screenshot the refreshed board carrying the regenerated run (BDD tab).
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt, "rerun-reviewed-commit", true, `[data-testid="attempt-tab-bdd"]`, `[data-testid="attempt-tab-bdd"]`)
}

// facetCucumber is a captured run whose tags, by scenario count, are @zebra (2) >
// @mango (1) = @apple (1): count-ordering would lead with @zebra, so alphabetical
// ordering is observably different.
const facetCucumber = `[{"uri":"features/f.feature","keyword":"Feature","name":"Facet ordering","elements":[
  {"keyword":"Scenario","name":"one","type":"scenario","tags":[{"name":"@zebra"},{"name":"@apple"}],
   "steps":[{"keyword":"Then ","name":"ok","result":{"status":"passed","duration":1000000},
     "embeddings":[{"mime_type":"image/png","data":"` + onePxPNG + `","name":"shot"}]}]},
  {"keyword":"Scenario","name":"two","type":"scenario","tags":[{"name":"@zebra"},{"name":"@mango"}],
   "steps":[{"keyword":"Then ","name":"ok","result":{"status":"passed","duration":1000000}}]}]}]`

// givenTaggedRun seeds a board (non-git — this scenario only renders a report, never
// reruns) with a single captured run carrying the facetCucumber tags.
func givenTaggedRun(w *World, sr *StepRun) error {
	b, err := seedBoard(w, greenPyramid)
	if err != nil {
		return err
	}
	dir := filepath.Join(b.root.ArtefactsDir(accTicket, accAtt), report.BDDArtefactSubdir, accRung, accEnv, accCommit, "20260101T120000Z")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "cucumber.json"), []byte(facetCucumber), 0o644); err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

// whenViewReport fetches the rendered BDD report HTML the webui serves for the
// selected run and stashes it for the Then assertion.
func whenViewReport(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	// The facet scenario seeds exactly one captured run, so the report route's default
	// (latest run) renders it — no run selector needed.
	resp, err := http.Get(b.url("/ticket/" + accTicket + "/" + accAtt + "/report"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("report status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	w.set("reportHTML", string(body))
	return nil
}

// thenFacetsAlphabetical asserts the facet chips render in alphabetical tag order —
// @apple, @mango, @zebra — regardless of scenario count (drv-030 bug 2).
func thenFacetsAlphabetical(w *World, sr *StepRun) error {
	html, _ := w.get("reportHTML").(string)
	iApple := strings.Index(html, `data-tag="@apple"`)
	iMango := strings.Index(html, `data-tag="@mango"`)
	iZebra := strings.Index(html, `data-tag="@zebra"`)
	if iApple < 0 || iMango < 0 || iZebra < 0 {
		return fmt.Errorf("facet chips missing: apple=%d mango=%d zebra=%d", iApple, iMango, iZebra)
	}
	if !(iApple < iMango && iMango < iZebra) {
		return fmt.Errorf("facets not alphabetical: positions apple=%d mango=%d zebra=%d (want apple<mango<zebra)", iApple, iMango, iZebra)
	}
	// Screenshot the rendered report page showing the ordered facet bar.
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt+"/report", "facets-alphabetical", true, `[data-testid="tag-facets"]`, "")
}

// gitcAcc runs a git command in dir, returning an error (not failing a *testing.T) so
// it is usable from the step functions. Mirrors steps_git.go's gitc.
func gitcAcc(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return nil
}
