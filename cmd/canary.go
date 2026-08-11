package cmd

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/canary"
)

var (
	canaryRepo      string
	canaryMinPrefix int
	canaryReadFloor float64
)

var canaryCmd = &cobra.Command{
	Use:   "canary",
	Short: "Silent-invalidator canary: alert when a repo's shared prompt-cache prefix is busted",
	Long: "canary scans the attempts of each repo and checks that the second and later\n" +
		"attempts reuse the shared tools+system+append prefix on turn 1 (drvctl-036).\n" +
		"A busted prefix — per-ticket data in the --append-system-prompt, or an\n" +
		"unpinned adapter upgrade — makes every attempt cold-write the prefix instead,\n" +
		"silently erasing the cross-ticket cache win. It doubles as the per-repo\n" +
		"cross-attempt cache rollup. Exits " + fmt.Sprintf("%d", ExitCanaryFired) + " if the canary fires.\n\n" +
		"The scan is read-only. Scope to one repo with --repo.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		obs, err := canary.Observe(root)
		if err != nil {
			return err
		}
		if canaryRepo != "" {
			filtered := obs[:0]
			for _, o := range obs {
				if o.Repo == canaryRepo {
					filtered = append(filtered, o)
				}
			}
			obs = filtered
		}

		cfg := canary.DefaultConfig()
		if canaryMinPrefix > 0 {
			cfg.MinSharedPrefixTokens = canaryMinPrefix
		}
		if canaryReadFloor > 0 {
			cfg.ReadShareFloor = canaryReadFloor
		}
		rep := canary.Analyze(obs, cfg)

		out := cmd.OutOrStdout()
		if len(rep.Repos) == 0 {
			fmt.Fprintln(out, "no metered attempts found — nothing to scan")
			return nil
		}
		for _, repo := range rep.Repos {
			printRepo(out, repo)
		}
		if rep.Fired() {
			return &exitError{code: ExitCanaryFired}
		}
		return nil
	},
}

func printRepo(out io.Writer, repo canary.RepoResult) {
	status := "OK  "
	switch {
	case repo.Fired:
		status = "FIRE"
	case !repo.Judged:
		status = "skip"
	}
	fmt.Fprintf(out, "\n%s %s\n", status, repo.Repo)

	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  ticket@attempt\trole\tt1-read\tt1-create\tt1-share\thit-ratio\tnorm-work\tbilled-in\tverdict")
	for _, a := range repo.Attempts {
		role := "reuse"
		if a.Baseline {
			role = "baseline"
		}
		verdict := "ok"
		switch {
		case a.CachingInactive:
			verdict = "CACHING-OFF"
		case a.Cold:
			verdict = "COLD"
		case a.Baseline:
			verdict = "-"
		}
		share := "-"
		read, create := "-", "-"
		if a.HasFirstTurn {
			share = fmt.Sprintf("%.2f", a.ReadShare)
			read = fmt.Sprintf("%d", a.FirstTurnRead)
			create = fmt.Sprintf("%d", a.FirstTurnCreation)
		}
		fmt.Fprintf(tw, "  %s@%s\t%s\t%s\t%s\t%s\t%.2f\t%d\t%.0f\t%s\n",
			a.Ticket, a.Attempt, role, read, create, share,
			a.HitRatio, a.NormalizedWork, a.BilledInput, verdict)
	}
	tw.Flush()

	if len(repo.Findings) > 0 {
		sort.Strings(repo.Findings)
		fmt.Fprintln(out, "  findings:")
		for _, f := range repo.Findings {
			fmt.Fprintf(out, "    - %s\n", f)
		}
	}
}

func init() {
	canaryCmd.Flags().StringVar(&canaryRepo, "repo", "", "only scan this repo path")
	canaryCmd.Flags().IntVar(&canaryMinPrefix, "min-prefix", 0, "turn-1 cache_read below this = not reusing the prefix (default 1024)")
	canaryCmd.Flags().Float64Var(&canaryReadFloor, "read-share-floor", 0, "turn-1 read share below this = cold (default 0.5)")
	rootCmd.AddCommand(canaryCmd)
}
