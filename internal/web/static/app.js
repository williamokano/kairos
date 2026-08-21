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

// ---------------------------------------------------------------------
// Confirm dialogs — cancel / fork / source-pause (L23-webui-revamp.md).
//
// A native <dialog>, opened only by an explicit click (never by page
// load, never by a single keypress), whose submit button starts disabled
// and enables ONLY once the "type the id to confirm" input matches the
// dialog's own data-confirm-target exactly — the same "no accidental
// single-click destructive action" discipline app.js's decision-screen
// code above already established. This, too, is a client-side UX aid
// only: every route these forms post to (handleCancelRun/handleForkRun/
// handlePauseSource, internal/web/mutations.go) independently re-checks
// the SAME "confirm" field server-side and rejects a mismatch with 422 —
// disabling this script, or editing the DOM, cannot bypass that check.
// ---------------------------------------------------------------------
(function () {
  "use strict";

  function wireConfirmDialogs(root) {
    root.querySelectorAll("[data-open-dialog]").forEach(function (btn) {
      if (btn.dataset.wired) return;
      btn.dataset.wired = "1";
      btn.addEventListener("click", function () {
        // getElementById, not querySelector — data-open-dialog carries a
        // bare element id (e.g. a source id a user chose freely via
        // `kairos src add <id>`), which may contain characters that are
        // not valid unescaped in a CSS selector string even though they
        // are perfectly valid in an HTML id attribute.
        var dlg = document.getElementById(btn.getAttribute("data-open-dialog"));
        if (dlg && typeof dlg.showModal === "function") dlg.showModal();
      });
    });

    root.querySelectorAll("dialog.confirm-dialog").forEach(function (dlg) {
      if (dlg.dataset.wired) return;
      dlg.dataset.wired = "1";
      var form = dlg.querySelector("form");
      if (!form) return;
      var confirmInput = form.querySelector('input[name="confirm"]');
      var submitBtn = form.querySelector("[data-confirm-submit]");
      var want = form.dataset.confirmTarget || "";

      function refresh() {
        if (submitBtn) submitBtn.disabled = !(confirmInput && confirmInput.value === want);
      }
      if (confirmInput) confirmInput.addEventListener("input", refresh);
      refresh();

      dlg.querySelectorAll("[data-close-dialog]").forEach(function (b) {
        b.addEventListener("click", function () { dlg.close(); });
      });
      // A successful submit's response replaces #mutation-result; close the
      // dialog either way so a failed attempt's error is readable behind it.
      form.addEventListener("htmx:afterRequest", function () { dlg.close(); });
    });
  }

  function init() { wireConfirmDialogs(document); }
  document.addEventListener("DOMContentLoaded", init);
  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail.target) wireConfirmDialogs(e.detail.target);
  });
})();

