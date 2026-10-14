package cmd

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/kb"
	"github.com/Dawil/draiver/internal/repo"
)

var (
	recallChain int
	recallType  string
	recallTags  []string
	recallGrep  string
	recallAll   bool
)

var recallCmd = &cobra.Command{
	Use:   "recall TICKET [SEQ...]",
	Short: "Pull facts from an attempt's log on demand — the read side of the fold",
	Long: "recall is the deterministic read surface over the knowledge fold the brief\n" +
		"indexes (drv-026). With no seq it prints the index of live facts (seq, type,\n" +
		"tags, headline); with one or more seqs it prints their full bodies and artefact\n" +
		"pointers; with --chain it prints a fact's full supersession history, oldest to\n" +
		"newest. Filter the index with --type, --tag (repeatable; AND), and --grep, and\n" +
		"include retired facts with --all. It reads the same on-disk, hash-chained log\n" +
		"`audit` trusts — offline, no daemon.",
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		seqArgs := args[1:]

		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if !root.Exists(id) {
			return fmt.Errorf("ticket %q not found under %s", id, root.Dir)
		}
		att, err := resolveAttempt(root, id)
		if err != nil {
			return err
		}
		events, err := repo.New(root, resolveActor()).ReadLog(id, att)
		if err != nil {
			return err
		}
		view := kb.Fold(events)
		out := cmd.OutOrStdout()

		// --chain: a fact's supersession history, oldest-to-newest. It takes the seq as
		// its own value, so no positional seqs are expected alongside it.
		if cmd.Flags().Changed("chain") {
			if len(seqArgs) > 0 {
				return fmt.Errorf("--chain takes the seq as its value; drop the positional seq(s)")
			}
			if _, ok := view.Get(recallChain); !ok {
				return fmt.Errorf("no event #%d on %s/%s", recallChain, id, att)
			}
			chain := view.Chain(recallChain)
			for i, f := range chain {
				if i > 0 {
					fmt.Fprintln(out)
				}
				writeFactBody(out, f)
			}
			return nil
		}

		// Positional seqs: the full bodies of exactly those events.
		if len(seqArgs) > 0 {
			seqs, err := parseSeqs(seqArgs)
			if err != nil {
				return err
			}
			for i, s := range seqs {
				f, ok := view.Get(s)
				if !ok {
					return fmt.Errorf("no event #%d on %s/%s", s, id, att)
				}
				if i > 0 {
					fmt.Fprintln(out)
				}
				writeFactBody(out, f)
			}
			return nil
		}

		// Default: the filtered index.
		var re *regexp.Regexp
		if recallGrep != "" {
			if re, err = regexp.Compile(recallGrep); err != nil {
				return fmt.Errorf("--grep %q: %w", recallGrep, err)
			}
		}
		base := view.Live()
		if recallAll {
			base = view.Facts
		}
		var shown int
		for _, f := range base {
			if recallType != "" && f.Type != recallType {
				continue
			}
			if !hasAllTags(f.Tags, recallTags) {
				continue
			}
			if re != nil && !re.MatchString(f.Body) && !re.MatchString(f.Headline) {
				continue
			}
			fmt.Fprintf(out, "%s\n", indexLine(f))
			shown++
		}
		if shown == 0 {
			fmt.Fprintln(out, "(no matching facts)")
		}
		return nil
	},
}

// indexLine renders one index entry: `#<seq> <type> [tags] — <headline>`, with a
// trailing `(superseded by #N)` marker when a retired fact surfaces under --all.
func indexLine(f kb.Fact) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%d %s", f.Seq, f.Type)
	if len(f.Tags) > 0 {
		fmt.Fprintf(&sb, " [%s]", strings.Join(f.Tags, " "))
	}
	fmt.Fprintf(&sb, " — %s", f.Headline)
	if !f.Live {
		fmt.Fprintf(&sb, " (superseded by #%d)", f.SupersededBy)
	}
	return sb.String()
}

// writeFactBody prints a fact's full body form: a header line, the body, artefact
// pointers, and a retirement marker when the fact is no longer live.
func writeFactBody(out io.Writer, f kb.Fact) {
	fmt.Fprintf(out, "### #%d %s", f.Seq, f.Type)
	if len(f.Tags) > 0 {
		fmt.Fprintf(out, " [%s]", strings.Join(f.Tags, " "))
	}
	fmt.Fprintf(out, " — %s — %s\n", f.Actor, f.TS.UTC().Format("2006-01-02T15:04:05Z07:00"))
	if body := strings.TrimSpace(f.Body); body != "" {
		fmt.Fprintf(out, "%s\n", body)
	}
	for _, art := range f.Artefacts {
		fmt.Fprintf(out, "[artefact: %s]\n", art)
	}
	if !f.Live {
		fmt.Fprintf(out, "→ superseded by #%d\n", f.SupersededBy)
	}
}

// hasAllTags reports whether tags contains every wanted tag (the --tag AND filter).
func hasAllTags(tags, wanted []string) bool {
	for _, w := range wanted {
		found := false
		for _, t := range tags {
			if t == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// parseSeqs turns positional seq args into ints.
func parseSeqs(args []string) ([]int, error) {
	out := make([]int, 0, len(args))
	for _, a := range args {
		var n int
		a = strings.TrimPrefix(strings.TrimSpace(a), "#")
		if _, err := fmt.Sscanf(a, "%d", &n); err != nil {
			return nil, fmt.Errorf("invalid seq %q: want an integer", a)
		}
		out = append(out, n)
	}
	return out, nil
}

func init() {
	recallCmd.Flags().IntVar(&recallChain, "chain", 0, "print the full supersession history of this seq (oldest to newest)")
	recallCmd.Flags().StringVar(&recallType, "type", "", "filter the index to one event type")
	recallCmd.Flags().StringSliceVar(&recallTags, "tag", nil, "filter the index to facts carrying this tag (repeatable; AND)")
	recallCmd.Flags().StringVar(&recallGrep, "grep", "", "filter the index to facts whose body or headline matches this regexp")
	recallCmd.Flags().BoolVar(&recallAll, "all", false, "include superseded facts in the index")
	rootCmd.AddCommand(recallCmd)
}
