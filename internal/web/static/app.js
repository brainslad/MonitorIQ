// MonitorIQ front-end helpers (no inline scripts: CSP script-src 'self').
(function () {
  "use strict";

  // Confirmation for destructive forms.
  document.querySelectorAll("form[data-confirm]").forEach(function (f) {
    f.addEventListener("submit", function (e) {
      if (!window.confirm(f.getAttribute("data-confirm"))) e.preventDefault();
    });
  });

  // Monitor form: show only the options of the selected type.
  var form = document.querySelector("[data-monitor-form]");
  if (form) {
    var sel = form.querySelector("[data-type-select]");
    var target = form.querySelector("[data-target-input]");
    var update = function () {
      form.querySelectorAll("fieldset[data-for]").forEach(function (fs) {
        var on = fs.getAttribute("data-for") === sel.value;
        fs.hidden = !on;
        fs.querySelectorAll("input,select,textarea").forEach(function (i) { i.disabled = !on; });
      });
      var opt = sel.options[sel.selectedIndex];
      if (target && opt) target.placeholder = opt.getAttribute("data-hint") || "";
    };
    sel.addEventListener("change", update);
    update();
  }

  // Status page form: derive slug from title for new pages.
  var src = document.querySelector("[data-slug-source]");
  var dst = document.querySelector("[data-slug-target]");
  if (src && dst && !dst.value) {
    var touched = false;
    dst.addEventListener("input", function () { touched = true; });
    src.addEventListener("input", function () {
      if (touched) return;
      dst.value = src.value.toLowerCase().normalize("NFD").replace(/[̀-ͯ]/g, "")
        .replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 63);
    });
  }

  // Monitor detail chart.
  var canvas = document.getElementById("chart");
  if (canvas && window.Chart) {
    var css = getComputedStyle(document.documentElement);
    var color = function (n) { return css.getPropertyValue(n).trim(); };
    var chart = null;
    var load = function (range) {
      fetch(canvas.getAttribute("data-series") + "?range=" + encodeURIComponent(range), { credentials: "same-origin" })
        .then(function (r) { return r.json(); })
        .then(function (data) {
          var pts = data.points || [];
          var labels = pts.map(function (p) { return new Date(p.t); });
          var short = ["1h", "24h", "48h"].indexOf(range) >= 0;
          var fmt = function (d) {
            return short ? d.toLocaleTimeString("pt-PT", { hour: "2-digit", minute: "2-digit" })
              : d.toLocaleDateString("pt-PT", { day: "2-digit", month: "2-digit", year: range === "1y" || range === "2y" ? "2-digit" : undefined });
          };
          var cfg = {
            type: "line",
            data: {
              labels: labels.map(fmt),
              datasets: [
                { label: "Latência média (ms)", data: pts.map(function (p) { return p.avg; }), borderColor: color("--primary"),
                  backgroundColor: "transparent", pointRadius: pts.length > 120 ? 0 : 2, tension: 0.25, spanGaps: true, yAxisID: "y" },
                { label: "Disponibilidade (%)", data: pts.map(function (p) { return p.uptime; }), borderColor: color("--up"),
                  backgroundColor: "transparent", pointRadius: 0, stepped: true, borderWidth: 1.5, yAxisID: "y1" }
              ]
            },
            options: {
              responsive: true, maintainAspectRatio: false, animation: false,
              interaction: { mode: "index", intersect: false },
              scales: {
                x: { ticks: { color: color("--muted"), maxTicksLimit: 10, autoSkip: true }, grid: { color: color("--border") } },
                y: { beginAtZero: true, ticks: { color: color("--muted") }, grid: { color: color("--border") }, title: { display: true, text: "ms", color: color("--muted") } },
                y1: { position: "right", min: 0, max: 100, ticks: { color: color("--muted") }, grid: { display: false }, title: { display: true, text: "%", color: color("--muted") } }
              },
              plugins: { legend: { labels: { color: color("--text") } } }
            }
          };
          if (chart) chart.destroy();
          chart = new window.Chart(canvas, cfg);
        });
    };
    var ranges = document.querySelector("[data-chart-ranges]");
    if (ranges) {
      ranges.addEventListener("click", function (e) {
        var b = e.target.closest("button[data-range]");
        if (!b) return;
        ranges.querySelectorAll("button").forEach(function (x) { x.classList.toggle("active", x === b); });
        load(b.getAttribute("data-range"));
      });
    }
    load("24h");
  }
})();
