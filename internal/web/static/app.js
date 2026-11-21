// MonitorIQ front-end (no inline scripts: CSP script-src 'self').
(function () {
  "use strict";
  var root = document.documentElement;
  var store = {
    get: function (k) { try { return localStorage.getItem(k); } catch (e) { return null; } },
    set: function (k, v) { try { localStorage.setItem(k, v); } catch (e) { /* ignore */ } },
    sget: function (k) { try { return sessionStorage.getItem(k); } catch (e) { return null; } },
    sset: function (k, v) { try { sessionStorage.setItem(k, v); } catch (e) { /* ignore */ } }
  };
  var renderers = [];
  var css = function (n) { return getComputedStyle(root).getPropertyValue(n).trim(); };

  // ---------------------------------------------------------------- theme
  var isDark = function () {
    var t = root.getAttribute("data-theme");
    if (t) return t === "dark";
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  };
  document.querySelectorAll("[data-theme-toggle]").forEach(function (b) {
    b.addEventListener("click", function () {
      var next = isDark() ? "light" : "dark";
      root.setAttribute("data-theme", next);
      store.set("miq-theme", next);
      renderers.forEach(function (fn) { fn(); });
    });
  });

  // ---------------------------------------------------------------- confirmations
  document.querySelectorAll("form[data-confirm]").forEach(function (f) {
    f.addEventListener("submit", function (e) {
      if (!window.confirm(f.getAttribute("data-confirm"))) e.preventDefault();
    });
  });

  // ---------------------------------------------------------------- monitor form
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

  // ---------------------------------------------------------------- slug helper
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

  // ---------------------------------------------------------------- dashboard filters
  var filters = document.querySelector("[data-filters]");
  if (filters) {
    var state = { status: "all", type: "all", q: "" };
    try { var saved = JSON.parse(store.sget("miq-filters") || "{}"); Object.assign(state, saved); } catch (e) { /* ignore */ }
    var rows = document.querySelectorAll(".mon-table tbody tr");
    var empty = document.querySelector(".no-results");
    var search = filters.querySelector("[data-search]");
    var apply = function () {
      var shown = 0;
      var q = state.q.toLowerCase();
      rows.forEach(function (r) {
        var ok = (state.status === "all" || r.getAttribute("data-status") === state.status) &&
          (state.type === "all" || r.getAttribute("data-type") === state.type) &&
          (!q || r.getAttribute("data-text").toLowerCase().indexOf(q) >= 0);
        r.hidden = !ok;
        if (ok) shown++;
      });
      if (empty) empty.hidden = shown > 0;
      filters.querySelectorAll("[data-filter-group]").forEach(function (g) {
        var key = g.getAttribute("data-filter-group");
        g.querySelectorAll(".fpill").forEach(function (b) { b.classList.toggle("active", b.getAttribute("data-value") === state[key]); });
      });
      store.sset("miq-filters", JSON.stringify(state));
    };
    filters.querySelectorAll("[data-filter-group]").forEach(function (g) {
      g.addEventListener("click", function (e) {
        var b = e.target.closest(".fpill");
        if (!b) return;
        state[g.getAttribute("data-filter-group")] = b.getAttribute("data-value");
        apply();
      });
    });
    if (search) {
      search.value = state.q;
      search.addEventListener("input", function () { state.q = search.value; apply(); });
    }
    apply();
  }

  // ---------------------------------------------------------------- auto refresh (dashboard)
  if (document.getElementById("dash-chart")) {
    setInterval(function () {
      var a = document.activeElement;
      if (a && (a.tagName === "INPUT" || a.tagName === "TEXTAREA")) return;
      if (document.visibilityState === "visible") window.location.reload();
    }, 60000);
  }

  // ---------------------------------------------------------------- charts
  var fmtTime = function (d, withDate) {
    return withDate ? d.toLocaleDateString("en-GB", { day: "2-digit", month: "2-digit" })
      : d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" });
  };
  var baseScales = function () {
    return {
      x: { ticks: { color: css("--muted"), maxTicksLimit: 6, autoSkip: true, maxRotation: 0, font: { family: css("--font") } }, grid: { display: false }, border: { display: false } },
      y: { beginAtZero: true, position: "right", ticks: { color: css("--muted"), maxTicksLimit: 4, font: { family: css("--font") }, callback: function (v) { return v + " ms"; } },
        grid: { color: css("--border"), borderDash: [3, 3] }, border: { display: false } }
    };
  };

  // Dashboard: average response across monitors.
  var dash = document.getElementById("dash-chart");
  if (dash && window.Chart) {
    var dashChart = null, dashData = null;
    var drawDash = function () {
      if (!dashData) return;
      var pts = dashData.points || [];
      var cfg = {
        type: "line",
        data: {
          labels: pts.map(function (p) { return fmtTime(new Date(p.t)); }),
          datasets: [{ label: "Average response", data: pts.map(function (p) { return p.avg; }), borderColor: css("--chart-line"),
            backgroundColor: css("--chart-fill"), fill: true, pointRadius: 0, borderWidth: 1.6, tension: 0.25, spanGaps: true }]
        },
        options: {
          responsive: true, maintainAspectRatio: false, animation: false,
          interaction: { mode: "index", intersect: false },
          scales: baseScales(),
          plugins: { legend: { display: false }, tooltip: { callbacks: { label: function (c) { return c.parsed.y == null ? "—" : c.parsed.y.toFixed(1) + " ms"; } } } }
        }
      };
      if (dashChart) dashChart.destroy();
      dashChart = new window.Chart(dash, cfg);
    };
    renderers.push(drawDash);
    fetch(dash.getAttribute("data-series"), { credentials: "same-origin" })
      .then(function (r) { return r.json(); })
      .then(function (d) { dashData = d; drawDash(); });
  }

  // Monitor detail: latency + availability with range selector.
  var canvas = document.getElementById("chart");
  if (canvas && window.Chart) {
    var chart = null, data = null, range = "24h";
    var draw = function () {
      if (!data) return;
      var pts = data.points || [];
      var withDate = ["1h", "24h", "48h"].indexOf(range) < 0;
      var scales = baseScales();
      scales.y.position = "left";
      scales.y1 = { position: "right", min: 0, max: 100, ticks: { color: css("--muted"), maxTicksLimit: 3, callback: function (v) { return v + "%"; } }, grid: { display: false }, border: { display: false } };
      var cfg = {
        type: "line",
        data: {
          labels: pts.map(function (p) { return fmtTime(new Date(p.t), withDate); }),
          datasets: [
            { label: "Latency (ms)", data: pts.map(function (p) { return p.avg; }), borderColor: css("--chart-line"), backgroundColor: css("--chart-fill"),
              fill: true, pointRadius: 0, borderWidth: 1.6, tension: 0.25, spanGaps: true, yAxisID: "y" },
            { label: "Availability (%)", data: pts.map(function (p) { return p.uptime; }), borderColor: css("--up"),
              backgroundColor: "transparent", pointRadius: 0, stepped: true, borderWidth: 1.2, yAxisID: "y1" }
          ]
        },
        options: {
          responsive: true, maintainAspectRatio: false, animation: false,
          interaction: { mode: "index", intersect: false }, scales: scales,
          plugins: { legend: { labels: { color: css("--text"), boxWidth: 12, font: { family: css("--font") } } } }
        }
      };
      if (chart) chart.destroy();
      chart = new window.Chart(canvas, cfg);
    };
    renderers.push(draw);
    var load = function () {
      fetch(canvas.getAttribute("data-series") + "?range=" + encodeURIComponent(range), { credentials: "same-origin" })
        .then(function (r) { return r.json(); })
        .then(function (d) { data = d; draw(); });
    };
    var ranges = document.querySelector("[data-chart-ranges]");
    if (ranges) {
      ranges.addEventListener("click", function (e) {
        var b = e.target.closest("button[data-range]");
        if (!b) return;
        ranges.querySelectorAll("button").forEach(function (x) { x.classList.toggle("active", x === b); });
        range = b.getAttribute("data-range");
        load();
      });
    }
    load();
  }
})();

