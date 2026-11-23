package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// fork opens an interactive, read-only fork of an attempt's claude-code session
// (drvweb-029, decision #24). It is what the board's "Copy fork command" button
// copies. A raw `cd <worktree> && claude --resume` one-liner breaks once the
// daemon reclaims the worktree (a clean checkout is removed on retire into
// Review), and the cd cannot be dropped: Claude Code keys transcripts by cwd, so
// --resume only finds the session from that exact path. This verb recreates a
// reclaimed worktree with the same worktree.Manager.Create the daemon uses,
// which is idempotent, so the daemon adopts the checkout as-is on its next Admit.
//
// It writes no log events: the whole point is that the operator's conversation
// stays out of the draiver log and out of the transcript draiver resumes.

var forkPrint bool

var forkCmd = &cobra.Command{
	Use:   "fork <ticket[@attempt]>",
	Short: "Open a private, read-only fork of an attempt's Claude Code session",
	Long: "fork opens an interactive copy of the attempt's claude-code session under a NEW\n" +
		"session id (`claude --resume <id> --fork-session --permission-mode plan`), run in\n" +
		"the attempt's worktree. Ask the agent anything (\"what are you doing?\", \"why X?\")\n" +
		"without the exchange reaching the draiver log or the transcript draiver resumes.\n" +
		"Plan mode keeps the fork read-only, because it shares the worktree with the agent.\n\n" +
		"If the daemon reclaimed the worktree, fork recreates it at the same path on the\n" +
		"attempt's branch first. It must run on the host, and as the user, that runs claude\n" +
		"for draiver; `claude` must be on PATH. Writes no log events.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		ticket, att, err := resolveTarget(root, args[0])
		if err != nil {
			return err
		}
		cwd, argv, err := prepareFork(cmd.Context(), root, ticket, att, cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		if forkPrint {
			fmt.Fprintf(cmd.OutOrStdout(), "cwd: %s\nargv: %s\n", cwd, strings.Join(argv, " "))
			return nil
		}
		bin, err := exec.LookPath(argv[0])
		if err != nil {
			return fmt.Errorf("fork: %s not found on PATH: %w", argv[0], err)
		}
		if err := os.Chdir(cwd); err != nil {
			return fmt.Errorf("fork: cd %s: %w", cwd, err)
		}
		// Replace this process so claude owns the terminal directly.
		return syscall.Exec(bin, argv, os.Environ())
	},
}

// prepareFork resolves where and how to fork ticket/att's session: it reads
// session.json, applies the shared session.ForkCheck gate, and makes sure the
// session's worktree exists, recreating it if the daemon reclaimed it. It
// returns the cwd claude must run in and its argv.
func prepareFork(ctx context.Context, root store.Root, ticket, att string, stderr io.Writer) (string, []string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := os.ReadFile(root.SessionMetaPath(ticket, att))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, fmt.Errorf("fork: %s/%s has no session yet (no session.json)", ticket, att)
	} else if err != nil {
		return "", nil, fmt.Errorf("fork: read session.json: %w", err)
	}
	var id session.Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return "", nil, fmt.Errorf("fork: parse session.json: %w", err)
	}
	if err := session.ForkCheck(id); err != nil {
		return "", nil, fmt.Errorf("fork: %s/%s: %w", ticket, att, err)
	}
	if !filepath.IsAbs(id.Worktree) {
		return "", nil, fmt.Errorf("fork: %s/%s: session.json records no absolute worktree (%q); claude finds the transcript only from that path", ticket, att, id.Worktree)
	}
	want := filepath.Clean(id.Worktree)

	if fi, err := os.Stat(want); err == nil && fi.IsDir() {
		return want, session.ForkArgv(id.SessionID), nil
	}

	// The worktree is gone (reclaimed on retire, or a crash): recreate it.
	a, err := project.LoadAttempt(root, ticket, att)
	if err != nil {
		return "", nil, fmt.Errorf("fork: load attempt: %w", err)
	}
	if a.Repo == "" {
		return "", nil, fmt.Errorf("fork: worktree %s is gone and %s/%s records no repo to recreate it from", want, ticket, att)
	}
	wm, err := worktree.NewManager(a.Repo)
	if err != nil {
		return "", nil, fmt.Errorf("fork: %w", err)
	}
	key := worktree.Key{Ticket: ticket, Attempt: att}
	ok, err := wm.BranchExists(ctx, key)
	if err != nil {
		return "", nil, fmt.Errorf("fork: %w", err)
	}
	if !ok {
		// Create would cut a fresh branch from HEAD, which is not the attempt's
		// work: refuse rather than fork a session against the wrong tree.
		return "", nil, fmt.Errorf("fork: worktree %s is gone and branch %s no longer exists in %s; cannot recreate it", want, key.Branch(), a.Repo)
	}
	if p := filepath.Clean(wm.PathFor(key)); p != want {
		return "", nil, fmt.Errorf("fork: worktree %s is gone and would be recreated at %s instead; claude keys transcripts by path, so the session would not be found", want, p)
	}
	wt, err := wm.Create(ctx, worktree.Spec{Key: key})
	if err != nil {
		return "", nil, fmt.Errorf("fork: recreate worktree: %w", err)
	}
	if filepath.Clean(wt.Path) != want {
		return "", nil, fmt.Errorf("fork: recreated worktree at %s, not %s; claude would not find the session", wt.Path, want)
	}
	fmt.Fprintf(stderr, "fork: worktree was reclaimed; recreated %s on %s\n", want, key.Branch())
	return want, session.ForkArgv(id.SessionID), nil
}

func init() {
	forkCmd.Flags().BoolVar(&forkPrint, "print", false, "print the cwd and claude argv instead of running claude (still recreates a reclaimed worktree)")
	rootCmd.AddCommand(forkCmd)
}
