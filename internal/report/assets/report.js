// Self-contained interactivity for the standalone cucumber report (drv-018).
// Inlined into the rendered file; no external dependencies, works offline.
// Vanilla JS only — expand/collapse all, and a "failures only" filter.
(function () {
  "use strict";

  function onReady(fn) {
    if (document.readyState !== "loading") fn();
    else document.addEventListener("DOMContentLoaded", fn);
  }

  onReady(function () {
    var root = document.querySelector(".report");
    if (!root) return;

    function setAll(open) {
      root.querySelectorAll("details.feature, details.scenario").forEach(function (d) {
        d.open = open;
      });
    }

    var expandBtn = root.querySelector("[data-act=expand]");
    var collapseBtn = root.querySelector("[data-act=collapse]");
    var failuresOnly = root.querySelector("[data-act=failures]");

    if (expandBtn) expandBtn.addEventListener("click", function () { setAll(true); });
    if (collapseBtn) collapseBtn.addEventListener("click", function () { setAll(false); });

    if (failuresOnly) {
      failuresOnly.addEventListener("change", function () {
        var on = failuresOnly.checked;
        root.querySelectorAll("details.scenario").forEach(function (sc) {
          var failed = sc.getAttribute("data-status") === "failed";
          sc.classList.toggle("filtered-hidden", on && !failed);
          if (on && failed) sc.open = true;
        });
        // Hide a feature whose every scenario is now hidden.
        root.querySelectorAll("details.feature").forEach(function (ft) {
          var scenarios = ft.querySelectorAll("details.scenario");
          var anyVisible = false;
          scenarios.forEach(function (sc) {
            if (!sc.classList.contains("filtered-hidden")) anyVisible = true;
          });
          ft.classList.toggle("filtered-hidden", on && scenarios.length > 0 && !anyVisible);
          if (on && anyVisible) ft.open = true;
        });
      });
    }
  });
})();
