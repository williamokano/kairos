// app.js — the hand-written JS budget 10-webui.md names (~600 lines at
// full parity; this pass implements only the decision screen's
// anti-rubber-stamp enforcement, the one piece that must be real for the
// safety story to hold — see L20-webui.md's Future work for what's
// deferred: keyboard model, command palette, log-tail scroll-lock,
// optimistic composer echo, diff-viewer interactions).
//
// This is a CLIENT-SIDE UX AID ONLY. The server independently re-validates
// the reason and typed word at POST /runs/{id}/approve (internal/api's
// existing L13 handler) regardless of what this script allows through —
// disabling this script, or editing the DOM with devtools, cannot bypass
// the server-side check. See internal/web/mutations.go's
// handleAnswerDecision doc comment.
(function () {
  "use strict";

  function wireDecisionScreen(root) {
    var panes = (root.dataset.panes || "").split(",").filter(Boolean);
    var viewed = {};
    panes.forEach(function (p) { viewed[p] = false; });

    var fieldset = root.querySelector("#decision-fieldset");
    if (!fieldset) return;

    function allRisksAccepted() {
      var boxes = root.querySelectorAll(".risk-accept-checkbox");
      for (var i = 0; i < boxes.length; i++) {
        if (!boxes[i].checked) return false;
      }
      return true;
    }

    function maybeEnable() {
      var allViewed = panes.every(function (p) { return viewed[p]; });
      fieldset.disabled = !(allViewed && allRisksAccepted());
    }

    if ("IntersectionObserver" in window) {
      var io = new IntersectionObserver(function (entries) {
        entries.forEach(function (entry) {
          if (entry.isIntersecting) {
            var pane = entry.target.dataset.pane;
            if (pane && pane in viewed) {
              viewed[pane] = true;
              maybeEnable();
            }
          }
        });
      }, { threshold: 0.5 });
      root.querySelectorAll("[data-pane]").forEach(function (el) {
        if (el.dataset.pane !== "decision") io.observe(el);
      });
    } else {
      // No IntersectionObserver: fail closed, not open — the fieldset
      // simply never enables rather than silently skipping the check.
    }

    root.querySelectorAll(".risk-accept-checkbox").forEach(function (cb) {
      cb.addEventListener("change", maybeEnable);
    });
  }

  function init() {
    var root = document.getElementById("decision-root");
    if (root) wireDecisionScreen(root);
  }

  document.addEventListener("DOMContentLoaded", init);
  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail.target && e.detail.target.id === "decision-root") wireDecisionScreen(e.detail.target);
  });
})();
