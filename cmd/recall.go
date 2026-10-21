package cmd

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/kb"
	"github.com/Dawil/draiver/internal/recallmetric"
	"github.com/Dawil/draiver/internal/repo"
	"github.com/Dawil/draiver/internal/store"
)

var (
	recallChain   int
	recallType    string
	recallTags    []string
	recallGrep    string
	recallAll     bool
	recallTagList bool
)

var recallCmd = &cobra.Command{
	Use:   "recall <ticket[@attempt]> [SEQ...]",
	Short: "Pull facts from an attempt's log on demand — the read side of the fold",
	Long: "recall is the deterministic read surface over the knowledge fold the brief\n" +
		"indexes (drv-026). With no seq it prints the index of live facts (seq, type,\n" +
		"tags, headline); with one or more seqs it prints their full bodies and artefact\n" +
		"pointers; with --chain it prints a fact's full supersession history, oldest to\n" +
		"newest. Filter the index with --type, --tag (repeatable; AND), and --grep, and\n" +
		"include retired facts with --all; --tags lists the distinct tags in use. The\n" +
		"positional argument accepts the ticket@attempt address form; with no @attempt it\n" +
		"resolves to the latest attempt. It reads the same on-disk, hash-chained log\n" +
		"`audit` trusts — offline, no daemon.",
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		seqArgs := args[1:]

		root, err := resolveRoot()
		if err != nil {
			return err
		}
		// Parse the ticket@attempt address form through the same parser archive/ctl use
		// (explicit @attempt wins; else --attempt / $DRAIVER_ATTEMPT / latest). Both the
		// ticket and the resolved attempt must exist.
		ticket, att, err := resolveTarget(root, id)
		if err != nil {
			return err
		}

		// Validate the mode-exclusive flags before writing anything. --chain and --tags
		// are each index-level and reject stray positional seqs; --tags and --chain are
		// mutually exclusive too.
		chainMode := cmd.Flags().Changed("chain")
		if recallTagList {
			if len(seqArgs) > 0 {
				return fmt.Errorf("--tags is an index summary; drop the positional seq(s)")
			}
			if chainMode {
				return fmt.Errorf("--tags cannot be combined with --chain")
			}
		}
		if chainMode && len(seqArgs) > 0 {
			return fmt.Errorf("--chain takes the seq as its value; drop the positional seq(s)")
		}

		events, err := repo.New(root, resolveActor()).ReadLog(ticket, att)
		if err != nil {
			return err
		}
		view := kb.Fold(events)
		out := cmd.OutOrStdout()

		// A one-line context header names the resolved target so a reader can tell which
		// attempt they are looking at. When the attempt was not chosen explicitly and the
		// ticket has siblings, it also flags their existence and points at `attempt ls`. It
		// goes to stderr so the stdout index/body stays clean for `| grep`-style consumers.
		if err := writeContextHeader(cmd.ErrOrStderr(), root, ticket, att, attemptChosenExplicitly(id)); err != nil {
			return err
		}

		// --chain: a fact's supersession history, oldest-to-newest. It takes the seq as
		// its own value, so no positional seqs are expected alongside it.
		if chainMode {
			if _, ok := view.Get(recallChain); !ok {
				return fmt.Errorf("no event #%d on %s/%s", recallChain, ticket, att)
			}
			chain := view.Chain(recallChain)
			surfaced := make([]int, 0, len(chain))
			for i, f := range chain {
				if i > 0 {
					fmt.Fprintln(out)
				}
				writeFactBody(out, f)
				surfaced = append(surfaced, f.Seq)
			}
			recordRecall(root, ticket, att, recallmetric.ModeChain, surfaced)
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
					return fmt.Errorf("no event #%d on %s/%s", s, ticket, att)
				}
				if i > 0 {
					fmt.Fprintln(out)
				}
				writeFactBody(out, f)
			}
			recordRecall(root, ticket, att, recallmetric.ModeSeq, seqs)
			return nil
		}

		// Both the index and --tags scope to the same filtered set of facts: live facts
		// (or all with --all), narrowed by --type, --tag, and --grep.
		re, err := compileGrep(recallGrep)
		if err != nil {
			return err
		}
		base := view.Live()
		if recallAll {
			base = view.Facts
		}

		// --tags: enumerate the distinct tags of the in-scope facts, sorted, with counts.
		if recallTagList {
			counts := map[string]int{}
			for _, f := range base {
				if !matchFilters(f, re) {
					continue
				}
				for _, t := range f.Tags {
					counts[t]++
				}
			}
			if len(counts) == 0 {
				fmt.Fprintln(out, "(no tags)")
				return nil
			}
			tags := make([]string, 0, len(counts))
			for t := range counts {
				tags = append(tags, t)
			}
			sort.Strings(tags)
			for _, t := range tags {
				fmt.Fprintf(out, "%-16s (%d)\n", t, counts[t])
			}
			return nil
		}

		// Default: the filtered index.
		var surfaced []int
		for _, f := range base {
			if !matchFilters(f, re) {
				continue
			}
			fmt.Fprintf(out, "%s\n", indexLine(f))
			surfaced = append(surfaced, f.Seq)
		}
		if len(surfaced) == 0 {
			fmt.Fprintln(out, "(no matching facts)")
		}
		recordRecall(root, ticket, att, recallmetric.ModeIndex, surfaced)
		return nil
	},
}

