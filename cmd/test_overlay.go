package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/overlay"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/repo"
	"github.com/Dawil/draiver/internal/worktree"
)

// test_overlay.go implements `draiver test create-overlay` (drv-041): the first-class,
// deterministic opt-in that creates a developer-owned test-pyramid overlay for the
// codebase the command is run from. It is a real command rather than an ad-hoc
// `git init` so a coding agent has a repeatable, idempotent way to stand an overlay up —
// it creates the repo at the resolved location, scaffolds a starter skeleton, makes the
// initial commit (so the overlay's default branch exists for the per-attempt worktree to
// cut from), and records the per-codebase setting so resolution finds it next time.
//
// It refuses — writing nothing — when the codebase already carries an in-repo pyramid
// (overlays are ignored outright; in-repo wins) or when an overlay already exists at the
// target. Provisioning a backup remote for the overlay is left to the developer.

var createOverlayCmd = &cobra.Command{
	Use:   "create-overlay",
	Short: "Create + scaffold + register a developer-owned test-pyramid overlay for this codebase",
	Long: "create-overlay stands up a test-pyramid overlay (drv-041) for the codebase this\n" +
		"command is run from — a separate, developer-owned git repo holding the\n" +
		".test-pyramid.yaml (and the features/ + review-env scripts it drives) so none of\n" +
		"it is committed to the codebase's own version control.\n" +
		"\n" +
		"Run it from inside the attempt's codebase worktree. The overlay is created at the\n" +
		"explicit per-codebase overlay location if one is set, else at\n" +
		"<overlay_dir>/<codebase-basename>; set one of those first (via `draiver config\n" +
		"repo <repo> --overlay <path>` or the global overlay_dir). It git-inits the repo,\n" +
		"scaffolds a minimal .test-pyramid.yaml, a features/ dir, and review-env script\n" +
		"stubs, commits them, and records the per-codebase overlay setting so `draiver\n" +
		"test` resolves it from then on.\n" +
		"\n" +
		"It is idempotent and safe: it refuses (writing nothing) if the codebase already\n" +
		"has an in-repo .test-pyramid.yaml (in-repo wins, overlays are ignored) or if an\n" +
		"overlay already exists at the target. A backup remote for the overlay is the\n" +
		"developer's own concern.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		out := cmd.OutOrStdout()
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}

		// Identify the codebase this overlay is for: the attempt's recorded repo path P,
		// whose basename is the folder-lookup key and under whose repos.<P> entry the
		// setting is stored.
		key, err := worktree.Identify(ctx, wd)
		if err != nil {
			return fmt.Errorf("create-overlay must run from inside an attempt's codebase worktree: %w", err)
		}
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		meta, err := attempt.LoadMeta(root, key.Ticket, key.Attempt)
		if err != nil {
			return err
		}
		codebase := strings.TrimSpace(meta.Repo)
		if codebase == "" {
			return fmt.Errorf("attempt %s/%s records no repo, so there is no codebase to key an overlay on", key.Ticket, key.Attempt)
		}

		// Refuse up front if the codebase carries its own pyramid: an overlay would be
		// ignored (in-repo wins outright), so creating one would be a silent no-op dressed
		// up as success. Write nothing.
		inRepo, err := pyramid.Load(wd)
		if err != nil {
			return err
		}
		if inRepo != nil {
			return fmt.Errorf("codebase %s already has an in-repo %s — overlays are ignored (in-repo wins); nothing to create", wd, pyramid.FileName)
		}

		// Resolve the target location: the explicit per-codebase overlay if set, else
		// <overlay_dir>/<basename>. With neither configured there is nowhere to create it.
		cfg, err := config.Load("")
		if err != nil {
			return err
		}
		res := cfg.RepoSettings(codebase)
		target := strings.TrimSpace(res.Overlay)
		if target == "" {
			if strings.TrimSpace(cfg.OverlayDir) == "" {
				return fmt.Errorf("no overlay location configured for %s: set an explicit overlay (`draiver config repo %s --overlay <path>`) or a global overlay_dir first", codebase, codebase)
			}
			target = filepath.Join(cfg.OverlayDir, filepath.Base(codebase))
		}
		target, err = filepath.Abs(target)
		if err != nil {
			return fmt.Errorf("resolve overlay path %q: %w", target, err)
		}

		// Refuse if an overlay (or any non-empty directory) already occupies the target —
		// create-overlay never writes into an existing tree.
		if isRepo, err := overlay.IsGitRepo(target); err != nil {
			return err
		} else if isRepo {
			return fmt.Errorf("an overlay already exists at %s — nothing created (it is already registered for resolution, or set it with `draiver config repo %s --overlay %s`)", target, codebase, target)
		}
		if nonEmpty, err := dirNonEmpty(target); err != nil {
			return err
		} else if nonEmpty {
			return fmt.Errorf("target %s exists and is not empty but is not a git repo — refusing to scaffold into it; nothing created", target)
		}

		// Create + scaffold + git init + initial commit.
		if err := scaffoldOverlay(ctx, target, codebase); err != nil {
			return err
		}

		// Register the per-codebase setting so `draiver test` resolves the overlay from
		// now on (even when it was derived from overlay_dir), making resolution
		// deterministic and surviving a basename collision.
		if _, err := repo.New(root, resolveActor()).SetRepoSettings(codebase, config.RepoSettingsUpdate{Overlay: &target}); err != nil {
			return fmt.Errorf("overlay scaffolded at %s but could not record the per-codebase setting: %w", target, err)
		}

		fmt.Fprintf(out, "created test-pyramid overlay for %s at %s\n", codebase, target)
		fmt.Fprintf(out, "registered as the per-codebase overlay in config.json; `draiver test` will now run it\n")
		fmt.Fprintf(out, "edit %s/%s and %s/features/ to flesh out the pyramid; a backup remote is optional and yours to add\n", target, pyramid.FileName, target)
		return nil
	},
}

