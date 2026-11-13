package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/Dawil/draiver/internal/changelog"
)

// steps_changelog_drv052.go realises drv-052's acceptance criteria
// (features/changelog/changelog-fragments.feature): the `changelog` rung over
// per-ticket fragments, the deterministic generator, and the migration invariant that
// the committed CHANGELOG.md is reproduced from fragments alone.
//
// The rung scenarios drive the real draiver binary (`draiver test changelog`) in a
// scratch git repo on a draiver/<ticket>/<attempt> branch, whose pyramid runs the real
// checker (cmd/changelog, built once) — so they prove draiver's $DRAIVER_TICKET export
// end to end, not just the checker in isolation. No browser, no screenshots.

const (
	drv052Ticket = "drv-900"
	drv052Branch = "draiver/" + drv052Ticket + "/0001"
)

func init() {
	steps := map[string]StepFunc{
		"a scratch attempt repo for drv-900 with a changelog rung":               drv052GivenScratchRepo,
		`the drv-900 fragment is a valid "Added" entry`:                          drv052Frag(drv052Ticket, "---\nsection: Added\n---\n**A scratch change (drv-900).** Prose.\n"),
		`the drv-900 fragment is a skip with reason "test-only refactor"`:        drv052Frag(drv052Ticket, "---\nskip: test-only refactor\n---\n"),
		"the drv-900 fragment has an unknown section":                            drv052Frag(drv052Ticket, "---\nsection: Improved\n---\n**A scratch change (drv-900).**\n"),
		"the drv-900 fragment has an empty body":                                 drv052Frag(drv052Ticket, "---\nsection: Added\n---\n\n"),
		"the drv-900 fragment is a skip with an empty reason":                    drv052Frag(drv052Ticket, "---\nskip:\n---\n"),
		"another ticket's fragment has an unknown section":                       drv052Frag("drv-901", "---\nsection: Tweaked\n---\n**Other (drv-901).**\n"),
		"the scratch repo is on its main branch":                                 drv052GivenOnMain,
		"I run draiver test on the changelog rung":                               drv052WhenDraiverTest,
		"I run the changelog check by hand":                                      drv052WhenHandCheck,
		"the changelog rung fails":                                               drv052ThenRung(false),
		"the changelog rung passes":                                              drv052ThenRung(true),
		"a scratch fragment tree spanning several sections, prefixes and a skip": drv052GivenTree,
		"I generate CHANGELOG.md":                                                drv052WhenGenerate("first"),
		"I generate CHANGELOG.md again":                                          drv052WhenGenerate("second"),
		"the sections appear in Keep-a-Changelog order":                          drv052ThenSectionOrder,
		"entries within a section are ordered by ticket prefix then number":      drv052ThenEntryOrder,
		"the skip fragment renders nothing":                                      drv052ThenSkipOmitted,
		"the two renders are byte-identical":                                     drv052ThenIdempotent,
		"the generator's staleness check reports CHANGELOG.md current":           drv052ThenStaleCheck,
		"the repository's changelog.d fragments and its committed CHANGELOG.md":  drv052GivenRepoTree,
		"I render CHANGELOG.md from the fragments it covers":                     drv052WhenRenderCovered,
		"the render equals the committed CHANGELOG.md byte-for-byte":             drv052ThenRenderMatches,
	}
	for _, s := range []string{
		"changelog.d/drv-900.md", "ticket drv-900 from $DRAIVER_TICKET", "skip: test-only refactor",
		"unknown section", "empty body", "non-empty reason", "changelog.d/drv-901.md",
		"ticket drv-900 from branch draiver/drv-900/0001", "cannot determine the ticket id",
	} {
		steps[`the rung output names "`+s+`"`] = drv052ThenOutputNames(s)
	}
	for text, fn := range steps {
		registry[text] = fn
	}
}

// drv052Checker builds cmd/changelog once per acceptance run (like overlayBin).
var drv052Checker = sync.OnceValues(func() (string, error) {
	root, err := ovRepoRoot()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(root, "features", "_artifacts", ".bin", "changelog")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/changelog")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build cmd/changelog: %v\n%s", err, out)
	}
	return bin, nil
})

type drv052Scratch struct {
	dir, data string
	out       string
	err       error
}

