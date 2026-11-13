package acceptance

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Dawil/draiver/internal/agentmsg"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
)

// steps_agentmsg_drvctl059.go realises drvctl-059 (features/agent-messages/): the
// in-session unread-message notice. Every scenario shells the REAL draiver binary
// (built once, shared with the overlay steps) against an isolated data root, with
// the DRAIVER_SESSION_* env the daemon hands a supervised session, and posts the
// human's messages through the REAL board compose handler (POST .../log) on a live
// internal/web server — so the marker the board writes is the marker the CLI counts.

const (
	amTicket = "MSG-1"
	amAtt    = "0001"
	// amSecret is embedded in every posted message body so a step can prove the
	// notice is count-only (it must never leak message content).
	amSecret = "PUSH-DIRECTLY-TO-MAIN"
)

func init() {
	steps := map[string]StepFunc{
		"a supervised agent session is live on an attempt":                          amGivenLiveSession,
		"the human posts a message to the agent from the board":                     amPostMessage,
		"the human has posted a message to the agent from the board":                amPostMessage,
		"the human posts two more messages to the agent from the board":             amPostTwo,
		"the agent logs a note":                                                     amAgentLogs,
		"the agent's command prints the unread notice with a count of 1 on stderr":  amThenNotice(1),
		"the agent's command prints the unread notice with a count of 2 on stderr":  amThenNotice(2),
		"the agent's command stdout and exit code are unchanged":                    amThenStdoutUnchanged,
		"the notice carries the `draiver messages` pointer and no message content":  amThenCountOnly,
		"the agent runs draiver messages":                                           amRunMessages,
		"the message is printed in full":                                            amThenMessagePrinted,
		"the agent's next command prints no unread notice":                          amThenNextNoNotice,
		"the agent's command prints no unread notice":                               amThenNoNotice,
		"an attempt with a message to the agent posted before any session":          amGivenPreSessionMessage,
		"a supervised agent session starts on it":                                   amStartSession,
		"spine and system notes stamped with human actors land on the attempt":      amSpineNotes,
		"a human runs draiver log outside any session":                              amHumanLogsOutside,
		"the session's read cursor file is unreadable":                              amBreakCursor,
		"the agent claims review":                                                   amClaimReview(false),
		"the agent claims review with --ignore-unread":                              amClaimReview(true),
		"the review is refused and no event is appended":                            amThenReviewRefused,
		"the refusal prints the unread notice and the --ignore-unread hint":         amThenRefusalText,
		"the review claim is recorded":                                              amThenReviewRecorded,
		"the agent runs draiver test":                                               amRunTest,
		"the test output carries the unread notice at the top and again at the end": amThenTestNoticeTwice,
		"the human reopens the attempt with a decision":                             amReopen,
		"the next resume note lists the unread message":                             amThenResumeNoteLists,
		"draiver inbox prints the same output as before the message and no notice":  amThenInboxUnchanged,
		"the human opens the attempt page":                                          amOpenPage,
		"the compose form offers a \"Message the agent\" box checked by default":    amThenComposeCheckbox,
		"the posted message carries a \"to agent\" badge in the log":                amThenBadge,
	}
	for text, fn := range steps {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registered: " + text)
		}
		registry[text] = fn
	}
}

// amWorld is one scenario's isolated draiver install plus the live board.
type amWorld struct {
	dataRoot, configPath, cacheDir, workdir string
	root                                    store.Root
	srv                                     *httptest.Server
	inSession                               bool
	sessionSeq                              int
	posted                                  []int // seqs of board-posted messages

	// last shelled command
	stdout, stderr string
	code           int
	// inboxBefore is `draiver inbox` captured before any message was posted.
	inboxBefore string
	// before is the log length captured just before a review attempt.
	before int
	page   string
}

func amGet(w *World) (*amWorld, error) {
	a, _ := w.get("am").(*amWorld)
	if a == nil {
		return nil, errors.New("no drvctl-059 world in scope")
	}
	return a, nil
}

