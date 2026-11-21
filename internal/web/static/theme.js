// Applies the saved colour theme before the page paints (loaded in <head>).
(function () {
  try {
    var t = localStorage.getItem("miq-theme");
    if (t === "light" || t === "dark") document.documentElement.setAttribute("data-theme", t);
  } catch (e) { /* storage unavailable */ }
})();
