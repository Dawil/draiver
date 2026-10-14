package acceptance

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/audit"
	"github.com/Dawil/draiver/internal/brief"
	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/kb"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// attachText records a CLI scenario's evidence as a text/plain embedding — the
// recall/brief output that IS the agent-facing surface. Every BDD scenario must
// carry at least one artefact (acceptance_test.go:TestEveryScenarioHasAnArtifact);
// a CLI feature's artefact is its captured output, not a screenshot. persistEmbeddings
// base64-decodes Data and writes it to the screenshots dir as a .txt sidecar.
func attachText(sr *StepRun, name, content string) {
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "text/plain",
		Name:     name,
		Data:     base64.StdEncoding.EncodeToString([]byte(content)),
	})
}

// recallIndexLines renders live facts the way `draiver recall` prints its index —
// `#seq type [tags] — headline` — for an evidence artefact.
func recallIndexLines(facts []kb.Fact) string {
	var b strings.Builder
	for _, f := range facts {
		fmt.Fprintf(&b, "#%d %s", f.Seq, f.Type)
		if len(f.Tags) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(f.Tags, " "))
		}
		fmt.Fprintf(&b, " — %s\n", f.Headline)
	}
	return b.String()
}

// getBody GETs a URL and returns its body as a string (for asserting on served HTML).
func getBody(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s = %d, want 200", url, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// These steps exercise drv-026's progressive-reveal fold end to end over a real
// on-disk, hash-chained attempt log — the same primitives `draiver brief`, `recall`
// and `audit` wrap. The fold (internal/kb) is the engine `recall` is a thin CLI over,
// so asserting on kb.Fold + brief.Build is asserting on the agent-facing surface.

const (
	recTicket = "KB-1"
	recAtt    = "0001"
)

// seedKBData makes a throwaway data root with one attempt and a spec, registered for
// cleanup on the World, and returns it.
func seedKBData(w *World) (store.Root, error) {
	dir, err := os.MkdirTemp("", "acc-kb-")
	if err != nil {
		return store.Root{}, err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	root := store.Root{Dir: dir}
	if err := root.EnsureAttemptDirs(recTicket, recAtt); err != nil {
		return store.Root{}, err
	}
	spec := "---\nid: " + recTicket + "\ntitle: progressive reveal\n---\n\n# progressive reveal\n"
	if err := os.WriteFile(root.SpecPath(recTicket), []byte(spec), 0o644); err != nil {
		return store.Root{}, err
	}
	return root, nil
}

func appendKB(root store.Root, e event.Event) (event.Event, error) {
	e.Actor = "agent:acceptance"
	return ticketlog.Append(root, recTicket, recAtt, e)
}

func kbView(root store.Root) (kb.View, error) {
	events, err := ticketlog.Read(root, recTicket, recAtt)
	if err != nil {
		return kb.View{}, err
	}
	return kb.Fold(events), nil
}

// replaySize approximates the pre-drv-026 brief, which replayed every event body
// verbatim — the baseline the progressive brief must beat.
func replaySize(root store.Root) (int, error) {
	events, err := ticketlog.Read(root, recTicket, recAtt)
	if err != nil {
		return 0, err
	}
	var n int
	for _, e := range events {
		n += len(e.Body)
	}
	return n, nil
}

// ---- recall.feature: supersede + recall ----

func givenSupersededFact(w *World, sr *StepRun) error {
	root, err := seedKBData(w)
	if err != nil {
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "created", Body: "start"}); err != nil { // #1
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "gotcha", Body: "keys only work in test mode"}); err != nil { // #2
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "gotcha", Body: "actually live keys 401 until verified", Supersedes: []int{2}}); err != nil { // #3
		return err
	}
	w.set("root", root)
	return nil
}

func thenBriefIndexesLiveOnly(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	out, err := brief.Build(root, recTicket, recAtt)
	if err != nil {
		return err
	}
	if !strings.Contains(out, "#3 gotcha") || !strings.Contains(out, "actually live keys 401") {
		return fmt.Errorf("brief does not index live fact #3:\n%s", out)
	}
	if strings.Contains(out, "only work in test mode") || strings.Contains(out, "#2 gotcha") {
		return fmt.Errorf("brief still replays superseded fact #2:\n%s", out)
	}
	attachText(sr, "brief-live-index", "$ draiver brief "+recTicket+"\n\n"+out)
	return nil
}

func thenRecallBodyReturns(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	view, err := kbView(root)
	if err != nil {
		return err
	}
	f, ok := view.Get(2)
	if !ok {
		return fmt.Errorf("recall could not find superseded seq #2")
	}
	if !strings.Contains(f.Body, "only work in test mode") {
		return fmt.Errorf("recall #2 body wrong: %q", f.Body)
	}
	if f.Live {
		return fmt.Errorf("#2 should be retired, recall reports it live")
	}
	return nil
}

