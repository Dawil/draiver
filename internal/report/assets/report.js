// Self-contained interactivity for the cucumber report (drv-018, drv-023).
// Inlined into the rendered file; no external dependencies, works offline.
// Vanilla JS only. Provides: expand/collapse all, a "failures only" filter, a
// clickable tag facet bar (multi-select OR), a free-text subsequence filter box,
// and a group-by control (folder / tag / status / flat). All filtering composes
// (tag OR, AND-ed with the text box and the failures toggle) and runs entirely
// client-side over the rendered DOM.
(function () {
  "use strict";

  function onReady(fn) {
    if (document.readyState !== "loading") fn();
    else document.addEventListener("DOMContentLoaded", fn);
  }

  // subseq reports whether every char of needle appears in haystack in order
  // (fuzzy, not substring). Both are expected lowercased by the caller.
  function subseq(needle, haystack) {
    if (!needle) return true;
    var i = 0;
    for (var j = 0; j < haystack.length && i < needle.length; j++) {
      if (haystack[j] === needle[i]) i++;
    }
    return i === needle.length;
  }

  // severity mirrors the Go rollup so a group's dot shows its worst scenario.
  var SEVERITY = { failed: 6, undefined: 5, ambiguous: 4, pending: 3, skipped: 2, passed: 1, "": 0 };
  function sev(s) { return SEVERITY[s] != null ? SEVERITY[s] : 1; }
  function worstStatus(list) {
    var worst = "";
    list.forEach(function (s) { if (sev(s) > sev(worst)) worst = s; });
    return worst || "skipped";
  }
  function statusClass(s) {
    return SEVERITY[s] != null && s !== "" ? s : "unknown";
  }

  onReady(function () {
    var root = document.querySelector(".report");
    if (!root) return;
    var tree = root.querySelector(".tree");

    var expandBtn = root.querySelector("[data-act=expand]");
    var collapseBtn = root.querySelector("[data-act=collapse]");
    var failuresOnly = root.querySelector("[data-act=failures]");
    var groupBy = root.querySelector("[data-act=groupby]");
    var filterBox = root.querySelector("[data-act=filter]");
    var facetBtns = Array.prototype.slice.call(root.querySelectorAll(".facet"));
    var clearTags = root.querySelector("[data-act=clear-tags]");

    function setAll(open) {
      root.querySelectorAll("details.capability, details.feature, details.rule, details.scenario").forEach(function (d) {
        if (!d.classList.contains("filtered-hidden")) d.open = open;
      });
    }
    if (expandBtn) expandBtn.addEventListener("click", function () { setAll(true); });
    if (collapseBtn) collapseBtn.addEventListener("click", function () { setAll(false); });

    if (!tree) return; // nothing navigable (empty report)

    // Canonical, original-order feature nodes — the source of truth we re-group
    // from. Captured once, before any DOM surgery.
    var featureNodes = Array.prototype.slice.call(tree.querySelectorAll("details.feature"));

    var state = { groupby: "folder", tags: {}, tagCount: 0, query: "", failures: false };

    // --- filtering ----------------------------------------------------------

    function tokens(s) { return (s || "").split(/\s+/).filter(Boolean); }

    function applyFilters() {
      var active = state.failures || state.tagCount > 0 || state.query !== "";
      var q = state.query;

      tree.querySelectorAll("details.scenario").forEach(function (sc) {
        var visible = true;
        if (state.failures && sc.getAttribute("data-status") !== "failed") visible = false;
        if (visible && state.tagCount > 0) {
          var scTags = tokens(sc.getAttribute("data-tags"));
          visible = scTags.some(function (t) { return state.tags[t]; });
        }
        if (visible && q) visible = subseq(q, sc.getAttribute("data-key") || "");
        sc.classList.toggle("filtered-hidden", !visible);
      });

      // Hide containers with no visible scenario, bottom-up: rule → feature →
      // capability. A container with zero scenarios at all stays visible.
      function reap(sel) {
        tree.querySelectorAll(sel).forEach(function (box) {
          var all = box.querySelectorAll("details.scenario");
          if (all.length === 0) { box.classList.remove("filtered-hidden"); return; }
          var anyVisible = Array.prototype.some.call(all, function (sc) {
            return !sc.classList.contains("filtered-hidden");
          });
          box.classList.toggle("filtered-hidden", !anyVisible);
        });
      }
      reap("details.rule");
      reap("details.feature");
      reap("details.capability");

      // Reveal matches: open the ancestors of every visible scenario so a filter
      // never leaves its hits buried in a collapsed branch.
      if (active) {
        tree.querySelectorAll("details.scenario:not(.filtered-hidden)").forEach(function (sc) {
          sc.open = true;
          var p = sc.parentNode;
          while (p && p !== tree) {
            if (p.tagName === "DETAILS") p.open = true;
            p = p.parentNode;
          }
        });
      }
    }

    // --- grouping -----------------------------------------------------------

    // featureTagSet returns the union of effective tags across a feature's
    // scenarios — what "group by tag" buckets on.
    function featureTagSet(featureEl) {
      var set = {};
      featureEl.querySelectorAll("details.scenario").forEach(function (sc) {
        tokens(sc.getAttribute("data-tags")).forEach(function (t) { set[t] = true; });
      });
      return Object.keys(set);
    }

    function section(label, statuses) {
      var d = document.createElement("details");
      d.className = "capability";
      d.open = true;
      d.setAttribute("data-group", "");
      var st = worstStatus(statuses);
      d.innerHTML =
        '<summary><span class="marker">▸</span>' +
        '<span class="sdot status-' + statusClass(st) + '" style="background:var(--c)"></span>' +
        '<span class="title"></span><span class="count"></span></summary>' +
        '<div class="capability-body"></div>';
      d.querySelector(".title").textContent = label;
      return d;
    }

    function regroup() {
      var mode = state.groupby;
      tree.setAttribute("data-groupby", mode);
      tree.textContent = "";

      if (mode === "flat") {
        featureNodes.forEach(function (f) { tree.appendChild(f); });
        applyFilters();
        return;
      }

      // Build an ordered list of buckets: {label, order, features:[nodes]}.
      var buckets = {};
      var order = [];
      function bucket(key, label) {
        if (!buckets[key]) { buckets[key] = { label: label, features: [] }; order.push(key); }
        return buckets[key];
      }

      featureNodes.forEach(function (f) {
        if (mode === "folder") {
          var folder = f.getAttribute("data-folder") || "";
          bucket(folder, folder || "(ungrouped)").features.push(f);
        } else if (mode === "status") {
          var st = f.getAttribute("data-status") || "skipped";
          bucket(st, st).features.push(f);
        } else if (mode === "tag") {
          var tags = featureTagSet(f);
          if (tags.length === 0) { bucket("", "(untagged)").features.push(f.cloneNode(true)); return; }
          tags.forEach(function (t) { bucket(t, t).features.push(f.cloneNode(true)); });
        }
      });

      // Order the buckets per mode.
      if (mode === "folder") {
        order.sort(function (a, b) {
          if ((a === "") !== (b === "")) return a === "" ? 1 : -1; // ungrouped last
          return a < b ? -1 : a > b ? 1 : 0;
        });
      } else if (mode === "status") {
        order.sort(function (a, b) { return sev(b) - sev(a); }); // worst first
      } else if (mode === "tag") {
        order.sort(function (a, b) {
          var ca = buckets[a].features.length, cb = buckets[b].features.length;
          if (ca !== cb) return cb - ca;
          if ((a === "") !== (b === "")) return a === "" ? 1 : -1; // untagged last
          return a < b ? -1 : a > b ? 1 : 0;
        });
      }

      order.forEach(function (key) {
        var b = buckets[key];
        var statuses = b.features.map(function (f) { return f.getAttribute("data-status") || ""; });
        var scenarios = 0;
        b.features.forEach(function (f) { scenarios += f.querySelectorAll("details.scenario").length; });
        var sec = section(b.label, statuses);
        sec.querySelector(".count").textContent = scenarios + " scenarios";
        var body = sec.querySelector(".capability-body");
        b.features.forEach(function (f) { body.appendChild(f); });
        tree.appendChild(sec);
      });

      applyFilters();
    }

    // --- wiring -------------------------------------------------------------

    if (failuresOnly) failuresOnly.addEventListener("change", function () {
      state.failures = failuresOnly.checked; applyFilters();
    });
    if (filterBox) filterBox.addEventListener("input", function () {
      state.query = filterBox.value.trim().toLowerCase(); applyFilters();
    });
    if (groupBy) groupBy.addEventListener("change", function () {
      state.groupby = groupBy.value; regroup();
    });

    function syncClear() { if (clearTags) clearTags.hidden = state.tagCount === 0; }

    // reflect state back onto every control bound to a tag: the facet buttons and
    // the inline chips (which share the selection, so either toggles the filter).
    function syncTagControls() {
      facetBtns.forEach(function (b) {
        b.setAttribute("aria-pressed", state.tags[b.getAttribute("data-tag")] ? "true" : "false");
      });
      root.querySelectorAll(".tag").forEach(function (chip) {
        chip.classList.toggle("active", !!state.tags[chip.textContent.trim()]);
      });
      syncClear();
    }

    function toggleTag(tag, on) {
      if (!tag) return;
      if (on == null) on = !state.tags[tag];
      if (on) { if (!state.tags[tag]) { state.tags[tag] = true; state.tagCount++; } }
      else { if (state.tags[tag]) { delete state.tags[tag]; state.tagCount--; } }
      syncTagControls();
      applyFilters();
    }

    facetBtns.forEach(function (btn) {
      btn.addEventListener("click", function () { toggleTag(btn.getAttribute("data-tag")); });
    });
    // Inline chips on features/scenarios are toggles for the same selection.
    root.addEventListener("click", function (e) {
      var chip = e.target.closest ? e.target.closest(".tag") : null;
      if (chip && root.contains(chip)) { e.preventDefault(); toggleTag(chip.textContent.trim()); }
    });
    if (clearTags) clearTags.addEventListener("click", function () {
      state.tags = {}; state.tagCount = 0;
      syncTagControls();
      applyFilters();
    });
  });
})();
