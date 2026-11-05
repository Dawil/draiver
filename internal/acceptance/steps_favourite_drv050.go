package acceptance

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/webshot"
)

// steps_favourite_drv050.go realises drv-050's acceptance criteria
// (features/favourite-cards/favourite.feature): a per-viewer star on every board card
// that floats it to the top of its column, stored only in the viewing user's cookie.
// Every scenario stands up a real data root with three Running attempts and a LIVE
// webui, drives the real POST /star toggle end to end through a cookie-jar client (a
// browser's cookie behaviour), and asserts the re-rendered ordering and the cookie —
// the server persists nothing. The steps register into the shared registry from init().

const (
	favCardA  = "FAV-A" // alphabetically first Running card
	favCardB  = "FAV-B" // middle
	favCardC  = "FAV-C" // last
	favAtt    = "0001"
	favCookie = "draiver_faves" // must match web.favesCookie (unexported there)
)

func init() {
	drv050 := map[string]StepFunc{
		"a board with three Running cards in alphabetical order":                              givenFaveBoard,
		"every card shows a star button in its action cluster beside the archive X":           thenFaveStarsPresent,
		"the star is hidden at rest and revealed on card hover or focus":                      thenFaveHoverReveal,
		"a favourited card's star stays visible without hovering":                             thenFaveOnStaysVisible,
		"I click the star on the last card":                                                   whenFaveClickLast,
		"that card moves to the top of its column":                                            thenFaveLastAtTop,
		"the favourites cookie now contains that card's ref":                                  thenFaveCookieHasLast,
		"I click the star on that card again":                                                 whenFaveClickLastAgain,
		"the card returns to its alphabetical position":                                       thenFaveLastAlphabetical,
		"the favourites cookie no longer contains that card's ref":                            thenFaveCookieLacksLast,
		"I favourite the last card and then the middle card":                                  whenFaveLastThenMiddle,
		"the two favourited cards render above the non-favourited card":                       thenFaveTwoAboveOne,
		"the two favourited cards are ordered alphabetically among themselves":                thenFaveTwoAlpha,
		"I favourite the last card":                                                           whenFaveLast,
		"a board render with no cookie shows the plain alphabetical order":                    thenFavePlainNoCookie,
		"the same cookie drives the identical favourites-first order across repeated renders": thenFaveIdenticalRepeated,
		"no durable favourite event was written to any attempt":                               thenFaveNoDurable,
		"I open the board with a malformed favourites cookie":                                 whenFaveMalformedCookie,
		"the board renders successfully in the plain alphabetical order":                      thenFaveMalformedOK,
	}
	for text, fn := range drv050 {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registered: " + text)
		}
		registry[text] = fn
	}
}

// faveBoard is a live board over three Running attempts plus the cookie-jar client a
// scenario drives the star toggle through (the jar is the browser's cookie memory).
type faveBoard struct {
	*board
	client *http.Client
}

func currentFaveBoard(w *World) (*faveBoard, error) {
	b, _ := w.get("faveboard").(*faveBoard)
	if b == nil {
		return nil, errors.New("no favourite board set up in scope")
	}
	return b, nil
}

func seedFaveBoard(w *World) (*faveBoard, error) {
	dataDir, err := os.MkdirTemp("", "fav-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	for _, id := range []string{favCardA, favCardB, favCardC} {
		if err := root.EnsureAttemptDirs(id, favAtt); err != nil {
			return nil, err
		}
		spec := "---\nid: " + id + "\ntitle: " + id + " running\n---\n\n# " + id + "\n"
		if err := os.WriteFile(root.SpecPath(id), []byte(spec), 0o644); err != nil {
			return nil, err
		}
		meta := "---\nid: " + favAtt + "\nticket: " + id + "\nbase: main\n---\n\n"
		if err := os.WriteFile(root.AttemptMetaPath(id, favAtt), []byte(meta), 0o644); err != nil {
			return nil, err
		}
		if _, err := ticketlog.Append(root, id, favAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
			return nil, err
		}
	}
	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	fb := &faveBoard{
		board:  &board{root: root, wd: dataDir, srv: ts},
		client: &http.Client{Jar: jar},
	}
	w.set("board", fb.board) // shoot() resolves the live server via the shared key
	w.set("faveboard", fb)
	return fb, nil
}