func thenRecallChainOrdered(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	view, err := kbView(root)
	if err != nil {
		return err
	}
	chain := view.Chain(3)
	if len(chain) != 2 || chain[0].Seq != 2 || chain[1].Seq != 3 {
		return fmt.Errorf("chain(3) = %+v, want [#2 #3] oldest→newest", chain)
	}
	return nil
}

func thenAuditPasses(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	res, err := audit.VerifyAttempt(root, recTicket, recAtt)
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("audit failed after superseding: %s", res.Reason)
	}
	return nil
}

func givenTwoTaggedFacts(w *World, sr *StepRun) error {
	root, err := seedKBData(w)
	if err != nil {
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "created", Body: "start"}); err != nil {
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "gotcha", Body: "auth tokens expire hourly", Tags: []string{"auth"}}); err != nil {
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "decision", Body: "shard the db by tenant", Tags: []string{"db"}}); err != nil {
		return err
	}
	w.set("root", root)
	return nil
}

func thenRecallByTagFilters(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	view, err := kbView(root)
	if err != nil {
		return err
	}
	var auth, other int
	for _, f := range view.Live() {
		for _, tg := range f.Tags {
			if tg == "auth" {
				auth++
			} else {
				other++
			}
		}
	}
	if auth != 1 {
		return fmt.Errorf("expected exactly one auth-tagged live fact, got %d", auth)
	}
	// draiver attaches no behaviour to the value: the db-tagged fact is equally live.
	if other != 1 {
		return fmt.Errorf("the db tag should carry no special meaning (one other-tagged fact), got %d", other)
	}
	var authFacts []kb.Fact
	for _, f := range view.Live() {
		for _, tg := range f.Tags {
			if tg == "auth" {
				authFacts = append(authFacts, f)
			}
		}
	}
	attachText(sr, "recall-tag-auth",
		"$ draiver recall "+recTicket+" --tag auth\n"+recallIndexLines(authFacts)+
			"\n# the db-tagged fact is equally live; the tag only filters search:\n$ draiver recall "+recTicket+"\n"+recallIndexLines(view.Live()))
	return nil
}

// ---- cold-start.feature: the brief scales with live facts (AC7) ----

// seedBusy appends `live` independent live gotchas, then a supersession chain of
// length `chain` (each link retires the previous), leaving `live`+1 live facts and
// chain-1 retired — a busy log whose live-fact count is independent of chain length.
func seedBusy(root store.Root, live, chain int) error {
	if _, err := appendKB(root, event.Event{Type: "created", Body: "start"}); err != nil {
		return err
	}
	for i := 0; i < live; i++ {
		if _, err := appendKB(root, event.Event{Type: "note", Body: fmt.Sprintf("durable live fact number %d that stays in the index", i)}); err != nil {
			return err
		}
	}
	var prev int
	for i := 0; i < chain; i++ {
		e := event.Event{Type: "decision", Body: fmt.Sprintf("revision %d of the evolving design, a long superseded body that the old brief would have replayed verbatim", i)}
		if prev != 0 {
			e.Supersedes = []int{prev}
		}
		got, err := appendKB(root, e)
		if err != nil {
			return err
		}
		prev = got.Seq
	}
	return nil
}

func givenBusyAttempt(w *World, sr *StepRun) error {
	root, err := seedKBData(w)
	if err != nil {
		return err
	}
	if err := seedBusy(root, 3, 40); err != nil {
		return err
	}
	w.set("root", root)
	return nil
}

func thenBriefSmallerThanReplay(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	out, err := brief.Build(root, recTicket, recAtt)
	if err != nil {
		return err
	}
	replay, err := replaySize(root)
	if err != nil {
		return err
	}
	if len(out) >= replay {
		return fmt.Errorf("brief (%d bytes) is not smaller than the full-log replay (%d bytes)", len(out), replay)
	}
	w.set("briefLen", len(out))
	attachText(sr, "brief-vs-replay", fmt.Sprintf(
		"full-log replay (pre-drv-026): %d bytes\nprogressive brief:             %d bytes (%.0f%% smaller)\n\n--- brief ---\n%s",
		replay, len(out), 100*(1-float64(len(out))/float64(replay)), out))
	return nil
}

