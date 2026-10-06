// Command acceptance is the executable for drv-019's BDD acceptance rung. It is a
// thin wrapper over internal/acceptance: it runs the repo's features/*.feature,
// writes the cucumber-JSON and screenshot artefacts the rung declares
// (features/_artifacts/), prints a one-line summary, and exits non-zero if any
// scenario failed — so the rung's verdict reflects the acceptance result.
//
// The .test-pyramid.yaml `acceptance` rung invokes it as `go run ./features/acceptance`
// from the worktree root, so the relative paths below resolve against the repo root.
package main

import (
	"fmt"
	"os"

	"github.com/Dawil/draiver/internal/acceptance"
)

const (
	featuresDir = "features"
	outDir      = "features/_artifacts"
)

func main() {
	res, err := acceptance.Run(featuresDir, outDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acceptance:", err)
		os.Exit(2)
	}
	acceptance.WriteSummary(os.Stdout, res)
	if res.ScenariosFailed > 0 {
		fmt.Fprintf(os.Stderr, "acceptance: %d scenario(s) failed\n", res.ScenariosFailed)
		os.Exit(1)
	}
}
