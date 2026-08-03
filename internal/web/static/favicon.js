// Swap the board favicon to a badged variant based on the live control-state
// counts. The board region is re-rendered by htmx every few seconds; the <head>
// is not, so we read the counts out of the refreshed markup and point the
// <link rel="icon"> at the right SVG. Stuck outranks Review — a blocked attempt
// is more urgent than one merely awaiting verification.
(function () {
  var PLAIN = "/static/favicon.svg";
  var STUCK = "/static/favicon-stuck.svg";
  var REVIEW = "/static/favicon-review.svg";

  function count(testid) {
    var el = document.querySelector('[data-testid="' + testid + '"]');
    if (!el) return 0;
    var n = parseInt(el.textContent.trim(), 10);
    return isNaN(n) ? 0 : n;
  }

  function update() {
    var link = document.getElementById("favicon");
    if (!link) return;
    var href = PLAIN;
    if (count("count-stuck") > 0) href = STUCK;
    else if (count("count-review") > 0) href = REVIEW;
    if (link.getAttribute("href") !== href) link.setAttribute("href", href);
  }

  document.addEventListener("DOMContentLoaded", update);
  // htmx:afterSwap bubbles to the document after each board refresh.
  document.addEventListener("htmx:afterSwap", update);
})();
