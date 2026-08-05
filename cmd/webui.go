package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/web"
)

var webuiAddr string

var webuiCmd = &cobra.Command{
	Use:   "webui",
	Short: "Run the read-only HTMX board over a data folder",
	Long: "webui serves a read-only board of the four control states plus a per-ticket\n" +
		"detail view. It never writes to the data folder and never spawns processes.",
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
		fmt.Fprintf(cmd.OutOrStdout(), "draiver %s webui (read-only) on http://%s  data=%s\n", version, webuiAddr, root.Dir)
		return srv.Serve(webuiAddr)
	},
}

func init() {
	webuiCmd.Flags().StringVar(&webuiAddr, "addr", "127.0.0.1:7777", "address to listen on")
	rootCmd.AddCommand(webuiCmd)
}
