package acceptance

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/systemlog"
)

// steps_relay_drv048.go realises drv-048's acceptance criteria
// (features/system-logs/operator-output-relay.feature): the stdout/stderr an operator
// action's command produces is relayed into the System Logs stream rather than
// discarded (promotion check) or truncated to a one-line summary (git control) or left
// unrelayed (review-env). Each scenario drives the REAL action path through a live
// internal/web server and reads the attempt's system.jsonl — the journal the panel
// tails — to prove the relayed output landed, each line timestamped.
//
// The promotion and git scenarios reuse the drv-046 / drv-022 board harnesses
// (seedBCBoard, whenBCClickProd; seedGitBoard, whenSyncFromPanel); only the new Givens
// and the system.jsonl assertions are defined here.

const (
	// relayPromoSentinel is echoed by the prod promotion's qa script, so finding it in a
	// promotion-check system line is unambiguous evidence the qa stdout was relayed (not
	// discarded to io.Discard as before drv-048).
	relayPromoSentinel = "RELAY-PROMO-DIAGNOSTIC-LINE"
)

func init() {
	drv048 := map[string]StepFunc{
		"a Review attempt whose prod promotion check prints diagnostic output":            givenRelayPromoDiagnostic,
		"the System Logs show the prod check's output between the started and pass lines": thenRelayPromoOutputInterleaved,
		"the System Logs show the full Sync report beyond its one-line summary":           thenRelayGitFullReport,
		"a Review attempt whose review environment prints bring-up output":                givenRelayReviewEnv,
		"I bring the review environment up from the board":                                whenRelayReviewEnvUp,
		"the System Logs show the environment's bring-up output":                          thenRelayReviewEnvOutput,
	}
	for text, fn := range drv048 {
		if _, clash := registry[text]; clash {
			panic("drv-048 step text already registered: " + text)
		}
		registry[text] = fn
	}
}

// --- promotion -----------------------------------------------------------

// relayPromoPyramidYAML declares a prod promotion whose qa echoes the sentinel to
// stdout and still exits 0 (so the check passes), plus an always-green healthcheck —
// so a real promote.Run passes AND produces output the relay must carry. The dev chip
// is declared too so the card renders normally; only prod is clicked.
func relayPromoPyramidYAML() string {
	return "environments:\n" +
		"  - name: prod\n    healthchecks:\n      - name: reachable\n        script: \"true\"\n" +
		"levels:\n  - name: unit\n    run: \"true\"\n" +
		"promotions:\n" +
		"  - environment: prod\n    base: main\n    qa: \"echo " + relayPromoSentinel + "\"\n"
}

func givenRelayPromoDiagnostic(w *World, sr *StepRun) error {
	_, err := seedBCBoard(w, bcTicket, relayPromoPyramidYAML())
	return err
}

func thenRelayPromoOutputInterleaved(w *World, sr *StepRun) error {
	b, err := currentBCBoard(w)
	if err != nil {
		return err
	}
	events, err := bcReadSystemLog(b.root, b.ticket, bcAtt)
	if err != nil {
		return err
	}
	// Locate the started line, the relayed qa output, and the pass line — all for prod.
	startIdx, outIdx, passIdx := -1, -1, -1
	for i, e := range events {
		switch {
		case e.Fields["env"] == "prod" && e.Fields["verdict"] == "started":
			if startIdx < 0 {
				startIdx = i
			}
		case e.Fields["env"] == "prod" && e.Fields["verdict"] == "pass":
			passIdx = i
		case e.Class == systemlog.ClassPromotion && strings.Contains(e.Message, relayPromoSentinel):
			outIdx = i
		}
	}
	if startIdx < 0 || passIdx < 0 {
		return fmt.Errorf("System Logs should carry prod started+pass summary lines, got %+v", events)
	}
	if outIdx < 0 {
		return fmt.Errorf("the qa stdout (%q) was not relayed into System Logs — it was discarded, got %+v", relayPromoSentinel, events)
	}
	// The relayed output is interleaved in real recorded time BETWEEN the started and the
	// verdict, exactly as a reviewer watching the panel would see it.
	if !(startIdx < outIdx && outIdx < passIdx) {
		return fmt.Errorf("the relayed qa output must sit between the started (%d) and pass (%d) lines, got it at %d", startIdx, passIdx, outIdx)
	}
	// Every relayed line owns a recorded timestamp.
	for _, e := range events {
		if strings.TrimSpace(e.TS) == "" {
			return fmt.Errorf("every System Logs line must be stamped, got %+v", e)
		}
	}
	var arc strings.Builder
	arc.WriteString("prod promotion check — relayed System Logs:\n")
	for _, e := range events {
		fmt.Fprintf(&arc, "  %s  [%s]  %s\n", e.TS, e.Class, e.Message)
	}
	attachText(sr, "drv048-promotion-relay", arc.String())
	return nil
}