func drv052Get(w *World) (*drv052Scratch, error) {
	s, _ := w.get("drv052").(*drv052Scratch)
	if s == nil {
		return nil, fmt.Errorf("no drv-052 scratch repo: a Given step must run first")
	}
	return s, nil
}

func drv052GivenScratchRepo(w *World, sr *StepRun) error {
	checker, err := drv052Checker()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "drv052-repo-")
	if err != nil {
		return err
	}
	data, err := os.MkdirTemp("", "drv052-data-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(dir); os.RemoveAll(data) })
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=acc", "-c", "user.email=acc@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"},
		{"checkout", "-q", "-b", drv052Branch},
	} {
		if err := gitc(dir, args...); err != nil {
			return err
		}
	}
	pyr := "levels:\n  - name: changelog\n    run: " + checker + " check\n  - name: unit\n    run: \"true\"\n"
	files := map[string]string{
		".test-pyramid.yaml":     pyr,
		"changelog.d/_header.md": "# Changelog\n",
	}
	for rel, body := range files {
		if err := drv052Write(dir, rel, body); err != nil {
			return err
		}
	}
	w.set("drv052", &drv052Scratch{dir: dir, data: data})
	return nil
}

func drv052Write(dir, rel, body string) error {
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(body), 0o644)
}

func drv052Frag(ticket, body string) StepFunc {
	return func(w *World, sr *StepRun) error {
		s, err := drv052Get(w)
		if err != nil {
			return err
		}
		return drv052Write(s.dir, filepath.Join("changelog.d", ticket+".md"), body)
	}
}

func drv052GivenOnMain(w *World, sr *StepRun) error {
	s, err := drv052Get(w)
	if err != nil {
		return err
	}
	return gitc(s.dir, "checkout", "-q", "main")
}

// drv052Env is the child environment: the developer's own draiver knobs (and any
// DRAIVER_TICKET the acceptance run itself inherited from `draiver test`) are dropped,
// so the scratch run sees only what the scenario sets up.
func drv052Env(s *drv052Scratch) []string {
	drop := map[string]bool{
		"DRAIVER_DATA": true, "DRAIVER_CONFIG": true, "XDG_CACHE_HOME": true,
		"DRAIVER_ACTOR": true, "DRAIVER_ATTEMPT": true, "DRAIVER_TICKET": true,
	}
	var env []string
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i >= 0 && drop[e[:i]] {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"DRAIVER_DATA="+filepath.Join(s.data, "data"),
		"DRAIVER_CONFIG="+filepath.Join(s.data, "config.json"),
		"XDG_CACHE_HOME="+filepath.Join(s.data, "cache"),
		"DRAIVER_ACTOR=agent:acceptance",
	)
}

func drv052Exec(w *World, sr *StepRun, label, bin string, args ...string) error {
	s, err := drv052Get(w)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = s.dir
	cmd.Env = drv052Env(s)
	out, runErr := cmd.CombinedOutput()
	s.out, s.err = string(out), runErr
	exit := "exit 0"
	if runErr != nil {
		exit = runErr.Error()
	}
	attachText(sr, label, fmt.Sprintf("$ %s %s\n%s\n[%s]\n", filepath.Base(bin), strings.Join(args, " "), out, exit))
	return nil
}

func drv052WhenDraiverTest(w *World, sr *StepRun) error {
	bin, err := overlayBin()
	if err != nil {
		return err
	}
	return drv052Exec(w, sr, "draiver-test-changelog", bin, "test", "changelog")
}

func drv052WhenHandCheck(w *World, sr *StepRun) error {
	bin, err := drv052Checker()
	if err != nil {
		return err
	}
	return drv052Exec(w, sr, "changelog-check", bin, "check")
}

func drv052ThenRung(pass bool) StepFunc {
	return func(w *World, sr *StepRun) error {
		s, err := drv052Get(w)
		if err != nil {
			return err
		}
		if pass && s.err != nil {
			return fmt.Errorf("want the rung green, got %v:\n%s", s.err, s.out)
		}
		if !pass && s.err == nil {
			return fmt.Errorf("want the rung red, but it passed:\n%s", s.out)
		}
		return nil
	}
}

