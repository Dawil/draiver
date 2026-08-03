// Keep the board favicon in sync with the live control-state without coupling to
// the board's test hooks. The /board handler emits an HX-Trigger response header
// on every refresh; htmx dispatches it as a bubbling "draiver:favicon" DOM event
// whose detail carries the variant href, and we point <link rel="icon"> at it —
// no DOM scraping. Stuck outranks Review; that precedence is decided server-side
// (see boardVM.FaviconHref), which also server-renders the initial href.
(function () {
  document.addEventListener("draiver:favicon", function (e) {
    var link = document.getElementById("favicon");
    if (!link || !e.detail || !e.detail.href) return;
    if (link.getAttribute("href") !== e.detail.href) {
      link.setAttribute("href", e.detail.href);
    }
  });
})();
