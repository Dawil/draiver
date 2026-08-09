package web

// Palette is the single source of truth for Draiver's Australian-bush colours.
// The same hexes were previously hand-duplicated across the favicon SVGs and the
// web tests; every consumer now references a constant here instead of a literal.
//
// These are the BUSH colours (favicons, board session-dots). They are distinct
// from the terminal-style THEME palette in static/style.css (--running #56b6c2,
// --review #f5b955, …); the two schemes are deliberately not reconciled.
//
// How each colour reaches its consumer:
//   - Go (tests, template helpers): these constants directly.
//   - SVG (static favicons): the hex stays typed into the .svg, guarded by a test
//     asserting it equals the matching constant (see palette_test.go), so drift
//     fails CI rather than passing silently.
//   - CSS (browser dots): surfaced as :root custom properties rendered from these
//     constants by paletteVars (web.go), so style.css carries no dot hex — only
//     var(--dot-*) references.
const (
	// Eucalypt green — the letter "D" on every favicon and the "agent running"
	// session-dot. Named for the grey-green of eucalypt foliage.
	Eucalypt = "#3E6B48"

	// Murray rust-red — the Stuck favicon badge and Stuck accents. Named for the
	// iron-rich red earth along the Murray. Reserved for "stuck"; the stopped dot
	// deliberately avoids it.
	MurrayRust = "#B7410E"

	// Blue Mountains misty-blue — the Review favicon badge. Named for the haze
	// over the Blue Mountains.
	BlueMountains = "#6E9BB5"

	// Wattle gold — the "agent stopped" session-dot. Named for Australia's
	// golden-wattle national flower. Reads as a warm orange and is clearly
	// distinct from MurrayRust, so "stopped" never reads as "stuck".
	Wattle = "#E0A50E"

	// Ghost-gum grey — the "disabled" session-dot. Reuses the theme's --muted
	// (the ghost gum's pale bark), so a disabled leftover recedes.
	GhostGum = "#8B93A7"

	// Waratah crimson — the "draiverctld can't run this attempt" error session-dot
	// (drvweb-008). Named for the vivid crimson Waratah (Telopea), so it stays in
	// the bush palette while sitting a clear hue apart from MurrayRust: rust is the
	// burnt orange-red reserved for Stuck, and the error dot must never read as
	// "stuck". A true crimson (little green, a touch of blue) versus rust's orange
	// keeps "the supervisor is failing" legibly distinct from "needs me".
	Waratah = "#B0122F"
)
