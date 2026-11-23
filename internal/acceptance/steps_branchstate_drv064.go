package acceptance

// drv-064: the cold-start brief carries a Branch state block read from git. These
// steps build a real repo + managed worktree (under a scenario-scoped
// XDG_CACHE_HOME), move the base, and run the real `draiver brief` binary.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

const bs64Ticket = "BS-64"

type bs64World struct {
	root  store.Root
	repo  string
	cache string
	out   string
}

func init() {
	for text, fn := range map[string]StepFunc{
		"a drv-064 attempt whose worktree holds a commit and an uncommitted file":   bs64GivenWorktree,
		"the drv-064 base has gained a commit since the cut":                        bs64GivenBaseMoved,
		"a drv-064 attempt with no worktree":                                        bs64GivenNoWorktree,
		"the drv-064 agent runs draiver brief":                                      bs64WhenBrief,
		"the drv-064 brief shows the branch state block with the base's new commit": bs64ThenBlock,
		"the drv-064 brief has no branch state block":                               bs64ThenNoBlock,
	} {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registration: " + text)
		}
		registry[text] = fn
	}
}

func bs64Get(w *World) (*bs64World, error) {
	b, _ := w.get("bs64").(*bs64World)
	if b == nil {
		return nil, fmt.Errorf("drv-064 fixture not seeded")
	}
	return b, nil
}

// bs64Seed makes a repo on main with one commit and a data root whose attempt
// targets it. The cache dir is where the managed worktree (if any) lives.
func bs64Seed(w *World) (*bs64World, error) {
	b := &bs64World{repo: mustTemp(w, "bs64-repo-"), cache: mustTemp(w, "bs64-cache-")}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if err := gitc(b.repo, args...); err != nil {
			return nil, err
		}
	}
	root := store.Root{Dir: mustTemp(w, "bs64-data-")}
	if err := addTypedTicket(root, b.repo, bs64Ticket, ""); err != nil {
		return nil, err
	}
	b.root = root
	w.set("bs64", b)
	return b, nil
}

func bs64GivenNoWorktree(w *World, sr *StepRun) error {
	_, err := bs64Seed(w)
	return err
}

func bs64GivenWorktree(w *World, sr *StepRun) error {
	b, err := bs64Seed(w)
	if err != nil {
		return err
	}
	// NewManager derives its managed base from XDG_CACHE_HOME; point it at the
	// scenario's cache so the shelled binary (given the same cache) finds it.
	prev, had := os.LookupEnv("XDG_CACHE_HOME")
	os.Setenv("XDG_CACHE_HOME", b.cache)
	w.defer_(func() {
		if had {
			os.Setenv("XDG_CACHE_HOME", prev)
		} else {
			os.Unsetenv("XDG_CACHE_HOME")
		}
	})
	m, err := worktree.NewManager(b.repo)
	if err != nil {
		return err
	}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: bs64Ticket, Attempt: cbAtt}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "feature.go"), []byte("package f\n\nfunc F() {}\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "feature.go"}, {"commit", "-q", "-m", "add feature"}} {
		if err := gitc(wt.Path, args...); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(wt.Path, "wip.txt"), []byte("wip"), 0o644)
}

func bs64GivenBaseMoved(w *World, sr *StepRun) error {
	b, err := bs64Get(w)
	if err != nil {
		return err
	}
	return gitc(b.repo, "commit", "-q", "--allow-empty", "-m", "another ticket landed on main")
}

func bs64WhenBrief(w *World, sr *StepRun) error {
	b, err := bs64Get(w)
	if err != nil {
		return err
	}
	bin, err := overlayBin()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "--data", b.root.Dir, "brief", bs64Ticket, "--attempt", cbAtt)
	drop := map[string]bool{"DRAIVER_DATA": true, "DRAIVER_CONFIG": true, "XDG_CACHE_HOME": true, "DRAIVER_ACTOR": true, "DRAIVER_ATTEMPT": true}
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i >= 0 && drop[e[:i]] {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env,
		"XDG_CACHE_HOME="+b.cache,
		"DRAIVER_CONFIG="+filepath.Join(b.cache, "no-config.json"),
		"DRAIVER_ACTOR=agent:acceptance",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("draiver brief: %v\n%s", err, out)
	}
	b.out = string(out)
	attachText(sr, "drv-064-brief", b.out)
	return nil
}

func bs64ThenBlock(w *World, sr *StepRun) error {
	b, err := bs64Get(w)
	if err != nil {
		return err
	}
	start := strings.Index(b.out, "## Branch state")
	if start < 0 {
		return fmt.Errorf("brief has no Branch state block:\n%s", b.out)
	}
	block := b.out[start:]
	if end := strings.Index(block, "## Current understanding"); end >= 0 {
		block = block[:end]
	}
	attachText(sr, "drv-064-branch-state-block", block)
	for _, want := range []string{
		"Branch `draiver/" + bs64Ticket + "/" + cbAtt + "`",
		"base `main`",
		"merge-base",
		"- `feature.go` +3 −0",
		"- `?? wip.txt`",
		"Base `main` has 1 commit since your merge-base",
		"another ticket landed on main",
		"`git diff main...HEAD`",
	} {
		if !strings.Contains(block, want) {
			return fmt.Errorf("Branch state block missing %q:\n%s", want, block)
		}
	}
	if n := strings.Count(strings.TrimRight(block, "\n"), "\n") + 1; n > 40 {
		return fmt.Errorf("Branch state block is %d lines, want <= 40", n)
	}
	return nil
}

func bs64ThenNoBlock(w *World, sr *StepRun) error {
	b, err := bs64Get(w)
	if err != nil {
		return err
	}
	if strings.Contains(b.out, "## Branch state") {
		return fmt.Errorf("brief shows a Branch state block with no worktree:\n%s", b.out)
	}
	if !strings.Contains(b.out, "## Current understanding") {
		return fmt.Errorf("brief looks incomplete:\n%s", b.out)
	}
	return nil
}
