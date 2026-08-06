package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/web"
)

var webuiAddr string

var webuiCmd = &cobra.Command{
	Use:   "webui",
	Short: "Run the HTMX board over a data folder",
	Long: "webui serves a board of the four control states plus a per-ticket detail\n" +
		"view. Its one write path is appending a typed log entry from the detail page\n" +
		"(a note/gotcha/decision, or a Review attempt's Decision/Done action); it does\n" +
		"that by shelling the draiver CLI (log/done), so the write shares the CLI's\n" +
		"single append path rather than reimplementing it.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		srv, err := web.New(root)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "draiver %s webui (read-mostly) on http://%s  data=%s\n", version, webuiAddr, root.Dir)
		return srv.Serve(webuiAddr)
	},
}

func init() {
	webuiCmd.Flags().StringVar(&webuiAddr, "addr", "127.0.0.1:7777", "address to listen on")
	rootCmd.AddCommand(webuiCmd)
}