// --- git -----------------------------------------------------------------

func thenRelayGitFullReport(w *World, sr *StepRun) error {
	b, err := currentGitBoard(w)
	if err != nil {
		return err
	}
	events, err := bcReadSystemLog(b.root, gitTicket, gitAtt)
	if err != nil {
		return err
	}
	var git []bcSysEvent
	for _, e := range events {
		if e.Class == systemlog.ClassGit {
			git = append(git, e)
		}
	}
	// drv-045 already logs the one-line summary (verb/outcome). drv-048 adds the report
	// body — the lines the old truncateFirstLine dropped — as further git lines. A
	// successful advanced-base Sync produces a multi-line report (pull line, back-merge
	// line, recorded-note line), so there must be MORE than the lone summary now.
	if len(git) < 2 {
		return fmt.Errorf("a multi-line Sync report should relay its body beyond the summary line, got only %+v", git)
	}
	// The first git line is the summary headline, carrying the structured fields.
	if git[0].Fields["verb"] != "sync" {
		return fmt.Errorf("the first git line should be the sync summary, got %+v", git[0])
	}
	// At least one body line carries report detail the truncated summary would have
	// dropped (here, the durable recorded-note line the back-merge produced).
	var sawBody bool
	for _, e := range git[1:] {
		if strings.TrimSpace(e.Message) == "" {
			return fmt.Errorf("a relayed git body line must not be empty: %+v", e)
		}
		if strings.Contains(strings.ToLower(e.Message), "note") || strings.Contains(e.Message, gitAtt) || strings.Contains(strings.ToLower(e.Message), "synced") {
			sawBody = true
		}
	}
	if !sawBody {
		return fmt.Errorf("the relayed body should carry the Sync report detail, got %+v", git[1:])
	}
	var arc strings.Builder
	arc.WriteString("git Sync — relayed System Logs:\n")
	for _, e := range git {
		fmt.Fprintf(&arc, "  %s  [%s]  %s\n", e.TS, e.Class, e.Message)
	}
	attachText(sr, "drv048-git-relay", arc.String())
	return nil
}

// --- review-env ----------------------------------------------------------

func givenRelayReviewEnv(w *World, sr *StepRun) error {
	_, err := seedReviewBoard(w, reviewReadyPyramid)
	return err
}

// whenRelayReviewEnvUp brings the review environment up through the LIVE webui
// (handleReviewEnvUp), so the web layer's relay wiring (reviewEnvManager's Relay sink)
// is genuinely exercised. The handler launches asynchronously, so this polls the
// attempt's system.jsonl until the bring-up output lands (or a bounded timeout). A
// force-kill cleanup is registered so the real node server and detached worktree never
// leak across the single-process suite.
func whenRelayReviewEnvUp(w *World, sr *StepRun) error {
	rb, err := currentReviewBoard(w)
	if err != nil {
		return err
	}
	w.defer_(func() { forceKillReviewEnv(rb) })

	resp, err := http.Post(rb.b.url("/ticket/"+revTicket+"/"+revAtt+"/review-env/"+revEnv+"/up"), "application/x-www-form-urlencoded", nil)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST review-env up: status %d\n%s", resp.StatusCode, body)
	}

	// Poll system.jsonl until the relayed bring-up output appears (the Launch goroutine
	// runs up.sh, which echoes "up: listening on <port>"), bounded so a stuck launch
	// fails the step rather than hanging the suite.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		events, err := bcReadSystemLog(rb.b.root, revTicket, revAtt)
		if err == nil {
			for _, e := range events {
				if e.Class == systemlog.ClassReviewEnvUp && strings.Contains(e.Message, "up: listening") {
					return nil
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("the bring-up output never reached System Logs within the timeout")
}

func thenRelayReviewEnvOutput(w *World, sr *StepRun) error {
	rb, err := currentReviewBoard(w)
	if err != nil {
		return err
	}
	events, err := bcReadSystemLog(rb.b.root, revTicket, revAtt)
	if err != nil {
		return err
	}
	var sawUp bool
	for _, e := range events {
		if strings.TrimSpace(e.TS) == "" {
			return fmt.Errorf("every System Logs line must be stamped, got %+v", e)
		}
		if e.Class == systemlog.ClassReviewEnvUp && strings.Contains(e.Message, "up: listening") {
			sawUp = true
		}
	}
	if !sawUp {
		return fmt.Errorf("the review-env bring-up output was not relayed under %q, got %+v", systemlog.ClassReviewEnvUp, events)
	}
	var arc strings.Builder
	arc.WriteString("review-env up — relayed System Logs:\n")
	for _, e := range events {
		if e.Class == systemlog.ClassReviewEnvUp {
			fmt.Fprintf(&arc, "  %s  [%s]  %s\n", e.TS, e.Class, e.Message)
		}
	}
	attachText(sr, "drv048-reviewenv-relay", arc.String())
	return nil
}
