// Applies the saved colour theme and sidebar state before the page paints (loaded in <head>).
(function () {
  try {
    var r = document.documentElement;
    var t = localStorage.getItem("miq-theme");
    if (t === "light" || t === "dark") r.setAttribute("data-theme", t);
    if (localStorage.getItem("miq-sidebar") === "collapsed") r.classList.add("sb-collapsed");
  } catch (e) { /* storage unavailable */ }
})();
