package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/web"
)

var webuiAddr string

var webuiCmd = &cobra.Command{
	Use:   "webui",
	Short: "Run the HTMX board over a data folder",
	Long: "webui serves a board of the four control states plus a per-ticket detail\n" +
		"view. It is read-mostly but for three write affordances, each of which shells\n" +
		"the draiver CLI rather than reimplementing the write in the web layer: the\n" +
		"detail page appends a typed log entry (a note/gotcha/decision, or a Review\n" +
		"attempt's Decision/Done action) via `draiver log`/`draiver done` and answers\n" +
		"an open escalation inline via `draiver resolve`, and a Running attempt's green\n" +
		"play button opts it into daemon supervision via `draiver ctl enable` (the\n" +
		"board twin of `ctl enable`). Board-originated writes are attributed to\n" +
		"--actor / $DRAIVER_ACTOR, else human:webui.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		// The default supervision mode lives in operator config (drvctl-042); the
		// capability panel folds a per-ticket override over it to show the effective
		// mode. A missing config is not an error (config.Load yields the passthrough
		// floor), so the board runs zero-config.
		cfg, err := config.Load("")
		if err != nil {
			return err
		}
		// The board's "Restart context window" button (drv-028) reaps the attempt's
		// session and hands it back to a running `ctl up` on a clean context window —
		// the `ctl restart --new-session` path. The web package can't build a
		// reconciler, so inject a client one here. Only the client Restart verb is
		// exercised (reap + flush-session + hand-off to the daemon): the daemon brings
		// the attempt up with its own config, so BaseSpec/model are irrelevant and a
		// minimal Root/Adapters/Actor reconciler suffices. With no `ctl up` running,
		// Restart returns reconcile.ErrNoController and the handler reports it.
		rec, err := reconcile.New(reconcile.Options{
			Root:     root,
			Adapters: claudeAdapters,
			Actor:    webuiActor(),
		})
		if err != nil {
			return err
		}
		restart := func(ctx context.Context, ticket, att string) error {
			_, err := rec.Restart(ctx, ticket, att, reconcile.FlushSession)
			return err
		}
		srv, err := web.New(root,
			web.WithActor(webuiActor()),
			web.WithSupervisionDefault(cfg.DefaultSupervision),
			web.WithRestart(restart),
		)
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
