package acceptance

// drvweb-031 — four small web UI fixes, each proven in a live browser. The shared
// d31probe drives a page through webshot.CaptureProbe: an awaited in-page script
// performs the interaction (fill → click → await htmx:afterRequest) or measures the
// layout, and reports what the browser actually holds. The run's screenshot is the
// scenario's artefact. A probe writes through the page, so unlike capture it is never
// retried (a retry could post twice) and has no HTML fallback: no browser → the step
// fails, as an acceptance precondition should.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/webshot"
)

const (
	d31Marker     = "drvweb031-marker"
	d31CardMark   = "D31-MARK"
	d31CardPlay   = "D31-PLAY"
	d31CardStop   = "D31-RESTART"
	d31CardDone   = "D31-DONE"
	d31TabsTicket = "D31-TABS"
	d31LongTitle  = "A deliberately long ticket title that wraps onto several lines so the card's width below the corner buttons is plain to see"
)

func init() {
	for text, fn := range map[string]StepFunc{
		"a Running attempt page for drvweb-031":                                                                                     givenD31Running,
		"a Review attempt page for drvweb-031":                                                                                      givenD31Review,
		"a Running attempt page for drvweb-031 with an open escalation":                                                             givenD31Escalated,
		"I post a note, a gotcha and a tagged decision with 📬 unticked from the drvweb-031 compose box":                             whenD31ComposeThree,
		"after each post the drvweb-031 compose body and tags are empty while the chosen type and the unticked 📬 stay":              thenD31ComposeCleared,
		"the three drvweb-031 entries are on the attempt's log":                                                                     thenD31ThreeLogged,
		"I type a reason and press the drvweb-031 Review Decision button":                                                           whenD31ReviewDecision,
		"I type a reason and press the drvweb-031 Review Done button, accepting its confirm":                                        whenD31ReviewDone,
		"the drvweb-031 compose body is empty after the Review action":                                                              thenD31ReviewCleared,
		"the drvweb-031 decision reason is on the log and the attempt is Running again":                                             thenD31DecisionLogged,
		"the drvweb-031 done reason is on the log and the attempt is Done":                                                          thenD31DoneLogged,
		"I type an answer and press the drvweb-031 Resolve button":                                                                  whenD31Resolve,
		"the drvweb-031 escalation is resolved with my answer and no answer box still holds the text":                               thenD31Resolved,
		"I post a drvweb-031 note that supersedes a seq that does not exist":                                                        whenD31FailedPost,
		"the drvweb-031 compose body still holds the rejected note and nothing was logged":                                          thenD31FailedKept,
		"I press Sync on the drvweb-031 Git controls tab without accepting any dialog":                                              whenD31Sync,
		"the drvweb-031 Sync ran and reported success, and Push still confirms":                                                     thenD31SyncOK,
		"the drvweb-031 Sync reported its dirty-checkout failure banner":                                                            thenD31SyncFail,
		"a drvweb-031 board with cards carrying every corner-button combination":                                                    givenD31CardBoard,
		"I measure the drvweb-031 cards at desktop width, at rest and while hovered, and at phone width":                            whenD31MeasureCards,
		"on every drvweb-031 card the title spans the card's full width, no button overlaps any text, and hovering reflows nothing": thenD31CardsOK,
		"a drvweb-031 Review attempt with promotions and a captured BDD run":                                                        givenD31TabsBoard,
		"I open each drvweb-031 attempt tab in the browser":                                                                         whenD31OpenTabs,
		"no drvweb-031 tab panel starts with a heading repeating its tab label, and each panel is labelled by its tab":              thenD31NoDupHeadings,
	} {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registration: " + text)
		}
		registry[text] = fn
	}
}

// webshotCaptureProbe is d31probe's seam onto internal/webshot.
var webshotCaptureProbe = webshot.CaptureProbe

