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
	// t1-rd:cr is the turn-1 read:creation ratio (cache_read/cache_creation) — the
	// amortization health signal Anthropic's own guidance names: >1.0x (>100%) means
	// the shared prefix was read more than it was written on the first request, i.e.
	// this attempt reused a prior attempt's prefix rather than cold-writing its own.
	// Unlike hit-ratio (bounded 0–1), it is unbounded and rises the more a prefix is
	// reused — the number the ">100% cache hit" goal actually refers to.
	fmt.Fprintln(tw, "  ticket@attempt\trole\tt1-read\tt1-create\tt1-rd:cr\tt1-share\thit-ratio\tnorm-work\tbilled-in\tverdict")
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
		read, create, rdcr := "-", "-", "-"
		if a.HasFirstTurn {
			share = fmt.Sprintf("%.2f", a.ReadShare)
			read = fmt.Sprintf("%d", a.FirstTurnRead)
			create = fmt.Sprintf("%d", a.FirstTurnCreation)
			switch {
			case a.FirstTurnCreation > 0:
				rdcr = fmt.Sprintf("%.2fx", float64(a.FirstTurnRead)/float64(a.FirstTurnCreation))
			case a.FirstTurnRead > 0:
				rdcr = "∞" // read with zero cold-write: a fully warm prefix
			}
		}
		fmt.Fprintf(tw, "  %s@%s\t%s\t%s\t%s\t%s\t%s\t%.2f\t%d\t%.0f\t%s\n",
			a.Ticket, a.Attempt, role, read, create, rdcr, share,
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
