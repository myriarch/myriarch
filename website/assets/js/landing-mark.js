// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// The loop of the mark on the landing page (layouts/_partials/landing/hero.html, assets/scss/_landing.scss, "Motion").
//
// Without this script the rollout plays once and the page keeps the unchanged mark (decoration: hidden from assistive technology, not
// focusable). With it the rollout loops (the stylesheet holds the timeline: rollout, hold, retraction), and the script decides when the
// loop runs and makes the mark its own control (WCAG 2.2.2: Pause, Stop, Hide). There is no visible button: a click or tap on the mark
// pauses the loop and shows the unchanged mark, another click resumes it. The loop runs
//   - not with prefers-reduced-motion: reduce (the stylesheet plays nothing, and the mark stays a plain image: no role, not focusable),
//   - not while the visitor has paused it,
//   - not while the hero is out of view or the document is hidden.
// The mark becomes a control only while it can move: role="button", tabindex="0", an accessible name, aria-pressed for the paused state, and
// Enter and Space toggle it like a native button. A stopped mark shows the unchanged mark (class is-static, no animation). The loop starts
// again at the retraction that ends a cycle (class is-resumed, the stylesheet sets the offset), so that the dots shrink from the full mark
// and nothing jumps, and the rollout follows at once. There is no timer: the browser runs the animation, and a stopped mark has none.
(function () {
  "use strict";

  var mark = document.querySelector("svg.landing-hero__mark");
  if (!mark || typeof window.matchMedia !== "function") return;

  var reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
  var paused = false; // the choice of the visitor
  var visible = true; // the hero is in the viewport (until the observer says otherwise)
  var playing = null; // null until the first decision
  var started = false;
  var spaceDown = false; // Space went down on the mark (a native button acts on Space only when it saw the keydown)

  function wanted() {
    return !reduced.matches && !paused && visible && !document.hidden;
  }

  // Progress of the rollout that the base style began on load, in ms: the loop continues it where it is, so that a script that
  // arrives late does not restart the dots. The dots start together but end at different times (the last one 180 ms after the first), and
  // a finished animation keeps its end time (fill-mode both), so the largest time over all dots is the progress of the rollout.
  function progress() {
    var dots = mark.querySelectorAll(".dot");
    var time = 0;
    for (var i = 0; i < dots.length; i++) {
      if (typeof dots[i].getAnimations !== "function") return 0;
      var animations = dots[i].getAnimations();
      for (var j = 0; j < animations.length; j++) {
        var t = Number(animations[j].currentTime);
        if (isFinite(t) && t > time) time = t;
      }
    }
    return time;
  }

  // The mark as a control (only while it can animate). Without the control it is the plain decoration of the markup.
  function control() {
    if (reduced.matches) {
      mark.removeAttribute("role");
      mark.removeAttribute("tabindex");
      mark.removeAttribute("aria-label");
      mark.removeAttribute("aria-pressed");
      mark.setAttribute("aria-hidden", "true");
      mark.setAttribute("focusable", "false");
      return;
    }
    mark.removeAttribute("aria-hidden");
    mark.setAttribute("focusable", "true");
    mark.setAttribute("role", "button");
    mark.setAttribute("tabindex", "0");
    mark.setAttribute("aria-label", "Pause logo animation");
    mark.setAttribute("aria-pressed", paused ? "true" : "false");
  }

  function toggle() {
    paused = !paused;
    mark.setAttribute("aria-pressed", paused ? "true" : "false");
    apply();
  }

  function apply() {
    var run = wanted();
    if (run === playing) return;
    playing = run;
    if (run) {
      // The first start continues the rollout of the base style; a later start (class is-resumed) takes the offset of the stylesheet.
      if (started) mark.style.removeProperty("--myr-offset");
      else mark.style.setProperty("--myr-offset", "-" + progress() + "ms");
      mark.classList.remove("is-static");
      mark.classList.add("is-looping");
      if (started) mark.classList.add("is-resumed");
      started = true;
    } else {
      mark.classList.remove("is-looping");
      mark.classList.add("is-static");
    }
  }

  mark.addEventListener("click", function () {
    if (!reduced.matches) toggle();
  });
  // Like a native button: Enter acts on keydown, Space on keyup (and does not scroll the page).
  mark.addEventListener("keydown", function (event) {
    if (reduced.matches) return;
    if (event.key === " " || event.key === "Spacebar") {
      event.preventDefault();
      spaceDown = true;
    } else if (event.key === "Enter" && !event.repeat && !event.altKey && !event.ctrlKey && !event.metaKey) {
      event.preventDefault();
      toggle();
    }
  });
  mark.addEventListener("keyup", function (event) {
    if (event.key !== " " && event.key !== "Spacebar") return;
    event.preventDefault();
    var armed = spaceDown;
    spaceDown = false;
    if (armed && !reduced.matches) toggle();
  });
  mark.addEventListener("blur", function () {
    spaceDown = false;
  });

  function onReducedChange() {
    control();
    apply();
  }

  if (typeof reduced.addEventListener === "function") reduced.addEventListener("change", onReducedChange);
  document.addEventListener("visibilitychange", apply);

  if (typeof window.IntersectionObserver === "function") {
    new window.IntersectionObserver(function (entries) {
      visible = entries[entries.length - 1].isIntersecting;
      apply();
    }).observe(mark.parentElement);
  }

  control();
  apply();
})();