// d31probe runs one browser pass over url with opts (opts.Probe set), attaches the
// screenshot as name, and decodes the probe's JSON result into out.
func d31probe(sr *StepRun, url, name string, opts webshot.Options, out any) error {
	if !webshotAvailable() {
		return errors.New("drvweb-031 browser probe needs node + playwright + chromium (the acceptance rung's bdd environment)")
	}
	if opts.Width == 0 {
		opts.Width, opts.Height = 1280, 900
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	png, probe, err := webshotCaptureProbe(ctx, url, opts)
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png", Name: name, Data: base64.StdEncoding.EncodeToString(png),
	})
	if err := json.Unmarshal([]byte(probe), out); err != nil {
		return fmt.Errorf("decode probe result %q: %w", probe, err)
	}
	return nil
}

// d31Await is the in-page helper every interaction probe starts with: click el and
// resolve with event.detail.successful once THAT element's htmx request completes
// (the 3s log poll's own afterRequest is ignored; null if it never completes). The element's own hx-on handler
// has already run by then — it fires on the element before the event bubbles to body.
const d31Await = `
const d31click = (el) => new Promise((resolve) => {
  // Listen on the requesting element itself as well as body: a form inside the swapped
  // log region (an escalation's resolve form) is detached by its own swap, so its
  // afterRequest never bubbles to body. A 20s guard turns a hang into a clear failure.
  const src = el.form || el;
  let settled = false;
  const finish = (v) => { if (!settled) { settled = true; setTimeout(() => resolve(v), 50); } };
  const h = (e) => { if (e.detail.elt === el || e.detail.elt === src) finish(e.detail.successful); };
  src.addEventListener('htmx:afterRequest', h);
  document.body.addEventListener('htmx:afterRequest', h);
  setTimeout(() => finish(null), 20000);
  el.click();
});
const d31compose = () => {
  const f = document.querySelector('[data-testid="log-compose"]');
  return {
    type: f.elements.type.value, body: f.elements.body.value, tags: f.elements.tags.value,
    supersedes: f.elements.supersedes.value,
    toAgent: f.querySelector('[data-testid="compose-to-agent"]').checked,
  };
};
`

func d31AttemptURL(w *World) (string, error) {
	b, err := currentBoard(w)
	if err != nil {
		return "", err
	}
	return b.url("/ticket/" + tabTicket + "/" + tabAtt), nil
}

func d31Events(w *World) ([]event.Event, error) {
	b, err := currentBoard(w)
	if err != nil {
		return nil, err
	}
	return ticketlog.Read(b.root, tabTicket, tabAtt)
}

// ---- Rule 1: the compose / answer boxes ----

func givenD31Running(w *World, sr *StepRun) error { return setBoard(w, "", false) }
func givenD31Review(w *World, sr *StepRun) error  { return setBoard(w, "review", false) }

func givenD31Escalated(w *World, sr *StepRun) error {
	if err := setBoard(w, "", false); err != nil {
		return err
	}
	b, _ := currentBoard(w)
	e, err := ticketlog.Append(b.root, tabTicket, tabAtt, event.Event{Type: "escalation", Actor: "agent:acceptance", Body: "Which base branch should this land on?"})
	if err != nil {
		return err
	}
	w.set("d31-esc", e.Seq)
	return nil
}

type d31ComposeStep struct {
	Type  string `json:"type"`
	OK    bool   `json:"ok"`
	After struct {
		Type       string `json:"type"`
		Body       string `json:"body"`
		Tags       string `json:"tags"`
		Supersedes string `json:"supersedes"`
		ToAgent    bool   `json:"toAgent"`
	} `json:"after"`
	LogHas bool `json:"logHas"`
}