func thenMoreHistoryKeepsBriefFlat(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	before := w.get("briefLen").(int)
	// Extend the supersession chain substantially: live-fact count is unchanged, so
	// the brief must stay essentially flat even as the retired history grows.
	events, err := ticketlog.Read(root, recTicket, recAtt)
	if err != nil {
		return err
	}
	prev := events[len(events)-1].Seq
	for i := 0; i < 60; i++ {
		got, err := appendKB(root, event.Event{Type: "decision", Body: fmt.Sprintf("further revision %d, another long superseded body", i), Supersedes: []int{prev}})
		if err != nil {
			return err
		}
		prev = got.Seq
	}
	out, err := brief.Build(root, recTicket, recAtt)
	if err != nil {
		return err
	}
	grew := len(out) - before
	if grew < 0 {
		grew = -grew
	}
	// The head fact's headline is identical in shape, so any drift is a handful of
	// bytes — certainly under the ~5 KB of body text 60 new superseded events carry.
	if grew > 512 {
		return fmt.Errorf("brief grew by %d bytes as superseded history grew — it should track live facts, not total events", grew)
	}
	return nil
}

// ---- talk.feature: talk to the attempt via recall (AC8) ----

func givenAttemptWithKnowledge(w *World, sr *StepRun) error {
	root, err := seedKBData(w)
	if err != nil {
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "created", Body: "start"}); err != nil {
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "decision", Body: "chose server-side pagination", Tags: []string{"api"}}); err != nil { // #2
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "gotcha", Body: "the staging cache ignores the tenant header"}); err != nil { // #3
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "decision", Body: "pagination must be cursor-based after all", Supersedes: []int{2}}); err != nil { // #4 retires #2
		return err
	}
	w.set("root", root)
	return nil
}

func thenRecallAnswersGotchas(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	view, err := kbView(root)
	if err != nil {
		return err
	}
	// "What gotchas are known?" — the live gotchas.
	var gotchas []kb.Fact
	for _, f := range view.Live() {
		if f.Type == "gotcha" {
			gotchas = append(gotchas, f)
		}
	}
	if len(gotchas) != 1 || !strings.Contains(gotchas[0].Body, "ignores the tenant header") {
		return fmt.Errorf("recall could not answer 'known gotchas': %+v", gotchas)
	}
	attachText(sr, "talk-known-gotchas",
		"Q: what gotchas are known on this attempt?\n$ draiver recall "+recTicket+" --type gotcha\n"+
			recallIndexLines(gotchas)+"\nA: "+gotchas[0].Body)
	return nil
}

func thenRecallAnswersHistory(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	view, err := kbView(root)
	if err != nil {
		return err
	}
	// "What was decided about pagination, and how did it change?" — the chain of the
	// current decision walks back to the superseded one.
	chain := view.Chain(4)
	if len(chain) != 2 || chain[0].Seq != 2 || chain[1].Seq != 4 {
		return fmt.Errorf("recall --chain could not answer the history of the pagination decision: %+v", chain)
	}
	if !strings.Contains(chain[0].Body, "server-side") || !strings.Contains(chain[1].Body, "cursor-based") {
		return fmt.Errorf("the pagination history bodies are wrong: %+v", chain)
	}
	return nil
}

// ---- board.feature: faithful display (AC6) ----

func givenBoardWithSupersededAndTags(w *World, sr *StepRun) error {
	b, err := seedBoard(w, greenPyramid)
	if err != nil {
		return err
	}
	// Append onto the board's ACC-1/0001 attempt: a tagged fact and a later fact that
	// supersedes it, plus a resolved escalation, so the page shows every demotion.
	if _, err := ticketlog.Append(b.root, accTicket, accAtt, event.Event{Type: "gotcha", Actor: "agent:acceptance", Body: "keys only work in test mode", Tags: []string{"auth", "billing"}}); err != nil {
		return err
	}
	if _, err := ticketlog.Append(b.root, accTicket, accAtt, event.Event{Type: "gotcha", Actor: "agent:acceptance", Body: "actually live keys 401 until verified", Supersedes: []int{2}}); err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

func thenPageDemotesSuperseded(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/ticket/" + accTicket + "/" + accAtt))
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="superseded-2"`) || !strings.Contains(html, `href="#event-3"`) {
		return fmt.Errorf("attempt page does not demote superseded #2 with a link to #3")
	}
	if !strings.Contains(html, "event-superseded") {
		return fmt.Errorf("superseded fact is not visibly demoted (no event-superseded class)")
	}
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt, "superseded-demotion", true, "", "")
}

func thenPageShowsTagChips(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/ticket/" + accTicket + "/" + accAtt))
	if err != nil {
		return err
	}
	if !strings.Contains(html, `data-testid="tag-chip"`) || !strings.Contains(html, "auth") || !strings.Contains(html, "billing") {
		return fmt.Errorf("attempt page does not render tags as chips")
	}
	return nil
}