func drv052ThenOutputNames(want string) StepFunc {
	return func(w *World, sr *StepRun) error {
		s, err := drv052Get(w)
		if err != nil {
			return err
		}
		if !strings.Contains(s.out, want) {
			return fmt.Errorf("output does not name %q:\n%s", want, s.out)
		}
		return nil
	}
}

// ---- generator ----

type drv052Gen struct {
	dir     string
	renders map[string]string
}

func drv052GetGen(w *World) (*drv052Gen, error) {
	g, _ := w.get("drv052gen").(*drv052Gen)
	if g == nil {
		return nil, fmt.Errorf("no drv-052 fragment tree: a Given step must run first")
	}
	return g, nil
}

func drv052GivenTree(w *World, sr *StepRun) error {
	dir, err := os.MkdirTemp("", "drv052-tree-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	frag := func(sec, body string) string { return "---\nsection: " + sec + "\n---\n" + body + "\n" }
	// Written deliberately out of order, across prefixes, numbers and sections.
	files := map[string]string{
		"changelog.d/_header.md":   "# Changelog\n\nScratch.\n",
		"changelog.d/README.md":    "Not a fragment.\n",
		"changelog.d/drvweb-3.md":  frag("Added", "**Web three (drvweb-3).**"),
		"changelog.d/drv-10.md":    frag("Added", "**Ten (drv-10).**"),
		"changelog.d/drvctl-2.md":  frag("Added", "**Ctl two (drvctl-2).**"),
		"changelog.d/drv-9.md":     frag("Added", "**Nine (drv-9).**\nWith prose."),
		"changelog.d/drv-5.md":     frag("Security", "**Five (drv-5).**"),
		"changelog.d/drv-6.md":     frag("Fixed", "**Six (drv-6).**"),
		"changelog.d/drv-7.md":     frag("Changed", "**Seven (drv-7).**"),
		"changelog.d/drv-8.md":     frag("Removed", "**Eight (drv-8).**"),
		"changelog.d/drv-4.md":     frag("Deprecated", "**Four (drv-4).**"),
		"changelog.d/drv-99.md":    "---\nskip: nothing user-visible\n---\n",
		"changelog.d/drvctl-10.md": frag("Added", "**Ctl ten (drvctl-10).**"),
	}
	for rel, body := range files {
		if err := drv052Write(dir, rel, body); err != nil {
			return err
		}
	}
	w.set("drv052gen", &drv052Gen{dir: dir, renders: map[string]string{}})
	return nil
}

func drv052WhenGenerate(which string) StepFunc {
	return func(w *World, sr *StepRun) error {
		g, err := drv052GetGen(w)
		if err != nil {
			return err
		}
		bin, err := drv052Checker()
		if err != nil {
			return err
		}
		out, err := exec.Command(bin, "generate", "--root", g.dir).CombinedOutput()
		if err != nil {
			return fmt.Errorf("changelog generate: %v\n%s", err, out)
		}
		b, err := os.ReadFile(filepath.Join(g.dir, "CHANGELOG.md"))
		if err != nil {
			return err
		}
		g.renders[which] = string(b)
		attachText(sr, "CHANGELOG-"+which+".md", string(b))
		return nil
	}
}

func drv052First(w *World) (string, error) {
	g, err := drv052GetGen(w)
	if err != nil {
		return "", err
	}
	r, ok := g.renders["first"]
	if !ok {
		return "", fmt.Errorf("nothing generated yet")
	}
	return r, nil
}

func drv052ThenSectionOrder(w *World, sr *StepRun) error {
	r, err := drv052First(w)
	if err != nil {
		return err
	}
	var got []string
	for _, m := range regexp.MustCompile(`(?m)^### (.+)$`).FindAllStringSubmatch(r, -1) {
		got = append(got, m[1])
	}
	want := strings.Join(changelog.Sections, ",")
	if strings.Join(got, ",") != want {
		return fmt.Errorf("section order %v, want %s", got, want)
	}
	return nil
}

func drv052ThenEntryOrder(w *World, sr *StepRun) error {
	r, err := drv052First(w)
	if err != nil {
		return err
	}
	start := strings.Index(r, "### Added")
	end := strings.Index(r, "### Changed")
	if start < 0 || end < start {
		return fmt.Errorf("no Added section:\n%s", r)
	}
	var got []string
	for _, m := range regexp.MustCompile(`\((drv(?:ctl|web)?-[0-9]+)\)`).FindAllStringSubmatch(r[start:end], -1) {
		got = append(got, m[1])
	}
	want := "drv-9,drv-10,drvctl-2,drvctl-10,drvweb-3"
	if strings.Join(got, ",") != want {
		return fmt.Errorf("Added entry order %v, want %s", got, want)
	}
	return nil
}

func drv052ThenSkipOmitted(w *World, sr *StepRun) error {
	r, err := drv052First(w)
	if err != nil {
		return err
	}
	if strings.Contains(r, "drv-99") || strings.Contains(r, "nothing user-visible") {
		return fmt.Errorf("skip fragment drv-99 leaked into the render:\n%s", r)
	}
	return nil
}

func drv052ThenIdempotent(w *World, sr *StepRun) error {
	g, err := drv052GetGen(w)
	if err != nil {
		return err
	}
	if g.renders["first"] != g.renders["second"] {
		return fmt.Errorf("regeneration changed CHANGELOG.md")
	}
	return nil
}

func drv052ThenStaleCheck(w *World, sr *StepRun) error {
	g, err := drv052GetGen(w)
	if err != nil {
		return err
	}
	bin, err := drv052Checker()
	if err != nil {
		return err
	}
	out, err := exec.Command(bin, "generate", "--check", "--root", g.dir).CombinedOutput()
	attachText(sr, "generate-check", string(out))
	if err != nil {
		return fmt.Errorf("generate --check: %v\n%s", err, out)
	}
	return nil
}

// ---- migration invariant on the real repo ----

type drv052Repo struct {
	root      string
	committed string
	tree      *changelog.Tree
	render    string
}

func drv052GivenRepoTree(w *World, sr *StepRun) error {
	root, err := ovRepoRoot()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		return err
	}
	tree, err := changelog.Load(root)
	if err != nil {
		return fmt.Errorf("load changelog.d: %v", err)
	}
	w.set("drv052repo", &drv052Repo{root: root, committed: string(b), tree: tree})
	return nil
}

