package webshot

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// pngMagic is the 8-byte PNG signature every real PNG starts with.
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// TestCaptureProducesPNG proves webshot drives a real browser end to end: it
// screenshots a self-contained data: URL (no server needed) and asserts the bytes
// are a non-trivial PNG. It skips — rather than fails — when no browser/node is
// available, so the unit rung stays green on a browserless box; the bdd-bound
// acceptance rung is where a browser is a hard precondition.
func TestCaptureProducesPNG(t *testing.T) {
	if !Available() {
		t.Skip("webshot: node/playwright/chromium not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const page = "data:text/html,<html><body style='background:%23123456;margin:0'>" +
		"<h1 style='color:white;font-family:sans-serif'>draiver</h1></body></html>"
	png, err := Capture(ctx, page, Options{Width: 400, Height: 240})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(png) < 200 {
		t.Fatalf("screenshot too small to be a real render: %d bytes", len(png))
	}
	if !bytes.HasPrefix(png, pngMagic) {
		t.Fatalf("output is not a PNG (first bytes %v)", png[:min(8, len(png))])
	}
}

// TestCaptureProbeRunsScriptThenProbes proves the drvweb-031 Probe hook: the click
// lands, then an async probe drives the page further, is awaited, and its JSON result
// comes back — so a scenario can assert live browser state, not only server HTML.
func TestCaptureProbeRunsScriptThenProbes(t *testing.T) {
	if !Available() {
		t.Skip("webshot: node/playwright/chromium not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const page = "data:text/html,<html><body><textarea id=t></textarea>" +
		"<button id=b onclick=\"document.getElementById('t').value+='!'\">go</button></body></html>"
	png, probe, err := CaptureProbe(ctx, page, Options{
		Width: 400, Height: 240,
		ClickSelector: "#b",
		Probe: `(async () => {
			const t = document.getElementById('t');
			await new Promise((r) => setTimeout(r, 10));
			t.value += 'hi';
			return { value: t.value };
		})()`,
	})
	if err != nil {
		t.Fatalf("CaptureProbe: %v", err)
	}
	if !bytes.HasPrefix(png, pngMagic) {
		t.Fatalf("output is not a PNG")
	}
	if probe != `{"value":"!hi"}` {
		t.Fatalf("probe = %s, want {\"value\":\"!hi\"}", probe)
	}
}