func whenD31ComposeThree(w *World, sr *StepRun) error {
	url, err := d31AttemptURL(w)
	if err != nil {
		return err
	}
	probe := `(async () => {` + d31Await + `
  const f = document.querySelector('[data-testid="log-compose"]');
  f.querySelector('[data-testid="compose-to-agent"]').checked = false;
  const out = [];
  for (const type of ['note', 'gotcha', 'decision']) {
    const body = '` + d31Marker + `-' + type;
    f.elements.type.value = type;
    f.elements.body.value = body;
    f.elements.tags.value = 'polish ui';
    const ok = await d31click(f.querySelector('[data-testid="compose-submit"]'));
    out.push({ type, ok, after: d31compose(),
      logHas: document.querySelector('[data-testid="log-region"]').textContent.includes(body) });
  }
  return out;
})()`
	var steps []d31ComposeStep
	if err := d31probe(sr, url, "compose-cleared-after-three-posts", webshot.Options{FullPage: true, Probe: probe}, &steps); err != nil {
		return err
	}
	w.set("d31-compose", steps)
	return nil
}

func thenD31ComposeCleared(w *World, sr *StepRun) error {
	steps, _ := w.get("d31-compose").([]d31ComposeStep)
	if len(steps) != 3 {
		return fmt.Errorf("want 3 compose posts, probe reported %d: %+v", len(steps), steps)
	}
	var report strings.Builder
	for _, s := range steps {
		fmt.Fprintf(&report, "%-8s ok=%v body=%q tags=%q supersedes=%q type-after=%q 📬=%v in-log=%v\n",
			s.Type, s.OK, s.After.Body, s.After.Tags, s.After.Supersedes, s.After.Type, s.After.ToAgent, s.LogHas)
		switch {
		case !s.OK:
			return fmt.Errorf("the %s post did not succeed", s.Type)
		case s.After.Body != "" || s.After.Tags != "" || s.After.Supersedes != "":
			return fmt.Errorf("after a successful %s the compose text is not empty: %+v", s.Type, s.After)
		case s.After.Type != s.Type:
			return fmt.Errorf("after a %s the type <select> was reset to %q", s.Type, s.After.Type)
		case s.After.ToAgent:
			return fmt.Errorf("after a %s the unticked 📬 checkbox was re-ticked", s.Type)
		case !s.LogHas:
			return fmt.Errorf("the %s entry did not appear in the log region", s.Type)
		}
	}
	attachText(sr, "compose-state-after-each-post", report.String())
	return nil
}

func thenD31ThreeLogged(w *World, sr *StepRun) error {
	evs, err := d31Events(w)
	if err != nil {
		return err
	}
	seen := map[string]event.Event{}
	for _, e := range evs {
		if strings.HasPrefix(e.Body, d31Marker+"-") {
			seen[e.Type] = e
		}
	}
	for _, typ := range []string{"note", "gotcha", "decision"} {
		e, ok := seen[typ]
		if !ok {
			return fmt.Errorf("no %s entry with the marker on the log", typ)
		}
		if e.To != "" {
			return fmt.Errorf("the %s entry was marked to %q although 📬 was unticked", typ, e.To)
		}
		if strings.Join(e.Tags, " ") != "polish ui" {
			return fmt.Errorf("the %s entry carries tags %v, want [polish ui]", typ, e.Tags)
		}
	}
	attachText(sr, "logged-entries", fmt.Sprintf("%+v", seen))
	return nil
}

type d31ReviewResult struct {
	OK      bool   `json:"ok"`
	Body    string `json:"body"`
	Clicked bool   `json:"clicked"`
}

func d31ReviewAction(w *World, sr *StepRun, testid, reason string, accept bool) error {
	url, err := d31AttemptURL(w)
	if err != nil {
		return err
	}
	probe := `(async () => {` + d31Await + `
  const btn = document.querySelector('[data-testid="` + testid + `"]');
  if (!btn) return { clicked: false };
  document.querySelector('[data-testid="compose-body"]').value = ` + strconv.Quote(reason) + `;
  const ok = await d31click(btn);
  return { clicked: true, ok, body: d31compose().body };
})()`
	var res d31ReviewResult
	if err := d31probe(sr, url, testid+"-compose-cleared", webshot.Options{FullPage: true, Probe: probe, AcceptDialogs: accept}, &res); err != nil {
		return err
	}
	w.set("d31-review", res)
	w.set("d31-reason", reason)
	return nil
}