func amSetup(w *World) (*amWorld, error) {
	base, err := os.MkdirTemp("", "acc-drvctl059-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(base) })
	a := &amWorld{
		dataRoot:   filepath.Join(base, "data"),
		configPath: filepath.Join(base, "config.json"),
		cacheDir:   filepath.Join(base, "cache"),
		workdir:    filepath.Join(base, "work"),
	}
	for _, d := range []string{a.dataRoot, a.cacheDir, a.workdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	a.root = store.Root{Dir: a.dataRoot}
	w.set("am", a)
	// The attempt's repo is a plain (non-git) dir: review's pyramid gate has no
	// checkout to fold there, so only the drvctl-059 soft stop can refuse a claim.
	if _, err := a.run(false, "human:dave", "new", amTicket, "--title", "drvctl-059 messages", "--repo", a.workdir); err != nil {
		return nil, err
	}
	srv, err := web.New(a.root)
	if err != nil {
		return nil, err
	}
	a.srv = httptest.NewServer(srv.Handler())
	w.defer_(a.srv.Close)
	// The shared screenshot helper reads the board from the World.
	w.set("board", &board{root: a.root, wd: a.workdir, srv: a.srv})
	inbox, err := a.run(false, "human:dave", "inbox")
	if err != nil {
		return nil, err
	}
	a.inboxBefore = inbox
	return a, nil
}

// startSession does what the daemon's bringUp does: snapshot the log tail as the
// session watermark (persisted off-chain) and hand it to the session's env.
func (a *amWorld) startSession() error {
	evs, err := ticketlog.Read(a.root, amTicket, amAtt)
	if err != nil {
		return err
	}
	a.sessionSeq = evs[len(evs)-1].Seq
	a.inSession = true
	return agentmsg.WriteWatermark(a.root, amTicket, amAtt, a.sessionSeq)
}

func (a *amWorld) env(session bool, actor string) []string {
	drop := map[string]bool{
		"DRAIVER_DATA": true, "DRAIVER_CONFIG": true, "XDG_CACHE_HOME": true,
		"DRAIVER_ACTOR": true, "DRAIVER_ATTEMPT": true,
		agentmsg.EnvTicket: true, agentmsg.EnvAttempt: true, agentmsg.EnvSeq: true,
	}
	var env []string
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i >= 0 && drop[e[:i]] {
			continue
		}
		env = append(env, e)
	}
	env = append(env,
		"DRAIVER_DATA="+a.dataRoot,
		"DRAIVER_CONFIG="+a.configPath,
		"XDG_CACHE_HOME="+a.cacheDir,
		"DRAIVER_ACTOR="+actor,
	)
	if session {
		env = append(env, "DRAIVER_ATTEMPT="+amAtt)
		env = append(env, agentmsg.SessionEnv(amTicket, amAtt, a.sessionSeq)...)
	}
	return env
}

// run shells the real binary, recording stdout/stderr/exit separately. It returns
// stdout and an error only when the binary could not be run at all or (for setup
// calls) exited nonzero.
func (a *amWorld) run(session bool, actor string, args ...string) (string, error) {
	bin, err := overlayBin()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = a.workdir
	cmd.Env = a.env(session, actor)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	a.stdout, a.stderr, a.code = out.String(), errb.String(), 0
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		a.code = ee.ExitCode()
	} else if runErr != nil {
		return "", runErr
	}
	if !session && a.code != 0 {
		return a.stdout, fmt.Errorf("draiver %v exited %d:\n%s%s", args, a.code, a.stdout, a.stderr)
	}
	return a.stdout, nil
}

func (a *amWorld) agent(args ...string) error {
	_, err := a.run(true, "agent:claude-code", args...)
	return err
}

// postBoard posts a message through the real board compose handler, exactly as the
// form does with its default-checked "Message the agent" box.
func (a *amWorld) postBoard(typ, body string, toAgent bool) error {
	form := url.Values{"type": {typ}, "body": {body}}
	if toAgent {
		form.Set("to", "agent")
	}
	resp, err := http.PostForm(a.srv.URL+"/ticket/"+amTicket+"/"+amAtt+"/log", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("board compose POST = %d: %s", resp.StatusCode, b)
	}
	evs, err := ticketlog.Read(a.root, amTicket, amAtt)
	if err != nil {
		return err
	}
	last := evs[len(evs)-1]
	if toAgent && last.To != agentmsg.ToAgent {
		return fmt.Errorf("board-posted message #%d is not marked to: agent", last.Seq)
	}
	a.posted = append(a.posted, last.Seq)
	return nil
}

func (a *amWorld) logLen() (int, error) {
	evs, err := ticketlog.Read(a.root, amTicket, amAtt)
	return len(evs), err
}

// ---- givens / whens ----

func amGivenLiveSession(w *World, sr *StepRun) error {
	a, err := amSetup(w)
	if err != nil {
		return err
	}
	return a.startSession()
}

func amGivenPreSessionMessage(w *World, sr *StepRun) error {
	a, err := amSetup(w)
	if err != nil {
		return err
	}
	return a.postBoard("note", "Before you start: "+amSecret, true)
}

func amStartSession(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	return a.startSession()
}

func amPostMessage(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	return a.postBoard("decision", "If possible, "+amSecret+" on the forgejo remote once done.", true)
}