// fragmentTicket strips a ".N" continuation suffix: drvctl-016.2 → drvctl-016.
func fragmentTicket(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return name
}

func drv052WhenRenderCovered(w *World, sr *StepRun) error {
	r, _ := w.get("drv052repo").(*drv052Repo)
	if r == nil {
		return fmt.Errorf("no repo tree: a Given step must run first")
	}
	// The ids the committed [Unreleased] section carries — the fragments it was
	// generated from. Fragments added since (pending the next regeneration) are listed
	// as evidence but are not part of the committed file.
	covered := map[string]bool{}
	for _, e := range parseChangelogEntries(r.committed) {
		if strings.HasPrefix(e.release, "[Unreleased]") && e.id != "" {
			covered[e.id] = true
		}
	}
	t := *r.tree
	t.Unreleased = nil
	var pending []string
	for _, f := range r.tree.Unreleased {
		switch {
		case f.Skip != "":
			// renders nothing either way
		case covered[fragmentTicket(f.Name)]:
			t.Unreleased = append(t.Unreleased, f)
		default:
			pending = append(pending, f.Path)
		}
	}
	r.render = string(changelog.Render(&t))
	note := "none"
	if len(pending) > 0 {
		note = strings.Join(pending, "\n")
	}
	attachText(sr, "coverage", fmt.Sprintf("%d released versions; %d unreleased fragments in CHANGELOG.md\nfragments pending regeneration:\n%s\n",
		len(r.tree.Releases), len(t.Unreleased), note))
	return nil
}

func drv052ThenRenderMatches(w *World, sr *StepRun) error {
	r, _ := w.get("drv052repo").(*drv052Repo)
	if r == nil {
		return fmt.Errorf("no repo tree: a Given step must run first")
	}
	if r.render == r.committed {
		return nil
	}
	a, b := strings.Split(r.render, "\n"), strings.Split(r.committed, "\n")
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return fmt.Errorf("CHANGELOG.md differs from the fragment render at line %d:\n  render:    %q\n  committed: %q\n(regenerate with `go run ./cmd/changelog generate`; never edit CHANGELOG.md by hand)", i+1, x, y)
		}
	}
	return fmt.Errorf("CHANGELOG.md differs from the fragment render (%d vs %d bytes)", len(r.render), len(r.committed))
}
