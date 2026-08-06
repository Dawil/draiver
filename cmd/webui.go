package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/web"
)

var webuiAddr string

var webuiCmd = &cobra.Command{
	Use:   "webui",
	Short: "Run the HTMX board over a data folder",
	Long: "webui serves a board of the four control states plus a per-ticket detail\n" +
		"view. It is read-mostly but for two write affordances, each of which shells\n" +
		"the draiver CLI rather than reimplementing the write in the web layer: the\n" +
		"detail page appends a typed log entry (a note/gotcha/decision, or a Review\n" +
		"attempt's Decision/Done action) via `draiver log`/`draiver done`, and a\n" +
		"Running attempt's green play button opts it into daemon supervision via\n" +
		"`draiver ctl enable` (the board twin of `ctl enable`). Board-originated writes\n" +
		"are attributed to --actor / $DRAIVER_ACTOR, else human:webui.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		srv, err := web.New(root, web.WithActor(webuiActor()))
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "draiver %s webui on http://%s  data=%s  actor=%s\n", version, webuiAddr, root.Dir, webuiActor())
		return srv.Serve(webuiAddr)
	},
}

// webuiActor resolves the identity stamped on board-originated writes (the enable
// button). An explicit --actor / $DRAIVER_ACTOR wins; otherwise events are
// attributed to human:webui — the board is a shared localhost surface, so the
// process owner is not necessarily the clicker, and a neutral webui identity is
// the honest default (rather than resolveActor's human:$USER).
func webuiActor() string {
	if actorFlag != "" {
		return actorFlag
	}
	if v := os.Getenv("DRAIVER_ACTOR"); v != "" {
		return v
	}
	return "human:webui"
}

func init() {
	webuiCmd.Flags().StringVar(&webuiAddr, "addr", "127.0.0.1:7777", "address to listen on")
	rootCmd.AddCommand(webuiCmd)
}