// ---------------------------------------------------------------------
// The command palette (':' / Ctrl+K / Cmd+K) — a literal port of
// internal/tui/palette.go's resolvePalette, screenNames, and
// paletteVerbs (L15-tui.md), not a reinterpretation: NO fuzzy matching
// and NO history, exactly 09-cli-and-tui.md's stated design ("no
// fuzzy-search over everything, no command history as a feature. A typo
// gets no match, not a guess."). Screens that need a run id (run,
// conversation, logs) resolve against the CURRENT page's run context
// (<body data-run-id>, set by renderPage's optional runID — see
// render.go) exactly as the TUI's own 'l'/'c' bindings only act "if
// m.runInspector.runID != """; with none, they report "no match" rather
// than guessing one.
// ---------------------------------------------------------------------
(function () {
  "use strict";

  // screenNames mirrors internal/tui/palette.go's map verbatim in shape:
  // a bare screen/page name resolves to a URL, some needing the current
  // run context. "bench"/"benchmark" have no web equivalent (the web UI
  // never built one) — deliberately absent, not silently mapped to "/".
  var screenNames = {
    "home": function () { return "/"; },
    "runs": function () { return "/runs"; },
    "inbox": function () { return "/"; },
    "runners": function () { return "/runners"; },
    "run": function (runID) { return runID ? "/runs/" + runID : null; },
    "conversation": function (runID) { return runID ? "/c/" + runID : null; },
    "conversations": function (runID) { return runID ? "/c/" + runID : null; },
    "logs": function (runID) { return runID ? "/runs/" + runID : null; }
  };

  // paletteVerbs mirrors internal/tui/palette.go's own narrow verb map —
  // "a real, if narrow, verb-to-screen mapping," not a general command
  // dispatcher (kairos run/approve/etc. as full palette actions are
  // Future work there too).
  var paletteVerbs = {
    "approve": function () { return "/"; },
    "inbox": function () { return "/"; },
    "status": function () { return "/"; }
  };

  // looksLikeULID mirrors internal/tui/palette.go's looksLikeULID exactly:
  // 26 Crockford-base32-alphabet characters, a shape check only.
  function looksLikeULID(s) {
    return /^[0-9A-Za-z]{26}$/.test(s);
  }

  // resolvePalette mirrors internal/tui/palette.go's resolvePalette
  // exactly: screen name, then verb, then ULID shape, in that order, no
  // fallback fuzzy step.
  function resolvePalette(input, runID) {
    input = input.trim();
    if (!input) return { matched: false };
    if (Object.prototype.hasOwnProperty.call(screenNames, input)) {
      return { matched: true, url: screenNames[input](runID) };
    }
    if (Object.prototype.hasOwnProperty.call(paletteVerbs, input)) {
      return { matched: true, url: paletteVerbs[input](runID) };
    }
    if (looksLikeULID(input)) {
      return { matched: true, url: "/runs/" + input };
    }
    return { matched: false };
  }
  // Exposed for the harness's own structural tests (see palette_test.go's
  // sibling doc — Go cannot execute this file, so its own port fidelity
  // is checked by asserting these exact literals appear, not by running
  // it); harmless in production, npm-free, no test runner wired to it.
  window.__kairosResolvePalette = resolvePalette;

  var overlay, input, status;

  function currentRunID() {
    return document.body.getAttribute("data-run-id") || "";
  }

  function openPalette() {
    overlay.hidden = false;
    input.value = ""; // no history: always starts empty
    status.textContent = "";
    input.focus();
  }

  function closePalette() {
    overlay.hidden = true;
    input.value = "";
    status.textContent = "";
  }

  function submitPalette() {
    var result = resolvePalette(input.value, currentRunID());
    if (!result.matched) {
      status.textContent = "no match";
      return;
    }
    if (!result.url) {
      status.textContent = "no active run in this page";
      return;
    }
    closePalette();
    window.location.assign(result.url);
  }

  function init() {
    overlay = document.getElementById("palette-overlay");
    input = document.getElementById("palette-input");
    status = document.getElementById("palette-status");
    if (!overlay || !input || !status) return;

    input.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { e.preventDefault(); closePalette(); }
      else if (e.key === "Enter") { e.preventDefault(); submitPalette(); }
    });
    overlay.addEventListener("click", function (e) {
      if (e.target === overlay) closePalette();
    });
  }

  document.addEventListener("DOMContentLoaded", init);
  window.__kairosOpenPalette = function () { if (overlay) openPalette(); };
  window.__kairosPaletteOpen = function () { return !!overlay && !overlay.hidden; };
})();

