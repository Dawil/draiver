package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/repo"
)

var (
	configRepoRemote string
	configRepoBranch string
	configRepoRung   string
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Read and write draiver's operator config (config.json)",
	Long: "config operates on the operator config file (config.json) — the tunables the\n" +
		"supervisor reads, resolved from --config's absence via $DRAIVER_CONFIG, then\n" +
		"~/.draiver/config.json. It lives outside the ticket data root.",
}

var configRepoCmd = &cobra.Command{
	Use:   "repo <repo>",
	Short: "Read or set a repo's default remote/branch/test rung (per-repo settings)",
	Long: "Read or set the per-repo settings stored under config.json's `repos` map,\n" +
		"keyed by the repo path (the same string an attempt records as its `repo`). These\n" +
		"are properties of the repo, not one attempt: set them once and every attempt on\n" +
		"the repo reads the same value.\n" +
		"\n" +
		"  draiver config repo /path/to/repo                       print the resolved values\n" +
		"  draiver config repo /path/to/repo --remote forgejo      set the default git remote\n" +
		"  draiver config repo /path/to/repo --branch main --rung integration\n" +
		"\n" +
		"Only the flags you pass change; an omitted flag leaves that key untouched, and a\n" +
		"flag passed empty (e.g. --remote \"\") clears it so it falls back to the global\n" +
		"default. Each value resolves per-repo → global default → builtin: the remote\n" +
		"falls back to primary_remote, the branch to the repo's current branch, the rung\n" +
		"to the pyramid file's top rung. The write merges into config.json atomically,\n" +
		"leaving every other key and repo untouched. It is the scriptable twin of the\n" +
		"attempt page's repo-settings panel, both going through the shared gateway.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoPath := strings.TrimSpace(args[0])
		if repoPath == "" {
			return fmt.Errorf("a repo path is required")
		}
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		gw := repo.New(root, resolveActor())

		// Write form: a changed flag sets its key (empty value clears it); an omitted
		// flag leaves it. With no flag changed this stays an all-nil update and the
		// command falls through to the read form below.
		var upd config.RepoSettingsUpdate
		if cmd.Flags().Changed("remote") {
			v := strings.TrimSpace(configRepoRemote)
			upd.DefaultRemote = &v
		}
		if cmd.Flags().Changed("branch") {
			v := strings.TrimSpace(configRepoBranch)
			upd.DefaultBranch = &v
		}
		if cmd.Flags().Changed("rung") {
			v := strings.TrimSpace(configRepoRung)
			upd.DefaultTestRung = &v
		}
		if changed, err := gw.SetRepoSettings(repoPath, upd); err != nil {
			return err
		} else if changed {
			fmt.Fprintf(cmd.OutOrStdout(), "set %s per-repo settings in config.json\n", repoPath)
			return nil
		}

		// Read form: print the resolved values, annotating where each came from so the
		// fallback chain is legible (per-repo entry vs the global/builtin it falls to).
		r, err := gw.RepoSettings(repoPath)
		if err != nil {
			return err
		}
		stored := config.RepoSettings{}
		if cfg, err := config.Load(""); err == nil {
			stored = cfg.Repos[repoPath]
		}
		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
		fmt.Fprintf(tw, "repo\t%s\n", repoPath)
		fmt.Fprintf(tw, "default remote\t%s\n", resolvedLine(stored.DefaultRemote, r.Remote, "unset — git actions fall back to the sole remote or surface ambiguity"))
		fmt.Fprintf(tw, "default branch\t%s\n", resolvedLine(stored.DefaultBranch, r.Branch, "unset — defaults to the repo's current branch"))
		fmt.Fprintf(tw, "default test rung\t%s\n", resolvedLine(stored.DefaultTestRung, r.TestRung, "unset — defaults to the pyramid file's top rung"))
		return tw.Flush()
	},
}

// resolvedLine renders one setting's resolved value with a provenance hint: the
// per-repo entry when it set the value, the global/builtin fallback when it did not,
// or the supplied "unset" note when nothing resolved it at all.
func resolvedLine(stored, resolved, unsetNote string) string {
	switch {
	case stored != "":
		return fmt.Sprintf("%s (per-repo)", resolved)
	case resolved != "":
		return fmt.Sprintf("%s (global default)", resolved)
	default:
		return unsetNote
	}
}

func init() {
	configRepoCmd.Flags().StringVar(&configRepoRemote, "remote", "", "default git remote for this repo (supersedes primary_remote); empty clears it")
	configRepoCmd.Flags().StringVar(&configRepoBranch, "branch", "", "default branch (trunk) for this repo; empty clears it")
	configRepoCmd.Flags().StringVar(&configRepoRung, "rung", "", "default target test rung for this repo; empty clears it")
	configCmd.AddCommand(configRepoCmd)
	rootCmd.AddCommand(configCmd)
}
