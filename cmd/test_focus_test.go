package cmd

import (
	"strings"
	"testing"
)

// drv-063: a focused run (--tags/--dir/--name) runs ONLY the target rung, exporting
// the focus to it as $DRAIVER_FOCUS_*; a red lower rung is never reached.
func TestFocusedRunRunsOnlyTargetRungWithFocusEnv(t *testing.T) {
	pyramidCheckout(t, "PROJ-1", "0001", true, [][2]string{
		{"unit", "echo LOWER_RAN; false"},
		{"acceptance", `echo "TAGS=$DRAIVER_FOCUS_TAGS DIR=$DRAIVER_FOCUS_DIR NAME=$DRAIVER_FOCUS_NAME"`},
	})
	out, code := run(t, "test", "acceptance", "--tags", "@drv-046,@ui", "--dir", "features/x", "--name", "shown")
	if code != 0 {
		t.Fatalf("focused run exited %d:\n%s", code, out)
	}
	if strings.Contains(out, "LOWER_RAN") {
		t.Errorf("focused run climbed into the lower rung:\n%s", out)
	}
	if !strings.Contains(out, "TAGS=@drv-046,@ui DIR=features/x NAME=shown") {
		t.Errorf("focus env not exported to the rung:\n%s", out)
	}
	if !strings.Contains(out, "records no result") {
		t.Errorf("focused run does not say it records nothing:\n%s", out)
	}
}

// A focused run's exit code is the target rung's own.
func TestFocusedRunPropagatesRungFailure(t *testing.T) {
	pyramidCheckout(t, "PROJ-1", "0001", true, [][2]string{
		{"acceptance", "false"},
	})
	if out, code := run(t, "test", "acceptance", "--tags", "@x"); code == 0 {
		t.Fatalf("red focused rung exited 0:\n%s", out)
	}
}

// A focused run can never be a gate result: --log and --detach are refused before
// anything runs.
func TestFocusedRunRefusesLogAndDetach(t *testing.T) {
	for _, flag := range []string{"--log", "--detach"} {
		t.Run(flag, func(t *testing.T) {
			pyramidCheckout(t, "PROJ-1", "0001", true, [][2]string{
				{"acceptance", "echo RAN"},
			})
			out, code, err := runE(t, "test", "acceptance", "--tags", "@x", flag)
			if code == 0 || err == nil {
				t.Fatalf("focused %s was accepted:\n%s", flag, out)
			}
			if strings.Contains(out, "RAN") || !strings.Contains(err.Error(), "never be a gate result") {
				t.Fatalf("focused %s: want a refusal before running, got %v:\n%s", flag, err, out)
			}
		})
	}
}