func givenFaveBoard(w *World, sr *StepRun) error {
	_, err := seedFaveBoard(w)
	return err
}

// --- HTTP helpers (the cookie-jar client is the browser's cookie memory) ---

// faveStar POSTs the star toggle for a card through the jar client, so the Set-Cookie
// the handler writes is remembered and resent on the next render — exactly as a browser
// does. It returns the re-rendered board region body.
func faveStar(b *faveBoard, ticket string) (string, error) {
	resp, err := b.client.Post(b.url("/ticket/"+ticket+"/"+favAtt+"/star"), "application/x-www-form-urlencoded", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("POST star %s: status %d\n%s", ticket, resp.StatusCode, raw)
	}
	return string(raw), nil
}

// faveBoardBody GETs /board through the jar client (so the favourites cookie rides along).
func faveBoardBody(b *faveBoard) (string, error) {
	resp, err := b.client.Get(b.url("/board"))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// faveCookieValue returns the current favourites cookie value the jar holds for the
// server, or "" if none is set.
func faveCookieValue(b *faveBoard) string {
	u, _ := url.Parse(b.srv.URL)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == favCookie {
			return c.Value
		}
	}
	return ""
}

func cardIndex(body, ticket string) int {
	return strings.Index(body, "card-"+ticket+"-"+favAtt)
}

// --- scenario 1: the control renders ---

func thenFaveStarsPresent(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	body, err := faveBoardBody(b)
	if err != nil {
		return err
	}
	for _, id := range []string{favCardA, favCardB, favCardC} {
		star := `data-testid="star-btn-` + id + `-` + favAtt + `"`
		archive := `data-testid="archive-btn-` + id + `-` + favAtt + `"`
		if !strings.Contains(body, star) {
			return fmt.Errorf("card %s missing its star button (%s)\n%s", id, star, body)
		}
		if !strings.Contains(body, archive) {
			return fmt.Errorf("card %s should still carry the archive X beside the star", id)
		}
		// The star sits in the same .card-actions cluster as the archive X: both testids
		// must appear and the star must precede the archive in the action row.
		if cardStarBeforeArchive := strings.Index(body, star) < strings.Index(body, archive); !cardStarBeforeArchive {
			return fmt.Errorf("the star should sit in .card-actions beside the archive X on %s", id)
		}
	}
	return shoot(w, sr, "/", "favourite-stars-present", true, "", "")
}

func thenFaveHoverReveal(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	css, err := promGetBody(b.url("/static/style.css"))
	if err != nil {
		return err
	}
	for _, want := range []string{
		".star-form { margin: 0; opacity: 0;",
		".card-wrap:hover .star-form",
		".card-wrap:focus-within .star-form",
	} {
		if !strings.Contains(css, want) {
			return fmt.Errorf("style.css missing the star hover-reveal rule %q", want)
		}
	}
	return nil
}

func thenFaveOnStaysVisible(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	css, err := promGetBody(b.url("/static/style.css"))
	if err != nil {
		return err
	}
	// A favourited card forces the star form visible regardless of hover.
	if !strings.Contains(css, ".star-form.star-on { opacity: 1; }") {
		return fmt.Errorf("style.css must force a favourited card's star visible (.star-form.star-on { opacity: 1; })")
	}
	// And the template marks a favourited card's form with star-on + the button on-class,
	// so the rule has something to bind to. Drive a real favourite to prove it renders.
	body, err := faveStar(b, favCardC)
	if err != nil {
		return err
	}
	if !strings.Contains(body, "star-form star-on") {
		return fmt.Errorf("a favourited card's form should carry the star-on class\n%s", body)
	}
	if !strings.Contains(body, `data-testid="star-btn-`+favCardC+`-`+favAtt+`" data-favourited="true"`) {
		return fmt.Errorf("the favourited card's star button should report data-favourited=\"true\"")
	}
	// Reset so this scenario leaves no favourite behind for the next.
	_, _ = faveStar(b, favCardC)
	return nil
}

