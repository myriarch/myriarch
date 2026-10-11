// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// The loop of the mark on the landing page (layouts/_partials/landing/hero.html, assets/scss/_landing.scss, "Motion").
//
// Without this script the rollout plays once and the page keeps the unchanged mark. With it the rollout loops (the stylesheet holds the
// timeline: rollout, hold, retraction), and the script only decides when the loop runs:
//   - not with prefers-reduced-motion: reduce (the stylesheet plays nothing and hides the button),
//   - not while the visitor has paused it with the button (WCAG 2.2.2: Pause, Stop, Hide),
//   - not while the hero is out of view or the document is hidden.
// A stopped mark shows the unchanged mark (class is-static, no animation). The loop starts again from the beginning of a cycle. There is
// no timer: the browser runs the animation, and a stopped mark has none.
(function () {
  "use strict";

  var mark = document.querySelector("svg.landing-hero__mark");
  var button = document.querySelector(".landing-hero__motion");
  if (!mark || !button || typeof window.matchMedia !== "function") return;

  var reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
  var paused = false; // the choice of the visitor
  var visible = true; // the hero is in the viewport (until the observer says otherwise)
  var playing = null; // null until the first decision
  var started = false;

  function wanted() {
    return !reduced.matches && !paused && visible && !document.hidden;
  }

  // Progress of the rollout that the base style began on load, in ms: the loop continues it where it is, so that a script that
  // arrives late does not restart the dots.
  function progress() {
    var dot = mark.querySelector(".dot");
    if (!dot || typeof dot.getAnimations !== "function") return 0;
    var animations = dot.getAnimations();
    var time = animations.length > 0 ? Number(animations[0].currentTime) : 0;
    return isFinite(time) && time > 0 ? time : 0;
  }

  function apply() {
    var run = wanted();
    if (run === playing) return;
    playing = run;
    if (run) {
      mark.style.setProperty("--myr-offset", started ? "0s" : "-" + progress() + "ms");
      mark.classList.remove("is-static");
      mark.classList.add("is-looping");
      if (started) mark.classList.add("is-resumed");
      started = true;
    } else {
      mark.classList.remove("is-looping");
      mark.classList.add("is-static");
    }
  }

  button.addEventListener("click", function () {
    paused = !paused;
    button.setAttribute("aria-pressed", paused ? "true" : "false");
    apply();
  });

  if (typeof reduced.addEventListener === "function") reduced.addEventListener("change", apply);
  document.addEventListener("visibilitychange", apply);

  if (typeof window.IntersectionObserver === "function") {
    new window.IntersectionObserver(function (entries) {
      visible = entries[entries.length - 1].isIntersecting;
      apply();
    }).observe(mark.parentElement);
  }

  button.hidden = false;
  apply();
})();