// ---------------------------------------------------------------------
// Full keyboard model — mirrors internal/tui/keys.go's global NAV-mode
// bindings and the per-screen j/k/gg/G/Enter list bindings
// (screens_home.go, screens_inbox.go), adapted to page navigation instead
// of screen swaps (10-webui.md; L23-webui-revamp.md). Deliberately does
// NOT bind 'q'/'Q': the TUI quits its own process; a browser tab has no
// equivalent action a page can safely take (window.close() only works on
// a script-opened window) — named here rather than faked with a
// navigate-away.
// ---------------------------------------------------------------------
(function () {
  "use strict";

  var statusEl;
  var lastGPress = 0;

  function setStatus(msg) {
    if (statusEl) statusEl.textContent = msg;
  }

  function isTypingTarget(el) {
    if (!el) return false;
    var tag = el.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable;
  }

  function dialogOpen() {
    return !!document.querySelector("dialog[open]");
  }

  function currentRunID() {
    return document.body.getAttribute("data-run-id") || "";
  }

  function navList() {
    return document.querySelector("[data-nav-list]");
  }

  function navItems(list) {
    return list ? Array.prototype.slice.call(list.querySelectorAll("[data-nav-item]")) : [];
  }

  function moveCursor(delta) {
    var list = navList();
    var items = navItems(list);
    if (items.length === 0) return;
    var current = items.findIndex(function (el) { return el.classList.contains("nav-cursor"); });
    items.forEach(function (el) { el.classList.remove("nav-cursor"); });
    var next;
    if (current === -1) {
      next = delta > 0 ? 0 : items.length - 1;
    } else {
      next = Math.min(Math.max(current + delta, 0), items.length - 1);
    }
    items[next].classList.add("nav-cursor");
    items[next].scrollIntoView({ block: "nearest" });
  }

  function jumpCursor(toEnd) {
    var list = navList();
    var items = navItems(list);
    if (items.length === 0) return;
    items.forEach(function (el) { el.classList.remove("nav-cursor"); });
    var idx = toEnd ? items.length - 1 : 0;
    items[idx].classList.add("nav-cursor");
    items[idx].scrollIntoView({ block: "nearest" });
  }

  function openCursor() {
    var list = navList();
    var items = navItems(list);
    var current = items.find(function (el) { return el.classList.contains("nav-cursor"); });
    if (!current) return;
    var href = current.getAttribute("data-href");
    if (!href) {
      var a = current.querySelector("a[href]");
      href = a ? a.getAttribute("href") : null;
    }
    if (href) window.location.assign(href);
  }

  function focusComposer() {
    var composer = document.querySelector('input[name="definitionPath"]');
    if (composer) composer.focus();
  }

  function handleKey(e) {
    // Never intercept a modified keypress (browser shortcuts, e.g.
    // Ctrl+R reload) except the two explicitly bound below.
    var isPaletteToggle = (e.key === "k" && (e.metaKey || e.ctrlKey)) || (e.key === ":" && !e.metaKey && !e.ctrlKey && !e.altKey);

    if (window.__kairosPaletteOpen && window.__kairosPaletteOpen()) {
      return; // the palette input field owns keydown while open (see above)
    }

    if (isPaletteToggle) {
      e.preventDefault();
      if (window.__kairosOpenPalette) window.__kairosOpenPalette();
      return;
    }

    if (isTypingTarget(document.activeElement) || dialogOpen()) {
      return; // ModeINPUT-equivalent: global nav keys are suppressed
    }
    if (e.metaKey || e.ctrlKey || e.altKey) return;

    switch (e.key) {
      case "h":
        window.location.assign("/");
        break;
      case "r":
        window.location.assign("/runs");
        break;
      case "a":
        window.location.assign("/"); // "waiting on you" lives on home (no separate /inbox page)
        break;
      case "l": {
        var runID = currentRunID();
        if (runID) window.location.assign("/runs/" + runID);
        else setStatus("no active run in this page");
        break;
      }
      case "c": {
        var rid = currentRunID();
        if (rid) window.location.assign("/c/" + rid);
        else setStatus("no active run in this page");
        break;
      }
      case "j":
        e.preventDefault();
        moveCursor(1);
        break;
      case "k":
        e.preventDefault();
        moveCursor(-1);
        break;
      case "g": {
        var now = Date.now();
        if (now - lastGPress < 500) { jumpCursor(false); lastGPress = 0; }
        else { lastGPress = now; }
        break;
      }
      case "G":
        jumpCursor(true);
        break;
      case "Enter":
        openCursor();
        break;
      case "i":
        focusComposer();
        break;
      case "Escape":
        window.history.back();
        break;
      default:
        return;
    }
  }

  function init() {
    statusEl = document.getElementById("kbd-status");
    document.addEventListener("keydown", handleKey);
  }

  document.addEventListener("DOMContentLoaded", init);
})();