// --- scenario 2: click toggles + floats + cookie ---

func whenFaveClickLast(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	body, err := faveStar(b, favCardC)
	if err != nil {
		return err
	}
	w.set("favlastbody", body)
	// Evidence: drive the real star button in a browser and capture the card floating to
	// the top of the column — the spec's core interaction, captured end to end.
	return shootFaveClick(w, sr, "favourite-click-floats-to-top", favCardC)
}

func thenFaveLastAtTop(w *World, sr *StepRun) error {
	body, _ := w.get("favlastbody").(string)
	if body == "" {
		return errors.New("no star-toggle response captured")
	}
	// The favourited last card now sorts above A and B in the Running column.
	if cardIndex(body, favCardC) > cardIndex(body, favCardA) || cardIndex(body, favCardC) > cardIndex(body, favCardB) {
		return fmt.Errorf("favourited %s should render at the top of its column\n%s", favCardC, body)
	}
	return nil
}

func thenFaveCookieHasLast(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	val := faveCookieValue(b)
	if !strings.Contains(val, url.QueryEscape(favCardC+"/"+favAtt)) {
		return fmt.Errorf("the favourites cookie should contain %s's ref, got %q", favCardC, val)
	}
	return nil
}

func whenFaveClickLastAgain(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	body, err := faveStar(b, favCardC)
	if err != nil {
		return err
	}
	w.set("favlastbody", body)
	return nil
}

func thenFaveLastAlphabetical(w *World, sr *StepRun) error {
	body, _ := w.get("favlastbody").(string)
	if body == "" {
		return errors.New("no star-toggle response captured")
	}
	// Un-favourited, the last card is back below A and B.
	if cardIndex(body, favCardA) > cardIndex(body, favCardC) || cardIndex(body, favCardB) > cardIndex(body, favCardC) {
		return fmt.Errorf("un-favourited %s should return to its alphabetical (last) position\n%s", favCardC, body)
	}
	return nil
}

func thenFaveCookieLacksLast(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	val := faveCookieValue(b)
	if strings.Contains(val, url.QueryEscape(favCardC+"/"+favAtt)) {
		return fmt.Errorf("the favourites cookie should no longer contain %s's ref, got %q", favCardC, val)
	}
	return nil
}

// --- scenario 3: multiple favourites ---

func whenFaveLastThenMiddle(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	if _, err := faveStar(b, favCardC); err != nil {
		return err
	}
	body, err := faveStar(b, favCardB)
	if err != nil {
		return err
	}
	w.set("favmultibody", body)
	return shoot(w, sr, "/", "favourite-multiple-sorted", true, "", "")
}

func thenFaveTwoAboveOne(w *World, sr *StepRun) error {
	body, _ := w.get("favmultibody").(string)
	if body == "" {
		return errors.New("no multi-favourite response captured")
	}
	// B and C favourited; A not. Both favourites must render above A.
	if cardIndex(body, favCardB) > cardIndex(body, favCardA) || cardIndex(body, favCardC) > cardIndex(body, favCardA) {
		return fmt.Errorf("the two favourited cards (%s, %s) should render above the non-favourite %s\n%s", favCardB, favCardC, favCardA, body)
	}
	return nil
}

func thenFaveTwoAlpha(w *World, sr *StepRun) error {
	body, _ := w.get("favmultibody").(string)
	if body == "" {
		return errors.New("no multi-favourite response captured")
	}
	// Among the favourites, alphabetical: B before C.
	if cardIndex(body, favCardB) > cardIndex(body, favCardC) {
		return fmt.Errorf("favourites should be alphabetical among themselves: %s before %s\n%s", favCardB, favCardC, body)
	}
	return nil
}

// --- scenario 4: server stores nothing + stable ordering ---

func whenFaveLast(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	_, err = faveStar(b, favCardC)
	return err
}