func amPostTwo(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if err := a.postBoard("note", "Second thought: "+amSecret, true); err != nil {
		return err
	}
	return a.postBoard("gotcha", "Also: "+amSecret, true)
}

func amAgentLogs(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if a.inSession {
		return a.agent("log", amTicket, "--type", "note", "Implemented the next slice.")
	}
	_, err = a.run(false, "human:dave", "log", amTicket, "--type", "note", "Implemented the next slice.")
	return err
}

func amHumanLogsOutside(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	_, err = a.run(false, "human:dave", "log", amTicket, "--type", "note", "A human, in a terminal.")
	return err
}

func amSpineNotes(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	// The exact shapes the spec calls out: spine notes stamped human:*, appended
	// through the same gateway every write uses, but never carrying the marker.
	for _, e := range []event.Event{
		{Type: "note", Actor: "human:david", Body: "Daemon reclaimed the worktree."},
		{Type: "note", Actor: "human:webui", Body: "Restart flush: session context discarded."},
		{Type: "note", Actor: "human:david", Body: "Synced branch with forgejo/main."},
	} {
		if _, err := ticketlog.Append(a.root, amTicket, amAtt, e); err != nil {
			return err
		}
	}
	// And an unmarked board note ("Message the agent" unchecked).
	return a.postBoard("note", "Just for the humans.", false)
}

func amBreakCursor(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	dir := a.root.SessionDir(amTicket, amAtt)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "messages-read.json"), []byte("{not json"), 0o644)
}

func amRunMessages(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if err := a.agent("messages"); err != nil {
		return err
	}
	if a.code != 0 {
		return fmt.Errorf("draiver messages exited %d: %s", a.code, a.stderr)
	}
	w.set("am-messages", a.stdout)
	attachText(sr, "draiver-messages", "$ draiver messages\n"+a.stdout)
	return nil
}

func amClaimReview(ignore bool) StepFunc {
	return func(w *World, sr *StepRun) error {
		a, err := amGet(w)
		if err != nil {
			return err
		}
		if a.before, err = a.logLen(); err != nil {
			return err
		}
		args := []string{"review", amTicket, "Claiming review."}
		if ignore {
			args = append(args, "--ignore-unread")
		}
		if err := a.agent(args...); err != nil {
			return err
		}
		attachText(sr, "review", fmt.Sprintf("$ draiver %s\n[exit %d]\n--- stdout\n%s--- stderr\n%s", strings.Join(args, " "), a.code, a.stdout, a.stderr))
		return nil
	}
}

func amRunTest(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if err := a.agent("test"); err != nil {
		return err
	}
	attachText(sr, "draiver-test", fmt.Sprintf("$ draiver test\n[exit %d]\n--- stdout\n%s--- stderr\n%s", a.code, a.stdout, a.stderr))
	return nil
}

func amReopen(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	// The board's Review "Decision — reopen" action.
	return a.postBoard("decision", "Reopened to Running.", true)
}

func amOpenPage(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	resp, err := http.Get(a.srv.URL + "/ticket/" + amTicket + "/" + amAtt)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	a.page = string(b)
	return nil
}

// ---- thens ----

func amThenNotice(n int) StepFunc {
	return func(w *World, sr *StepRun) error {
		a, err := amGet(w)
		if err != nil {
			return err
		}
		want := agentmsg.Notice(n)
		if !strings.Contains(a.stderr, want) {
			return fmt.Errorf("stderr missing notice %q:\n%s", want, a.stderr)
		}
		if strings.Contains(a.stdout, "📬") {
			return fmt.Errorf("the notice must go to stderr only; stdout:\n%s", a.stdout)
		}
		attachText(sr, "notice", "--- stdout\n"+a.stdout+"--- stderr\n"+a.stderr)
		return nil
	}
}

func amThenStdoutUnchanged(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if a.code != 0 {
		return fmt.Errorf("exit code changed: %d (%s)", a.code, a.stderr)
	}
	if !strings.HasPrefix(a.stdout, "logged note #") || strings.Count(a.stdout, "\n") != 1 {
		return fmt.Errorf("stdout is not exactly the usual one-line log receipt:\n%q", a.stdout)
	}
	return nil
}

func amThenCountOnly(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if !strings.Contains(a.stderr, "`draiver messages`") {
		return fmt.Errorf("notice lacks the `draiver messages` pointer:\n%s", a.stderr)
	}
	if strings.Contains(a.stderr, amSecret) || strings.Contains(a.stderr, "forgejo") {
		return fmt.Errorf("notice leaked message content:\n%s", a.stderr)
	}
	return nil
}

