package web

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/Dawil/draiver/internal/project"
)

// Favourite board cards (drv-050): a per-viewer "star" that floats a card to the
// top of its column. It is a view preference of the person looking at the board —
// NOT ticket state — so it lives entirely in that web user's cookie and the server
// persists nothing. The cookie is the sole source of truth, re-read on every board
// render; this file owns reading it, toggling it, writing it back, and the
// stable favourites-first reordering layered over a boardVM.

const (
	// favesCookie is the one cookie this ticket introduces — the first cookie the web
	// layer uses at all. It holds the set of favourited refs as comma-joined,
	// url.QueryEscape'd `ticket/id` tokens. Path=/ SameSite=Lax HttpOnly (the server
	// owns every read/write — no client JS needs it), a long Max-Age, and NOT
	// Secure-only (the dev board is served over plain HTTP).
	favesCookie = "draiver_faves"
	// favesMaxAge is a long life so a favourite survives browser restarts — it is a
	// durable viewer preference, just not a server-side one. One year in seconds.
	favesMaxAge = 365 * 24 * 60 * 60
	// favesMaxEncodedLen and favesMaxRefs cap the cookie defensively so a stale or
	// maliciously-oversized value can never bloat a request or a render. Reads past
	// the caps are truncated, not errored.
	favesMaxEncodedLen = 4096
	favesMaxRefs       = 200
)

// faveRef is the canonical favourite key for a card: the same `ticket/id` ref that
// keys every other per-attempt card action, so a favourite is unambiguous and two
// attempts of one ticket are independently starrable.
func faveRef(ticket, id string) string { return ticket + "/" + id }

// isFaveToken reports whether a decoded token is a well-formed `ticket/id` ref —
// exactly one slash, non-empty halves, and each half limited to the id charset
// (alnum plus -._). A token that fails is DROPPED on read rather than erroring the
// render, so a malformed/stale cookie degrades to the plain alphabetical order.
func isFaveToken(tok string) bool {
	slash := strings.IndexByte(tok, '/')
	if slash <= 0 || slash == len(tok)-1 {
		return false
	}
	if strings.IndexByte(tok[slash+1:], '/') >= 0 {
		return false // a second slash: not a bare ticket/id ref
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '.' || c == '_' || c == '/':
		default:
			return false
		}
	}
	return true
}

// parseFaves reads the favourites cookie off the request and returns the set of
// favourited refs. It never errors: a missing cookie yields an empty set (plain
// alphabetical order), and an oversized/malformed cookie has its bad or excess
// tokens dropped — so a stale cookie can never break a board render.
func parseFaves(r *http.Request) map[string]bool {
	set := map[string]bool{}
	if r == nil {
		return set
	}
	c, err := r.Cookie(favesCookie)
	if err != nil || c.Value == "" {
		return set
	}
	raw := c.Value
	if len(raw) > favesMaxEncodedLen {
		raw = raw[:favesMaxEncodedLen] // cap defensively; a truncated tail token just fails to decode/validate
	}
	for _, tok := range strings.Split(raw, ",") {
		if len(set) >= favesMaxRefs {
			break
		}
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		dec, err := url.QueryUnescape(tok)
		if err != nil || !isFaveToken(dec) {
			continue // drop unknown/malformed tokens
		}
		set[dec] = true
	}
	return set
}

// encodeFaves renders a favourite set back to the cookie wire form: url.QueryEscape
// each ref (so the `/` and any odd byte are percent-encoded) and comma-join them in
// a stable sorted order, so the same set always encodes to the same string. The
// result is capped to favesMaxRefs refs and favesMaxEncodedLen bytes.
func encodeFaves(set map[string]bool) string {
	refs := make([]string, 0, len(set))
	for ref := range set {
		if !isFaveToken(ref) {
			continue
		}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	if len(refs) > favesMaxRefs {
		refs = refs[:favesMaxRefs]
	}
	var b strings.Builder
	for _, ref := range refs {
		tok := url.QueryEscape(ref)
		// Stop before overflowing the cap (+1 for the joining comma).
		if b.Len()+len(tok)+1 > favesMaxEncodedLen {
			break
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(tok)
	}
	return b.String()
}

// writeFavesCookie writes the favourite set back onto the response. An empty set
// expires the cookie (Max-Age<0) so unstarring the last card clears it entirely
// rather than leaving an empty husk.
func writeFavesCookie(w http.ResponseWriter, set map[string]bool) {
	val := encodeFaves(set)
	c := &http.Cookie{
		Name:     favesCookie,
		Value:    val,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   favesMaxAge,
	}
	if val == "" {
		c.MaxAge = -1 // delete the cookie when nothing is favourited
	}
	http.SetCookie(w, c)
}

// applyFaves layers the per-viewer favourites over an already-assembled boardVM: it
// sets the Favourited presentation bit on every card whose ref is in the set, then
// stably reorders each of the five columns so favourited cards rise above
// non-favourited ones. The sort is STABLE, so within each partition the existing
// alphabetical order board() delivered (ticket id, then attempt id, via LoadAll) is
// preserved — favourites alphabetical among themselves, non-favourites untouched.
// board() stays request-blind; this is the single post-step every render path runs.
func applyFaves(vm *boardVM, faves map[string]bool) {
	cols := []*[]project.Attempt{&vm.Running, &vm.Pending, &vm.NeedsMe, &vm.Review, &vm.Done}
	for _, col := range cols {
		for i := range *col {
			(*col)[i].Favourited = faves[faveRef((*col)[i].Ticket, (*col)[i].ID)]
		}
		sort.SliceStable(*col, func(i, j int) bool {
			// favourited sorts before non-favourited; equal-favourited keeps order.
			return (*col)[i].Favourited && !(*col)[j].Favourited
		})
	}
}