// dirNonEmpty reports whether path exists and is a directory containing entries. A
// missing path is a clean (false, nil) — the common create case. A path that exists but
// is a plain file is reported non-empty so the scaffold refuses rather than clobbering.
func dirNonEmpty(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return true, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return len(entries) > 0, nil
}

// scaffoldOverlay creates the overlay directory, writes the starter skeleton, git-inits
// the repo, and makes the initial commit — so the overlay's default branch exists and a
// per-attempt worktree can be cut from it. codebase is named in the generated README for
// provenance.
func scaffoldOverlay(ctx context.Context, target, codebase string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create overlay dir %s: %w", target, err)
	}
	if err := os.MkdirAll(filepath.Join(target, "features"), 0o755); err != nil {
		return fmt.Errorf("create overlay features dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(target, "review-env"), 0o755); err != nil {
		return fmt.Errorf("create overlay review-env dir: %w", err)
	}

	files := map[string]struct {
		data []byte
		mode os.FileMode
	}{
		pyramid.FileName:               {[]byte(scaffoldPyramidYAML), 0o644},
		"README.md":                    {[]byte(fmt.Sprintf(scaffoldReadme, codebase)), 0o644},
		".gitignore":                   {[]byte(scaffoldGitignore), 0o644},
		"features/.gitkeep":            {[]byte(""), 0o644},
		"review-env/up.sh":             {[]byte(scaffoldUpSh), 0o755},
		"review-env/down.sh":           {[]byte(scaffoldDownSh), 0o755},
		"review-env/healthcheck.sh":    {[]byte(scaffoldHealthcheckSh), 0o755},
	}
	for rel, f := range files {
		if err := os.WriteFile(filepath.Join(target, rel), f.data, f.mode); err != nil {
			return fmt.Errorf("scaffold %s: %w", rel, err)
		}
	}

	if _, err := overlayGit(ctx, target, "init"); err != nil {
		return err
	}
	if _, err := overlayGit(ctx, target, "add", "-A"); err != nil {
		return err
	}
	if _, err := overlayGit(ctx, target, "commit", "-m", "draiver: scaffold test-pyramid overlay"); err != nil {
		return err
	}
	return nil
}

