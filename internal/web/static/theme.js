/* Copyright (C) 2026 Brainslad <https://github.com/brainslad>
 * SPDX-License-Identifier: GPL-3.0-or-later
 * Part of MonitorIQ; see LICENSE and NOTICE (author attribution must be kept). */
// Applies the saved colour theme and sidebar state before the page paints (loaded in <head>).
(function () {
  try {
    var r = document.documentElement;
    var t = localStorage.getItem("miq-theme");
    if (t === "light" || t === "dark") r.setAttribute("data-theme", t);
    if (localStorage.getItem("miq-sidebar") === "collapsed") r.classList.add("sb-collapsed");
  } catch (e) { /* storage unavailable */ }
})();