func amThenMessagePrinted(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	out, _ := w.get("am-messages").(string)
	if !strings.Contains(out, "If possible, "+amSecret+" on the forgejo remote once done.") {
		return fmt.Errorf("draiver messages did not print the message in full:\n%s", out)
	}
	if !strings.Contains(out, "#"+strconv.Itoa(a.posted[0])) {
		return fmt.Errorf("draiver messages should name the message seq #%d:\n%s", a.posted[0], out)
	}
	return nil
}

func amThenNextNoNotice(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if err := a.agent("recall", amTicket); err != nil {
		return err
	}
	return amThenNoNotice(w, sr)
}

func amThenNoNotice(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if strings.Contains(a.stderr+a.stdout, "📬") || strings.Contains(a.stderr, "unread") {
		return fmt.Errorf("expected no unread notice, got:\n--- stdout\n%s--- stderr\n%s", a.stdout, a.stderr)
	}
	if a.code != 0 {
		return fmt.Errorf("command exited %d: %s", a.code, a.stderr)
	}
	return nil
}

func amThenReviewRefused(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if a.code == 0 {
		return fmt.Errorf("review with unread messages must exit nonzero; stdout:\n%s", a.stdout)
	}
	after, err := a.logLen()
	if err != nil {
		return err
	}
	if after != a.before {
		return fmt.Errorf("a refused review must append no event: log %d -> %d", a.before, after)
	}
	return nil
}

func amThenRefusalText(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	for _, want := range []string{agentmsg.Notice(1), "review not recorded", "--ignore-unread", "`draiver messages`"} {
		if !strings.Contains(a.stderr, want) {
			return fmt.Errorf("refusal missing %q:\n%s", want, a.stderr)
		}
	}
	return nil
}

func amThenReviewRecorded(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if a.code != 0 || !strings.Contains(a.stdout, "review claimed") {
		return fmt.Errorf("review should be claimed: exit %d\n%s%s", a.code, a.stdout, a.stderr)
	}
	evs, err := ticketlog.Read(a.root, amTicket, amAtt)
	if err != nil {
		return err
	}
	if last := evs[len(evs)-1]; last.Type != "review" {
		return fmt.Errorf("last event is %q, want review", last.Type)
	}
	return nil
}

func amThenTestNoticeTwice(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	notice := agentmsg.Notice(1)
	lines := strings.Split(strings.TrimRight(a.stderr, "\n"), "\n")
	if len(lines) < 1 || lines[0] != notice || lines[len(lines)-1] != notice {
		return fmt.Errorf("draiver test must open and close its stderr with the notice:\n%s", a.stderr)
	}
	if strings.Contains(a.stdout, "📬") {
		return fmt.Errorf("notice leaked onto test stdout:\n%s", a.stdout)
	}
	return nil
}

func amThenResumeNoteLists(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	note := reconcile.ResumeWakeNote(a.root, amTicket, amAtt)
	msg := a.posted[0]
	if !strings.Contains(note, fmt.Sprintf("#%d", msg)) || !strings.Contains(note, amSecret) {
		return fmt.Errorf("resume note must list the never-read message #%d even though the review sits above it:\n%s", msg, note)
	}
	attachText(sr, "resume-note", note)
	return nil
}

func amThenInboxUnchanged(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if _, err := a.run(true, "human:dave", "inbox"); err != nil {
		return err
	}
	if a.stdout != a.inboxBefore {
		return fmt.Errorf("inbox output changed:\nbefore:\n%s\nafter:\n%s", a.inboxBefore, a.stdout)
	}
	if strings.Contains(a.stdout+a.stderr, "📬") || strings.Contains(a.stdout, amSecret) {
		return fmt.Errorf("inbox must not carry the notice or messages:\n%s%s", a.stdout, a.stderr)
	}
	return nil
}

func amThenComposeCheckbox(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	if !strings.Contains(a.page, `name="to" value="agent" data-testid="compose-to-agent" checked`) {
		return errors.New(`compose form lacks the default-checked "Message the agent" box`)
	}
	if !strings.Contains(a.page, "Message the agent") {
		return errors.New(`compose form lacks the "Message the agent" label`)
	}
	return nil
}

func amThenBadge(w *World, sr *StepRun) error {
	a, err := amGet(w)
	if err != nil {
		return err
	}
	testid := fmt.Sprintf(`data-testid="to-agent-%d"`, a.posted[0])
	if !strings.Contains(a.page, testid) {
		return fmt.Errorf("posted message #%d lacks the to-agent badge", a.posted[0])
	}
	return shoot(w, sr, "/ticket/"+amTicket+"/"+amAtt, "compose-message-the-agent", true, "[data-testid=compose-to-agent]", "")
}