// overlayGit runs a git command in the overlay repo, injecting a committer identity so
// the initial commit succeeds even on a box with no global git user configured (a fresh
// CI/agent environment), without disturbing the operator's global git config.
func overlayGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=draiver", "GIT_AUTHOR_EMAIL=draiver@localhost",
		"GIT_COMMITTER_NAME=draiver", "GIT_COMMITTER_EMAIL=draiver@localhost",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("overlay git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

const scaffoldPyramidYAML = `# .test-pyramid.yaml — a developer-owned test-pyramid OVERLAY (drv-041), scaffolded by
# ` + "`draiver test create-overlay`" + `. This file lives in a separate, developer-owned repo
# pointed at only via draiver's config.json — nothing here is added to the codebase's
# own version control.
#
# Execution contract: ` + "`draiver test`" + ` runs these commands with cwd = the CODEBASE
# worktree (so paths resolve against the code under test), and exports two env vars:
#   DRAIVER_CODEBASE_DIR  the codebase worktree (the cwd) — a stable anchor to cd back to
#   DRAIVER_OVERLAY_DIR   this overlay's worktree — reach overlay assets through it, e.g.
#                         run: go run $DRAIVER_OVERLAY_DIR/features/acceptance
#
# Replace the placeholder below with the codebase's real commands.
levels:
  - name: unit
    # Placeholder that passes trivially so a freshly scaffolded overlay is green. Replace
    # with the codebase's unit suite, e.g. run: go test ./... (runs in the codebase cwd).
    run: 'true'
`

const scaffoldReadme = `# Test-pyramid overlay

Developer-owned test-pyramid overlay (drv-041) for the codebase at ` + "`%s`" + `.

This is **personal dev tooling**, not shared with a team: it holds the
` + "`.test-pyramid.yaml`" + `, ` + "`features/`" + `, and ` + "`review-env/`" + ` scripts draiver runs for that
codebase, kept here so none of it lands in the codebase's own version control. draiver
operates this repo **in place** (it never clones or mirrors it); it cuts a per-attempt
worktree on branch ` + "`draiver/<ticket>/<attempt>`" + ` from this repo's default branch when it
runs the pyramid, so your edits stay isolated per attempt.

A backup remote is optional and entirely yours to add.
`

const scaffoldGitignore = `# draiver cuts per-attempt worktrees from this repo; keep run artefacts out of it.
features/_artifacts/
.review-env-run/
`

const scaffoldUpSh = `#!/bin/sh
# review-env up stub (drv-041). Bring the codebase's review environment up here.
# $DRAIVER_CODEBASE_DIR = the codebase worktree; $DRAIVER_OVERLAY_DIR = this overlay.
# Review-ready envs also receive $DRAIVER_REVIEW_PORT and $DRAIVER_REVIEW_INSTANCE.
echo "review-env up: nothing to do (stub) — edit $DRAIVER_OVERLAY_DIR/review-env/up.sh"
`

const scaffoldDownSh = `#!/bin/sh
# review-env down stub (drv-041). Tear the review environment down here. Runs
# best-effort; a non-zero exit never changes a rung's verdict.
echo "review-env down: nothing to do (stub)"
`

const scaffoldHealthcheckSh = `#!/bin/sh
# review-env healthcheck stub (drv-041). Exit 0 when the environment is ready, non-zero
# otherwise. A review-ready environment needs up + down + at least one healthcheck.
exit 0
`

func init() {
	testCmd.AddCommand(createOverlayCmd)
}