func whenD31ReviewDecision(w *World, sr *StepRun) error {
	return d31ReviewAction(w, sr, "action-decision", d31Marker+" reopen: the docs need another pass", false)
}

func whenD31ReviewDone(w *World, sr *StepRun) error {
	return d31ReviewAction(w, sr, "action-done", d31Marker+" done: looks good, shipping", true)
}

func thenD31ReviewCleared(w *World, sr *StepRun) error {
	res, _ := w.get("d31-review").(d31ReviewResult)
	if !res.Clicked {
		return errors.New("the Review action button is not on the page")
	}
	if !res.OK {
		return errors.New("the Review action post did not succeed")
	}
	if res.Body != "" {
		return fmt.Errorf("after the Review action the compose body still holds %q", res.Body)
	}
	attachText(sr, "compose-after-review-action", fmt.Sprintf("%+v", res))
	return nil
}

func d31ReasonLogged(w *World, sr *StepRun, typ, wantState string) error {
	evs, err := d31Events(w)
	if err != nil {
		return err
	}
	reason, _ := w.get("d31-reason").(string)
	for _, e := range evs {
		if e.Type == typ && e.Body == reason {
			b, _ := currentBoard(w)
			a, err := project.LoadAttempt(b.root, tabTicket, tabAtt)
			if err != nil {
				return err
			}
			if string(a.State) != wantState {
				return fmt.Errorf("after the %s the attempt is %s, want %s", typ, a.State, wantState)
			}
			attachText(sr, typ+"-logged", fmt.Sprintf("#%d %s: %s (attempt now %s)", e.Seq, e.Type, e.Body, a.State))
			return nil
		}
	}
	return fmt.Errorf("no %s event with body %q on the log", typ, reason)
}

func thenD31DecisionLogged(w *World, sr *StepRun) error {
	return d31ReasonLogged(w, sr, "decision", "Running")
}

func thenD31DoneLogged(w *World, sr *StepRun) error { return d31ReasonLogged(w, sr, "done", "Done") }

type d31ResolveResult struct {
	OK        bool `json:"ok"`
	Found     bool `json:"found"`
	Resolved  bool `json:"resolved"`
	TextLeft  bool `json:"textLeft"`
	InputGone bool `json:"inputGone"`
}

func whenD31Resolve(w *World, sr *StepRun) error {
	url, err := d31AttemptURL(w)
	if err != nil {
		return err
	}
	seq, _ := w.get("d31-esc").(int)
	n := strconv.Itoa(seq)
	answer := d31Marker + " land it on main"
	w.set("d31-answer", answer)
	probe := `(async () => {` + d31Await + `
  const input = document.querySelector('[data-testid="resolve-input-` + n + `"]');
  if (!input) return { found: false };
  input.value = ` + strconv.Quote(answer) + `;
  const ok = await d31click(document.querySelector('[data-testid="resolve-submit-` + n + `"]'));
  const left = Array.from(document.querySelectorAll('textarea, input[type=text]')).some((t) => t.value.includes(` + strconv.Quote(answer) + `));
  return { found: true, ok, textLeft: left,
    inputGone: !document.querySelector('[data-testid="resolve-input-` + n + `"]'),
    resolved: !!document.querySelector('[data-testid="resolved-` + n + `"]') };
})()`
	var res d31ResolveResult
	if err := d31probe(sr, url, "escalation-answered", webshot.Options{FullPage: true, Probe: probe}, &res); err != nil {
		return err
	}
	w.set("d31-resolve", res)
	return nil
}

