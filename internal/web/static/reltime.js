// Keep every log entry's timestamp showing a live relative age ("3 minutes
// ago"). The server renders an initial age and the precise UTC stamp in the
// title (see web.go relativeAge / eventVM.TSFull), which is enough with JS off;
// this script recomputes from the machine-readable datetime attribute so the age
// stays current even on a Done attempt, whose log region never polls. It also
// enriches the tooltip with the operator's local time alongside UTC.
//
// Buckets mirror web.go's relativeAge — keep the boundaries and wording in sync.
(function () {
  function agoPlural(n, unit) {
    return n === 1 ? "1 " + unit + " ago" : n + " " + unit + "s ago";
  }

  function relativeAge(seconds) {
    var s = seconds < 0 ? 0 : seconds; // future stamp (clock skew) -> just now
    if (s < 5) return "just now";
    if (s < 60) return agoPlural(Math.floor(s), "second");
    if (s < 3600) return agoPlural(Math.floor(s / 60), "minute");
    if (s < 86400) return agoPlural(Math.floor(s / 3600), "hour");
    if (s < 172800) return "yesterday";
    if (s < 604800) return agoPlural(Math.floor(s / 86400), "day");
    if (s < 2592000) return agoPlural(Math.floor(s / 604800), "week");
    if (s < 31536000) return agoPlural(Math.floor(s / 2592000), "month");
    return agoPlural(Math.floor(s / 31536000), "year");
  }

  function pad(n) {
    return n < 10 ? "0" + n : "" + n;
  }

  // Full tooltip derived purely from the datetime attribute (idempotent across
  // repaints): the operator's local time, with UTC alongside.
  function tooltip(d) {
    var utc =
      d.getUTCFullYear() +
      "-" + pad(d.getUTCMonth() + 1) +
      "-" + pad(d.getUTCDate()) +
      " " + pad(d.getUTCHours()) +
      ":" + pad(d.getUTCMinutes()) +
      ":" + pad(d.getUTCSeconds()) + " UTC";
    return d.toLocaleString() + " (" + utc + ")";
  }

  function paint() {
    var now = Date.now();
    var nodes = document.querySelectorAll("time.ts[datetime]");
    for (var i = 0; i < nodes.length; i++) {
      var el = nodes[i];
      var t = Date.parse(el.getAttribute("datetime"));
      if (isNaN(t)) continue; // leave the server-rendered fallback in place
      el.textContent = relativeAge((now - t) / 1000);
      el.title = tooltip(new Date(t));
    }
  }

  // Paint on load, after every htmx swap (the log poll replaces these nodes,
  // and the event bubbles to document), and on a slow tick so ages advance on a
  // static (non-polling) page.
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", paint);
  } else {
    paint();
  }
  document.addEventListener("htmx:afterSettle", paint);
  setInterval(paint, 30000);
})();
