package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/agentmsg"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// ExitUnreadMessages signals `draiver review` was soft-stopped because the human
// posted messages during the live session that the agent has not read
// (drvctl-059). Distinct from a plain failure so a supervising loop can tell
// "read your messages first" from "errored".
const ExitUnreadMessages = 8

// messagesCmd is the agent-facing reader for human→agent messages (drvctl-059).
// It is deliberately a new verb: `draiver inbox` stays the human escalation inbox.
var messagesCmd = &cobra.Command{
	Use:   "messages [TICKET]",
	Short: "Read the human's unread messages to this agent session",
	Long: "messages prints, in full, the messages the human addressed to the agent\n" +
		"(board compose entries marked \"Message the agent\", or `draiver log --to agent`)\n" +
		"since this session started that it has not yet read, then marks them read — so\n" +
		"the `📬 N unread message(s)` notice other draiver commands print goes away until\n" +
		"the human posts again.\n\n" +
		"Inside a supervised session (DRAIVER_SESSION_* set by draiverctld) TICKET is\n" +
		"optional and defaults to the session's own attempt. Outside one — or for a\n" +
		"ticket other than the session's — it lists every message addressed to the agent\n" +
		"on the attempt, read-only (no read cursor moves).",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		sess, inSession := agentmsg.FromEnv(os.Getenv)
		var ticket, att string
		switch {
		case len(args) == 1:
			ticket, att, err = resolveTarget(root, args[0])
			if err != nil {
				return err
			}
		case inSession:
			ticket, att = sess.Ticket, sess.Attempt
		default:
			return fmt.Errorf("not inside a supervised session (%s unset): name the TICKET to list its messages", agentmsg.EnvSeq)
		}
		if !root.AttemptExists(ticket, att) {
			return fmt.Errorf("attempt %s/%s not found under %s", ticket, att, root.Dir)
		}
		events, err := ticketlog.Read(root, ticket, att)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		own := inSession && ticket == sess.Ticket && att == sess.Attempt
		if !own {
			msgs := agentmsg.Unread(events, 0, 0)
			if len(msgs) == 0 {
				fmt.Fprintf(out, "No messages to the agent on %s/%s.\n", ticket, att)
				return nil
			}
			for _, e := range msgs {
				printMessage(out, e)
			}
			return nil
		}
		cursor, _ := agentmsg.ReadCursor(root, ticket, att) // unreadable ⇒ re-show, never fail
		msgs := agentmsg.Unread(events, sess.Seq, cursor)
		if len(msgs) == 0 {
			fmt.Fprintln(out, "No unread messages from the human.")
			return nil
		}
		for _, e := range msgs {
			printMessage(out, e)
		}
		if err := agentmsg.AdvanceCursor(root, ticket, att, msgs[len(msgs)-1].Seq); err != nil {
			return fmt.Errorf("messages shown but could not be marked read: %w", err)
		}
		noun := "messages"
		if len(msgs) == 1 {
			noun = "message"
		}
		fmt.Fprintf(out, "%d %s marked read. Act on them — log what you did — before you hand off.\n", len(msgs), noun)
		return nil
	},
}

func printMessage(w io.Writer, e event.Event) {
	fmt.Fprintf(w, "── #%d %s · %s · %s ──\n", e.Seq, e.Type, e.Actor, e.TS.UTC().Format(time.RFC3339))
	fmt.Fprintln(w, strings.TrimRight(e.Body, "\n"))
	fmt.Fprintln(w)
}

// noticeCommands are the agent-facing verbs that announce unread human messages
// (drvctl-059). `messages` itself is excluded — it is the reader.
var noticeCommands = map[string]bool{
	"log": true, "test": true, "review": true, "recall": true, "brief": true,
	"park": true, "escalate": true, "status": true,
}

// noticeRepeatCommands print the notice again after their output, so a truncated
// tool transcript (long pyramid output) cannot drop it.
var noticeRepeatCommands = map[string]bool{"test": true}

// isNoticeCommand reports whether cmd is a top-level agent-facing verb.
func isNoticeCommand(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Parent() == rootCmd && noticeCommands[cmd.Name()]
}

// sessionUnread counts the live session's unread human messages. ok is false
// outside a session or on any error — the notice must fail silent, never altering
// a command's behaviour or exit code.
func sessionUnread() (sess agentmsg.Session, n int, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	sess, inSession := agentmsg.FromEnv(os.Getenv)
	if !inSession {
		return sess, 0, false
	}
	root, err := resolveRoot()
	if err != nil || !root.AttemptExists(sess.Ticket, sess.Attempt) {
		return sess, 0, false
	}
	events, err := ticketlog.Read(root, sess.Ticket, sess.Attempt)
	if err != nil {
		return sess, 0, false
	}
	cursor, err := agentmsg.ReadCursor(root, sess.Ticket, sess.Attempt)
	if err != nil {
		return sess, 0, false
	}
	return sess, len(agentmsg.Unread(events, sess.Seq, cursor)), true
}

// printUnreadNotice writes the count-only notice to stderr when the live session
// has unread messages. Silent otherwise, and on any error.
func printUnreadNotice(w io.Writer) {
	if _, n, ok := sessionUnread(); ok && n > 0 {
		fmt.Fprintln(w, agentmsg.Notice(n))
	}
}

func init() {
	rootCmd.AddCommand(messagesCmd)
}