func thenD31Resolved(w *World, sr *StepRun) error {
	res, _ := w.get("d31-resolve").(d31ResolveResult)
	switch {
	case !res.Found:
		return errors.New("the escalation's answer box is not on the page")
	case !res.OK:
		return errors.New("the resolve post did not succeed")
	case !res.Resolved:
		return errors.New("the escalation does not show as resolved after answering")
	case res.TextLeft:
		return errors.New("an input on the page still holds the submitted answer")
	}
	evs, err := d31Events(w)
	if err != nil {
		return err
	}
	answer, _ := w.get("d31-answer").(string)
	for _, e := range evs {
		if e.Type == "resolution" && e.Body == answer {
			attachText(sr, "resolution-logged", fmt.Sprintf("#%d resolution: %s (answer box gone: %v)", e.Seq, e.Body, res.InputGone))
			return nil
		}
	}
	return fmt.Errorf("no resolution event with body %q on the log", answer)
}

func whenD31FailedPost(w *World, sr *StepRun) error {
	url, err := d31AttemptURL(w)
	if err != nil {
		return err
	}
	probe := `(async () => {` + d31Await + `
  const f = document.querySelector('[data-testid="log-compose"]');
  f.elements.body.value = '` + d31Marker + ` rejected note';
  f.elements.supersedes.value = '999';
  const ok = await d31click(f.querySelector('[data-testid="compose-submit"]'));
  return { ok, after: d31compose() };
})()`
	var res d31ComposeStep
	if err := d31probe(sr, url, "failed-post-keeps-text", webshot.Options{FullPage: true, Probe: probe}, &res); err != nil {
		return err
	}
	w.set("d31-failed", res)
	return nil
}

func thenD31FailedKept(w *World, sr *StepRun) error {
	res, _ := w.get("d31-failed").(d31ComposeStep)
	if res.OK {
		return errors.New("a supersedes of a non-existent seq was accepted; the failure path was not exercised")
	}
	if res.After.Body != d31Marker+" rejected note" || res.After.Supersedes != "999" {
		return fmt.Errorf("a failed post lost the typed text: %+v", res.After)
	}
	evs, err := d31Events(w)
	if err != nil {
		return err
	}
	for _, e := range evs {
		if strings.Contains(e.Body, d31Marker) {
			return fmt.Errorf("the rejected note reached the log as #%d", e.Seq)
		}
	}
	attachText(sr, "compose-after-failed-post", fmt.Sprintf("%+v", res.After))
	return nil
}

// ---- Rule 2: Sync without a confirm ----

type d31SyncResult struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// whenD31Sync clicks Sync WITHOUT AcceptDialogs: Playwright dismisses any dialog by
// default, so a surviving hx-confirm would cancel the post and no banner would land.
func whenD31Sync(w *World, sr *StepRun) error {
	b, err := currentGitBoard(w)
	if err != nil {
		return err
	}
	var res d31SyncResult
	if err := d31probe(sr, b.url("/ticket/"+gitTicket+"/"+gitAtt), "sync-no-confirm-"+b.variant, webshot.Options{
		Width: 1280, Height: 1000, FullPage: true,
		ClickSelector:          `[data-testid="attempt-tab-git"]`,
		ClickSelector2:         `[data-testid="action-git-sync"]`,
		AfterClickWaitSelector: `#git-result[data-kind]`,
		Probe: `(() => { const r = document.getElementById('git-result');
  return { kind: (r && r.dataset.kind) || '', text: r ? r.textContent.trim() : '' }; })()`,
	}, &res); err != nil {
		return err
	}
	w.set("d31-sync", res)
	return nil
}

func thenD31SyncOK(w *World, sr *StepRun) error {
	res, _ := w.get("d31-sync").(d31SyncResult)
	if res.Kind != "ok" {
		return fmt.Errorf("Sync with every dialog dismissed did not report success (kind %q, text %q) — is it still behind a confirm?", res.Kind, res.Text)
	}
	b, err := currentGitBoard(w)
	if err != nil {
		return err
	}
	html, err := getBody(b.url("/ticket/" + gitTicket + "/" + gitAtt))
	if err != nil {
		return err
	}
	sync, ok := openingTag(html, `data-testid="action-git-sync"`)
	if !ok || strings.Contains(sync, "hx-confirm") {
		return fmt.Errorf("the Sync button still carries an hx-confirm (or is missing): %s", sync)
	}
	push, ok := openingTag(html, `data-testid="action-git-push"`)
	if !ok || !strings.Contains(push, "hx-confirm") {
		return fmt.Errorf("the Push button lost its hx-confirm: %s", push)
	}
	attachText(sr, "sync-result", res.Kind+": "+res.Text+"\n\nSync tag: "+sync+"\nPush tag: "+push)
	return nil
}

