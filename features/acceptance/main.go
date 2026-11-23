// Command acceptance is the executable for drv-019's BDD acceptance rung. It is a
// thin wrapper over internal/acceptance: it runs the repo's features (recursively,
// across the capability subfolders),
// writes the cucumber-JSON and screenshot artefacts the rung declares
// (features/_artifacts/), prints a one-line summary, and exits non-zero if any
// scenario failed — so the rung's verdict reflects the acceptance result.
//
// The .test-pyramid.yaml `acceptance` rung invokes it as `go run ./features/acceptance`
// from the worktree root, so the relative paths below resolve against the repo root.
//
// Focused runs (drv-063) narrow the suite while you iterate:
//
//	go run ./features/acceptance --tags @drv-046[,@x]   # scenarios carrying any tag
//	go run ./features/acceptance --dir features/overlays # one capability folder
//	go run ./features/acceptance --name "promoted"       # scenario name contains
//
// Each flag falls back to $DRAIVER_FOCUS_TAGS / $DRAIVER_FOCUS_DIR /
// $DRAIVER_FOCUS_NAME, which `draiver test acceptance --tags …` exports. A focused
// run is evidence for you, never a gate result: `draiver test` records nothing for
// it, and the review gate needs the full suite.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/acceptance"
)

const (
	featuresDir = "features"
	outDir      = "features/_artifacts"
)

func main() {
	tags := flag.String("tags", os.Getenv("DRAIVER_FOCUS_TAGS"), "run only scenarios carrying any of these comma-separated tags (feature/rule tags count), e.g. @drv-046,@ui")
	dir := flag.String("dir", os.Getenv("DRAIVER_FOCUS_DIR"), "run only the features under this directory, e.g. features/overlays")
	name := flag.String("name", os.Getenv("DRAIVER_FOCUS_NAME"), "run only scenarios whose name contains this text (case-insensitive)")
	flag.Parse()

	root := featuresDir
	if d := strings.TrimSpace(*dir); d != "" {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			fmt.Fprintf(os.Stderr, "acceptance: --dir %q is not a directory\n", d)
			os.Exit(2)
		}
		root = filepath.Clean(d)
	}
	opts := acceptance.RunOptions{Tags: acceptance.ParseTags(*tags), Name: *name}
	focused := opts.Focused() || root != featuresDir

	res, err := acceptance.RunFocused(root, outDir, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acceptance:", err)
		os.Exit(2)
	}
	// List the discovered features (across the capability subfolders) before the
	// one-line summary, so the run log is self-evident about what was found.
	fmt.Printf("discovered %d feature(s) under %s/:\n", len(res.Report), root)
	for _, f := range res.Report {
		fmt.Printf("  - %s\t%s (%d scenarios)\n", f.URI, f.Name, len(f.Elements))
	}
	if focused {
		fmt.Printf("focused run: %d of %d scenario(s) under %s/ (tags %q, name %q) — not a gate result; the review gate needs the full suite\n",
			res.ScenariosSelected, res.ScenariosTotal, root, strings.Join(opts.Tags, ","), opts.Name)
	}
	acceptance.WriteSummary(os.Stdout, res)
	failed := false
	if res.ScenariosFailed > 0 {
		fmt.Fprintf(os.Stderr, "acceptance: %d scenario(s) failed\n", res.ScenariosFailed)
		failed = true
	}
	// The artefact rule, enforced at runtime (drv-063): a scenario that passed without
	// attaching anything fails the run — the report page relies on every scenario
	// carrying evidence.
	if missing := acceptance.MissingArtefacts(res.Report); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "acceptance: %d passing scenario(s) attached no artefact — end each with shoot(...) or attachText(...):\n", len(missing))
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "  - %s\n", m)
		}
		failed = true
	}
	if failed {
		os.Exit(1)
	}
}