// ---------------------------------------------------------------- authentication pages
(function () {
  "use strict";
  var textOf = function (sel) {
    var el = document.querySelector(sel);
    if (!el) return "";
    if (el.value !== undefined && el.tagName === "INPUT") return el.value;
    return Array.prototype.map.call(el.querySelectorAll("li"), function (li) { return li.textContent.trim(); }).join("\n");
  };
  document.querySelectorAll("[data-copy]").forEach(function (b) {
    b.addEventListener("click", function () {
      var t = textOf(b.getAttribute("data-copy"));
      var done = function () { var o = b.textContent; b.textContent = "Copied"; setTimeout(function () { b.textContent = o; }, 1500); };
      if (navigator.clipboard) navigator.clipboard.writeText(t).then(done);
    });
  });
  document.querySelectorAll("[data-download]").forEach(function (b) {
    b.addEventListener("click", function () {
      var t = "MonitorIQ recovery codes (" + location.host + ")\nEach code can be used once.\n\n" + textOf(b.getAttribute("data-download")) + "\n";
      var a = document.createElement("a");
      a.href = URL.createObjectURL(new Blob([t], { type: "text/plain" }));
      a.download = b.getAttribute("data-filename") || "codes.txt";
      document.body.appendChild(a); a.click(); a.remove();
    });
  });
  var uf = document.querySelector("[data-user-form]");
  if (uf) {
    var sel = uf.querySelector("[data-source]");
    var sync = function () {
      var local = !sel || sel.value === "local";
      uf.querySelectorAll("[data-local-only] input").forEach(function (i) { i.required = local; i.disabled = !local; });
      uf.querySelector("[data-local-only]").hidden = !local;
      uf.querySelector("[data-external-only]").hidden = local;
    };
    if (sel) sel.addEventListener("change", sync);
    sync();
  }
  var presets = {
    ad: { url: "ldaps://dc01.example.com:636", user_filter: "ad", username_attr: "sAMAccountName", name_attr: "displayName", email_attr: "mail", nested: true },
    openldap: { url: "ldaps://ldap.example.com:636", user_filter: "openldap", username_attr: "uid", name_attr: "cn", email_attr: "mail", nested: false }
  };
  document.querySelectorAll("[data-ldap-preset]").forEach(function (b) {
    b.addEventListener("click", function () {
      var p = presets[b.getAttribute("data-ldap-preset")], f = b.form;
      var set = function (n, v) { var i = f.elements["ldap_" + n]; if (i) i.value = v; };
      if (!f.elements.ldap_url.value || /example\.com/.test(f.elements.ldap_url.value)) set("url", p.url);
      var flt = f.elements.ldap_user_filter;
      flt.value = flt.getAttribute("data-" + p.user_filter);
      set("username_attr", p.username_attr); set("name_attr", p.name_attr); set("email_attr", p.email_attr);
      f.elements.ldap_nested.checked = p.nested;
    });
  });
  var tenant = document.querySelector("[data-azure-tenant]");
  if (tenant) {
    tenant.addEventListener("input", function () {
      var f = tenant.form, v = tenant.value.trim();
      if (!v) return;
      f.elements.oidc_issuer.value = "https://login.microsoftonline.com/" + v + "/v2.0";
      if (!f.elements.oidc_button_label.value) f.elements.oidc_button_label.value = "Sign in with Microsoft";
    });
  }
})();