func thenD31SyncFail(w *World, sr *StepRun) error {
	res, _ := w.get("d31-sync").(d31SyncResult)
	if res.Kind != "fail" || !strings.HasPrefix(res.Text, "Sync failed") {
		return fmt.Errorf("a dirty-checkout Sync should show the failure banner, got kind %q text %q", res.Kind, res.Text)
	}
	attachText(sr, "sync-refused", res.Text)
	return nil
}

// ---- Rule 3: card text vs the corner buttons ----

// givenD31CardBoard seeds one card per corner-button combination, each with a long
// title: star+mark-done+archive (all-green Review), star+play+archive (Running, not
// enabled), star+restart-context+archive (context auto-stop), star+archive (Done).
func givenD31CardBoard(w *World, sr *StepRun) error {
	bb, err := seedBCBoard(w, d31CardMark, bcPyramidYAML("dev"))
	if err != nil {
		return err
	}
	root := bb.root
	if _, err := ticketlog.Append(root, d31CardMark, bcAtt, event.Event{
		Type: "promotion-result", Actor: "agent:acceptance", Environment: "dev", Commit: bb.head, Outcome: "pass", Body: "verified",
	}); err != nil {
		return err
	}
	for _, t := range []string{d31CardPlay, d31CardStop, d31CardDone} {
		if err := addTypedTicket(root, bb.wd, t, ""); err != nil {
			return err
		}
	}
	if err := cbAppend(root, d31CardStop, event.Event{Type: "escalation", Body: "Context-window auto-stop.", Tags: []string{limit.TagContextAutoStop}}); err != nil {
		return err
	}
	if err := cbAppend(root, d31CardDone, event.Event{Type: "done", Body: "closed"}); err != nil {
		return err
	}
	for _, t := range []string{d31CardMark, d31CardPlay, d31CardStop, d31CardDone} {
		spec := "---\nid: " + t + "\ntitle: " + d31LongTitle + "\n---\n\n# " + t + "\n"
		if err := os.WriteFile(root.SpecPath(t), []byte(spec), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type d31Rect struct{ L, T, R, B float64 }

func (a d31Rect) overlaps(b d31Rect) bool {
	const eps = 0.5
	return a.L < b.R-eps && b.L < a.R-eps && a.T < b.B-eps && b.T < a.B-eps
}

type d31Card struct {
	ID           string    `json:"id"`
	PadRight     float64   `json:"padRight"`
	ContentRight float64   `json:"contentRight"`
	Title        d31Rect   `json:"title"`
	TitleLines   int       `json:"titleLines"`
	Buttons      []d31Rect `json:"buttons"`
	Texts        []d31Rect `json:"texts"`
}

const d31CardProbe = `(() => {
  const box = (r) => ({ L: r.left, T: r.top, R: r.right, B: r.bottom });
  const lines = (el) => { const rg = document.createRange(); rg.selectNodeContents(el);
    return Array.from(rg.getClientRects()).filter((r) => r.width > 0 && r.height > 0).map(box); };
  return Array.from(document.querySelectorAll('.card-wrap')).map((wrap) => {
    const card = wrap.querySelector('a.card'); const cs = getComputedStyle(card);
    const title = card.querySelector('.title');
    const texts = [];
    card.querySelectorAll('.tid, .title, .meta-row, .waiting, .coordinator').forEach((el) => texts.push(...lines(el)));
    return { id: wrap.dataset.testid, padRight: parseFloat(cs.paddingRight),
      contentRight: card.getBoundingClientRect().right - parseFloat(cs.borderRightWidth) - parseFloat(cs.paddingRight),
      title: box(title.getBoundingClientRect()), titleLines: new Set(lines(title).map((r) => Math.round(r.T))).size,
      buttons: Array.from(wrap.querySelectorAll('.card-actions button')).map((b) => box(b.getBoundingClientRect())),
      texts };
  });
})()`

func whenD31MeasureCards(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	hover := `[data-testid="card-` + d31CardPlay + `-0001"] a.card`
	for _, pass := range []struct {
		key   string
		w, h  int
		hover string
	}{
		{"desktop", 1280, 900, ""},
		{"desktop-hover", 1280, 900, hover},
		{"phone", 390, 844, ""},
	} {
		var cards []d31Card
		if err := d31probe(sr, b.url("/"), "cards-"+pass.key, webshot.Options{
			Width: pass.w, Height: pass.h, FullPage: true, HoverSelector: pass.hover, Probe: d31CardProbe,
		}, &cards); err != nil {
			return err
		}
		w.set("d31-cards-"+pass.key, cards)
	}
	return nil
}

func thenD31CardsOK(w *World, sr *StepRun) error {
	want := map[string]int{d31CardMark: 3, d31CardPlay: 3, d31CardStop: 3, d31CardDone: 2}
	var report strings.Builder
	byPass := map[string]map[string]d31Card{}
	for _, pass := range []string{"desktop", "desktop-hover", "phone"} {
		cards, _ := w.get("d31-cards-" + pass).([]d31Card)
		byPass[pass] = map[string]d31Card{}
		for _, c := range cards {
			byPass[pass][c.ID] = c
		}
		for t, nButtons := range want {
			c, ok := byPass[pass]["card-"+t+"-0001"]
			if !ok {
				return fmt.Errorf("[%s] no card for %s on the board", pass, t)
			}
			if len(c.Buttons) != nButtons {
				return fmt.Errorf("[%s] %s has %d corner buttons, want %d", pass, t, len(c.Buttons), nButtons)
			}
			if math.Abs(c.Title.R-c.ContentRight) > 1 {
				return fmt.Errorf("[%s] %s title ends at %.1f but the card's content box ends at %.1f — the title is not using the full width", pass, t, c.Title.R, c.ContentRight)
			}
			if c.PadRight > 12.5 {
				return fmt.Errorf("[%s] %s card still reserves %.0fpx on its right for the buttons", pass, t, c.PadRight)
			}
			for _, btn := range c.Buttons {
				for _, txt := range c.Texts {
					if btn.overlaps(txt) {
						return fmt.Errorf("[%s] on %s a corner button %+v overlaps text %+v", pass, t, btn, txt)
					}
				}
			}
			fmt.Fprintf(&report, "[%-13s] %-12s buttons=%d pad-right=%.0fpx title-right=%.1f content-right=%.1f title-lines=%d\n",
				pass, t, len(c.Buttons), c.PadRight, c.Title.R, c.ContentRight, c.TitleLines)
		}
	}
	// Hovering the play card must not move any of its text.
	rest, hov := byPass["desktop"]["card-"+d31CardPlay+"-0001"], byPass["desktop-hover"]["card-"+d31CardPlay+"-0001"]
	if rest.Title != hov.Title || fmt.Sprint(rest.Texts) != fmt.Sprint(hov.Texts) {
		return fmt.Errorf("hovering %s reflowed its text: rest %+v vs hover %+v", d31CardPlay, rest.Texts, hov.Texts)
	}
	report.WriteString("hover: " + d31CardPlay + " text boxes identical at rest and hovered\n")
	attachText(sr, "card-geometry", report.String())
	return nil
}

// ---- Rule 4: no duplicate tab-panel headings ----

func givenD31TabsBoard(w *World, sr *StepRun) error {
	pyramid := bcPyramidYAML("dev") // promotions → the Promotions tab
	// A review-ready env (up + down + healthcheck) → the Review environment tab.
	pyramid = strings.Replace(pyramid, "environments:\n", "environments:\n  - name: review\n    up: \"true\"\n    down: \"true\"\n    healthchecks:\n      - name: ok\n        script: \"true\"\n", 1)
	bb, err := seedBCBoard(w, d31TabsTicket, pyramid)
	if err != nil {
		return err
	}
	return d31SeedRun(bb.root, d31TabsTicket, bcAtt) // → the BDD report tab
}

func d31SeedRun(root store.Root, ticket, att string) error {
	dir := filepath.Join(root.ArtefactsDir(ticket, att), report.BDDArtefactSubdir, accRung, accEnv, accCommit, "20260101T100000Z")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cucumber.json"), []byte(seedCucumberJSON), 0o644)
}

type d31Tab struct {
	Label      string   `json:"label"`
	TabID      string   `json:"tabId"`
	Panel      string   `json:"panel"`
	Labelledby string   `json:"labelledby"`
	Visible    bool     `json:"visible"`
	Heads      []string `json:"heads"`
}

func whenD31OpenTabs(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	// Click each tab, read its panel's headings, then return to Spec for the shot. The
	// rendered spec prose's own markdown headings are content, not panel chrome, so
	// they are excluded.
	probe := `(() => {
  const tabs = Array.from(document.querySelectorAll('[role="tab"]'));
  const out = tabs.map((t) => {
    t.click();
    const p = document.getElementById(t.getAttribute('aria-controls'));
    if (!p) return { label: t.textContent.trim(), tabId: t.id, panel: '' };
    const heads = Array.from(p.querySelectorAll('h1, h2, h3, h4'))
      .filter((h) => !h.closest('.prose'))
      .map((h) => h.textContent.trim());
    return { label: t.textContent.trim(), tabId: t.id, panel: p.id,
      labelledby: p.getAttribute('aria-labelledby') || '', visible: !p.hidden, heads };
  });
  if (tabs.length) tabs[0].click();
  return out;
})()`
	var tabs []d31Tab
	if err := d31probe(sr, b.url("/ticket/"+d31TabsTicket+"/"+bcAtt), "spec-tab-no-duplicate-heading", webshot.Options{FullPage: true, Probe: probe}, &tabs); err != nil {
		return err
	}
	w.set("d31-tabs", tabs)
	return nil
}

func thenD31NoDupHeadings(w *World, sr *StepRun) error {
	tabs, _ := w.get("d31-tabs").([]d31Tab)
	labels := map[string]bool{}
	var report strings.Builder
	for _, t := range tabs {
		labels[t.Label] = true
		if t.Panel == "" {
			return fmt.Errorf("tab %q controls no panel", t.Label)
		}
		if t.Labelledby != t.TabID {
			return fmt.Errorf("panel %s is labelled by %q, want its tab %q", t.Panel, t.Labelledby, t.TabID)
		}
		if !t.Visible {
			return fmt.Errorf("clicking tab %q did not reveal panel %s", t.Label, t.Panel)
		}
		label := strings.ToLower(t.Label)
		for _, h := range t.Heads {
			hl := strings.ToLower(h)
			if hl == label || strings.HasPrefix(label, hl) || strings.HasPrefix(hl, label) {
				return fmt.Errorf("panel %s repeats its tab label %q as heading %q", t.Panel, t.Label, h)
			}
		}
		fmt.Fprintf(&report, "%-22s panel=%-30s labelledby=%-26s headings=%q\n", t.Label, t.Panel, t.Labelledby, t.Heads)
	}
	for _, want := range []string{"Spec", "Git controls", "Review environment", "Promotions", "BDD report"} {
		if !labels[want] {
			return fmt.Errorf("the fixture did not render the %q tab, so its panel went unchecked (tabs: %v)", want, labels)
		}
	}
	attachText(sr, "tab-panel-headings", report.String())
	return nil
}