// recordRecall appends one line to the root-level recall-metrics sink naming what this
// invocation surfaced (drv-036). It is a side-channel: any sink error is swallowed, so
// instrumentation can never change recall's output or exit (AC4). The `--tags` summary
// and flag-validation errors return before reaching here and are deliberately not
// recorded — they surface no individual facts to rank.
func recordRecall(root store.Root, ticket, att, mode string, seqs []int) {
	_ = recallmetric.Append(root, recallmetric.Record{
		Actor:   resolveActor(),
		Ticket:  ticket,
		Attempt: att,
		Mode:    mode,
		Seqs:    seqs,
	})
}

// compileGrep compiles the --grep pattern, or returns nil when no pattern is set.
func compileGrep(pat string) (*regexp.Regexp, error) {
	if pat == "" {
		return nil, nil
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("--grep %q: %w", pat, err)
	}
	return re, nil
}

// matchFilters reports whether a fact passes the shared index filters: --type, --tag
// (AND), and --grep (body or headline). It is the one predicate the index and --tags
// share, so both scope to the same facts.
func matchFilters(f kb.Fact, re *regexp.Regexp) bool {
	if recallType != "" && f.Type != recallType {
		return false
	}
	if !hasAllTags(f.Tags, recallTags) {
		return false
	}
	if re != nil && !re.MatchString(f.Body) && !re.MatchString(f.Headline) {
		return false
	}
	return true
}

// attemptChosenExplicitly reports whether the user pinned the attempt rather than
// falling through to the latest: an @attempt suffix on the positional arg, the
// --attempt flag, or $DRAIVER_ATTEMPT. It drives whether the context header flags the
// existence of sibling attempts (only meaningful when the attempt was NOT chosen).
func attemptChosenExplicitly(arg string) bool {
	if strings.ContainsRune(arg, '@') {
		return true
	}
	if attemptFlag != "" {
		return true
	}
	return os.Getenv("DRAIVER_ATTEMPT") != ""
}

// writeContextHeader prints the resolved `# ticket@attempt` to w. When the attempt was
// not chosen explicitly and the ticket has more than one attempt, it extends the line
// to state the count and point at `attempt ls`, so a reader of an implicit `recall
// <ticket>` learns the siblings exist without a second command.
func writeContextHeader(w io.Writer, root store.Root, ticket, att string, explicit bool) error {
	line := fmt.Sprintf("# %s@%s", ticket, att)
	if !explicit {
		atts, err := attempt.List(root, ticket)
		if err != nil {
			return err
		}
		if len(atts) > 1 {
			line += fmt.Sprintf(" — latest of %d attempts (draiver attempt ls %s)", len(atts), ticket)
		}
	}
	fmt.Fprintln(w, line)
	return nil
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
	recallCmd.Flags().BoolVar(&recallTagList, "tags", false, "list the distinct tags of the in-scope facts (sorted, with counts) instead of the index")
	rootCmd.AddCommand(recallCmd)
}
