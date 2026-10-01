package cmd

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
)

var (
	reportJSON   string
	reportRun    string
	reportOut    string
	reportInline bool
	reportDemo   bool
)

// reportCmd generates a standalone cucumber HTML report for a ticket's attempt
// (drv-018). It renders the runner-agnostic cucumber-JSON of a captured run — or
// any JSON file via --json — into a single self-contained file (inlined CSS/JS,
// base64-embedded screenshots) a human can open offline to review the run.
//
// With --demo it first seeds a representative run (the feature's own acceptance
// criteria as BDD scenarios) into the attempt's artefact store, which both yields
// a report to render *and* lights up the webui attempt page's inline embed — so a
// human can view and evaluate the feature before drv-017's real capture lands.
var reportCmd = &cobra.Command{
	Use:   "report TICKET",
	Short: "Generate a standalone cucumber HTML report for an attempt's run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
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
		if !root.AttemptExists(id, att) {
			return fmt.Errorf("attempt %q of ticket %q not found", att, id)
		}

		// --json renders an explicit cucumber-JSON file and never touches the store;
		// it is the runner-agnostic path that works before any capture exists.
		if reportJSON != "" {
			data, err := os.ReadFile(reportJSON)
			if err != nil {
				return fmt.Errorf("read %s: %w", reportJSON, err)
			}
			rep, err := report.Parse(data)
			if err != nil {
				return err
			}
			return renderReport(cmd, rep, id+" / "+att+" — "+reportJSON)
		}

		// --demo seeds a demonstration run into the store so the webui lights up, then
		// renders that run below via the normal discovery path.
		if reportDemo {
			stamp := time.Now().UTC().Format("20060102T150405Z")
			rel, err := report.SeedDemoRun(root, id, att, "demo0018", stamp)
			if err != nil {
				return err
			}
			reportRun = rel
			fmt.Fprintf(cmd.ErrOrStderr(), "seeded demo run %s into %s\n", rel, root.ArtefactsDir(id, att))
			fmt.Fprintf(cmd.ErrOrStderr(), "view it inline on the attempt page: http://127.0.0.1:7777/ticket/%s/%s\n",
				url.PathEscape(id), url.PathEscape(att))
		}

		run, ok, err := resolveReportRun(root, id, att, reportRun)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no BDD run found for %s/%s — capture one (drv-017), pass --json <file>, or use --demo", id, att)
		}
		rep, err := run.LoadReport()
		if err != nil {
			return err
		}
		return renderReport(cmd, rep, id+" / "+att+" — "+run.Rung+" @ "+run.ShortCommit())
	},
}

// resolveReportRun picks the run named by rel, or the attempt's latest when rel is
// empty.
func resolveReportRun(root store.Root, id, att, rel string) (report.Run, bool, error) {
	if rel == "" {
		return report.LatestRun(root, id, att)
	}
	return report.FindRun(root, id, att, rel)
}

// renderReport renders rep to a self-contained HTML file and writes it to --out (or
// stdout). The CLI always produces the standalone, offline-openable file (every
// embedding inlined); --inline=false is reserved for emitting the by-reference
// variant for debugging the webui path.
func renderReport(cmd *cobra.Command, rep *report.Report, title string) error {
	html, err := report.Render(rep, report.Options{Title: title})
	if err != nil {
		return err
	}
	if reportOut == "" || reportOut == "-" {
		_, err = cmd.OutOrStdout().Write(html)
		return err
	}
	if err := os.WriteFile(reportOut, html, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", reportOut, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "wrote standalone report (%d bytes) to %s\n", len(html), reportOut)
	return nil
}

func init() {
	reportCmd.Flags().StringVar(&reportJSON, "json", "", "render this cucumber-JSON file instead of a captured run")
	reportCmd.Flags().StringVar(&reportRun, "run", "", "render this run (its Rel id); default is the attempt's latest run")
	reportCmd.Flags().StringVarP(&reportOut, "out", "o", "", "write the HTML here (default stdout; '-' is stdout)")
	reportCmd.Flags().BoolVar(&reportInline, "inline", true, "inline every embedding for a self-contained file (always true for the CLI)")
	reportCmd.Flags().BoolVar(&reportDemo, "demo", false, "seed a demonstration run into the attempt's store, then render it")
	rootCmd.AddCommand(reportCmd)
}