func thenFavePlainNoCookie(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	// A render with NO cookie (a cookie-less client) must show the plain alphabetical
	// order — proving the server persisted nothing.
	body, err := promGetBody(b.url("/board"))
	if err != nil {
		return err
	}
	if cardIndex(body, favCardA) > cardIndex(body, favCardB) || cardIndex(body, favCardB) > cardIndex(body, favCardC) {
		return fmt.Errorf("a cookie-less render must be plain alphabetical (A,B,C)\n%s", body)
	}
	return nil
}

func thenFaveIdenticalRepeated(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	// The same cookie (held in the jar) drives the identical favourites-first order on
	// repeated /board renders — the 3s poll guarantee: each re-render re-derives the same
	// order from the cookie.
	var prev string
	for i := 0; i < 3; i++ {
		body, err := faveBoardBody(b)
		if err != nil {
			return err
		}
		// C is favourited → above A and B every time.
		if cardIndex(body, favCardC) > cardIndex(body, favCardA) {
			return fmt.Errorf("render %d: favourited %s should stay above %s across re-renders", i, favCardC, favCardA)
		}
		order := fmt.Sprintf("%d,%d,%d", cardIndex(body, favCardC), cardIndex(body, favCardA), cardIndex(body, favCardB))
		if i > 0 && order != prev {
			return fmt.Errorf("the order must be identical across re-renders, drifted at render %d", i)
		}
		prev = order
	}
	return nil
}

func thenFaveNoDurable(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	for _, id := range []string{favCardA, favCardB, favCardC} {
		a, err := project.LoadAttempt(b.root, id, favAtt)
		if err != nil {
			return err
		}
		for _, e := range a.Events {
			if strings.Contains(e.Type, "star") || strings.Contains(e.Type, "fav") {
				return fmt.Errorf("favourite must persist NO durable event; %s carries a %q event", id, e.Type)
			}
		}
	}
	attachText(sr, "favourite-no-server-state",
		"star toggle wrote no durable event to any attempt — favourite state lives only in the viewer's cookie")
	return nil
}

// --- scenario 5: malformed cookie never errors ---

func whenFaveMalformedCookie(w *World, sr *StepRun) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, b.url("/board"), nil)
	if err != nil {
		return err
	}
	// A stale/oversized/garbage cookie: a too-big token with disallowed characters.
	req.AddCookie(&http.Cookie{Name: favCookie, Value: strings.Repeat("!!bad ref!!,", 1000) + "a/b/c/d"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	w.set("favmalformedstatus", resp.StatusCode)
	w.set("favmalformedbody", string(raw))
	return nil
}

func thenFaveMalformedOK(w *World, sr *StepRun) error {
	status, _ := w.get("favmalformedstatus").(int)
	body, _ := w.get("favmalformedbody").(string)
	if status != http.StatusOK {
		return fmt.Errorf("a malformed favourites cookie must never error the board; got status %d", status)
	}
	// It rendered, and in the plain alphabetical order (the bad tokens were dropped).
	if cardIndex(body, favCardA) > cardIndex(body, favCardB) || cardIndex(body, favCardB) > cardIndex(body, favCardC) {
		return fmt.Errorf("a malformed cookie must fall back to plain alphabetical order\n%s", body)
	}
	attachText(sr, "favourite-malformed-cookie-safe",
		"a malformed/oversized favourites cookie rendered 200 in plain alphabetical order — bad tokens dropped on read")
	return nil
}

// shootFaveClick drives the real star button in a browser (ClickSelector) and captures
// the card floating to the top of its column after the htmx swap — the spec's core
// interaction, captured end to end rather than asserted only structurally.
func shootFaveClick(w *World, sr *StepRun, name, ticket string) error {
	b, err := currentFaveBoard(w)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	star := `[data-testid="star-btn-` + ticket + `-` + favAtt + `"]`
	png, err := webshot.Capture(ctx, b.url("/"), webshot.Options{
		Width: 1280, Height: 900, FullPage: true,
		ClickSelector:          star,
		AfterClickWaitSelector: `.star-btn-on`, // the lit, pinned star after the swap
	})
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png",
		Name:     name,
		Data:     base64.StdEncoding.EncodeToString(png),
	})
	return nil
}
