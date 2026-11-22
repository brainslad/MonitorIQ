/* Copyright (C) 2026 Brainslad <https://github.com/brainslad>
 * SPDX-License-Identifier: GPL-3.0-or-later
 * Part of MonitorIQ; see LICENSE and NOTICE (author attribution must be kept). */
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

  // ---------------------------------------------------------------- sidebar
  document.querySelectorAll("[data-sidebar-collapse]").forEach(function (b) {
    b.addEventListener("click", function () {
      var on = !root.classList.contains("sb-collapsed");
      root.classList.toggle("sb-collapsed", on);
      store.set("miq-sidebar", on ? "collapsed" : "open");
    });
  });
  var closeSidebar = function () { root.classList.remove("sb-open"); };
  document.querySelectorAll("[data-sidebar-open]").forEach(function (b) {
    b.addEventListener("click", function () { root.classList.add("sb-open"); });
  });
  document.querySelectorAll("[data-sidebar-close]").forEach(function (b) { b.addEventListener("click", closeSidebar); });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") closeSidebar();
    var t = e.target;
    var typing = t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
    if (e.key === "/" && !typing && !e.ctrlKey && !e.metaKey && !e.altKey) {
      var q = document.querySelector("[data-search]") || document.querySelector("[data-top-search]");
      if (q && q.offsetParent !== null) { e.preventDefault(); q.focus(); q.select(); }
    }
  });
  // The top search filters the dashboard table in place when it is on the page.
  var topSearch = document.querySelector("[data-top-search]");
  var pageSearch = document.querySelector("[data-search]");
  if (topSearch && pageSearch) {
    topSearch.form.addEventListener("submit", function (e) {
      e.preventDefault();
      pageSearch.value = topSearch.value;
      pageSearch.dispatchEvent(new Event("input"));
      var all = document.getElementById("all-monitors");
      if (all) all.scrollIntoView({ behavior: "smooth", block: "start" });
    });
  }

  // ---------------------------------------------------------------- greeting in local time
  var greet = document.querySelector("[data-greeting]");
  if (greet) {
    var h = new Date().getHours();
    var word = h < 5 ? "Good evening" : h < 12 ? "Good morning" : h < 18 ? "Good afternoon" : "Good evening";
    var name = greet.getAttribute("data-name");
    greet.textContent = word + (name ? ", " + name : "");
  }

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
    var urlQ = new URLSearchParams(window.location.search).get("q");
    if (urlQ !== null) {
      state = { status: "all", type: "all", q: urlQ };
      var all = document.getElementById("all-monitors");
      if (all && urlQ) setTimeout(function () { all.scrollIntoView({ block: "start" }); }, 0);
    }
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
      x: { ticks: { color: css("--muted"), maxTicksLimit: 6, autoSkip: true, maxRotation: 0, font: { family: css("--sans") } }, grid: { display: false }, border: { display: false } },
      y: { beginAtZero: true, position: "right", ticks: { color: css("--muted"), maxTicksLimit: 4, font: { family: css("--sans") }, callback: function (v) { return v + " ms"; } },
        grid: { color: css("--border"), borderDash: [3, 3] }, border: { display: false } }
    };
  };

  // Dashboard: checks by result (stacked bars) with the average response time (dotted line).
  var dash = document.getElementById("dash-chart");
  if (dash && window.Chart) {
    var dashChart = null, dashData = null, dashRange = store.sget("miq-dash-range") || "24h";
    var sums = {};
    document.querySelectorAll("[data-sum]").forEach(function (el) { sums[el.getAttribute("data-sum")] = el; });
    var setSum = function (k, v) { if (sums[k]) sums[k].textContent = v; };
    var rangeWords = { "24h": "the last 24 hours", "7d": "the last 7 days", "30d": "the last 30 days" };
    var bucketLabel = function (t, hours) {
      var d = new Date(t);
      if (hours >= 24) return d.toLocaleDateString([], { day: "numeric", month: "short" });
      if (hours > 1) return d.toLocaleDateString([], { weekday: "short" }) + " " + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
      return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
    };
    var spanLabel = function (t, hours) {
      var a = new Date(t), b = new Date(a.getTime() + hours * 3600000);
      var hm = function (d) { return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false }); };
      if (hours >= 24) return a.toLocaleDateString([], { weekday: "long", day: "numeric", month: "long" });
      if (hours > 1) return a.toLocaleDateString([], { weekday: "short", day: "numeric", month: "short" }) + ", " + hm(a) + " – " + hm(b);
      return hm(a) + " – " + hm(b);
    };
    var fmtN = function (n) { return Math.round(n).toLocaleString("en-US"); };
    var fmtMs = function (v) { return v == null ? "—" : (v >= 1000 ? (v / 1000).toFixed(2) + " s" : Math.round(v) + " ms"); };
    var fillSummary = function () {
      var s = dashData.summary || {};
      setSum("uptime", s.uptime == null ? "—" : (s.uptime >= 99.995 ? "100" : s.uptime.toFixed(2)));
      setSum("uptime-label", "availability over " + rangeWords[dashRange]);
      setSum("checks", fmtN(s.checks || 0));
      setSum("avg", fmtMs(s.avg_ms));
      setSum("incidents", fmtN(s.incidents || 0));
      if (s.worst && s.worst.uptime < 100) {
        setSum("worst", s.worst.uptime.toFixed(2) + " %");
        setSum("worst-sub", spanLabel(s.worst.t, s.hours));
      } else {
        setSum("worst", s.checks ? "100 %" : "—");
        setSum("worst-sub", s.checks ? "no failed checks in " + rangeWords[dashRange] : "no checks yet");
      }
    };
    var drawDash = function () {
      if (!dashData) return;
      var b = dashData.buckets || [], hours = (dashData.summary || {}).hours || 1;
      var ticks = { color: css("--muted"), font: { family: css("--sans"), size: 11 } };
      var bar = function (label, key, color) {
        return { type: "bar", label: label, data: b.map(function (x) { return x[key]; }), backgroundColor: color, stack: "c", yAxisID: "y",
          borderRadius: 4, borderSkipped: false, barPercentage: 0.72, categoryPercentage: 0.9, order: 2 };
      };
      var cfg = {
        data: {
          labels: b.map(function (x) { return bucketLabel(x.t, hours); }),
          datasets: [
            { type: "line", label: "Avg response", data: b.map(function (x) { return x.avg_ms; }), yAxisID: "y1", borderColor: css("--bar-line"),
              borderDash: [2, 4], borderWidth: 2, borderCapStyle: "round", pointRadius: 0, pointHoverRadius: 4, pointBackgroundColor: css("--bar-line"),
              tension: 0.35, spanGaps: true, order: 1 },
            bar("Operational", "up", css("--bar-up")),
            bar("Degraded", "degraded", css("--bar-degraded")),
            bar("Down", "down", css("--bar-down"))
          ]
        },
        options: {
          responsive: true, maintainAspectRatio: false, animation: { duration: 250 },
          interaction: { mode: "index", intersect: false },
          scales: {
            x: { stacked: true, grid: { display: false }, border: { display: false }, ticks: Object.assign({ maxRotation: 0, autoSkipPadding: 14 }, ticks) },
            y: { stacked: true, beginAtZero: true, grid: { color: css("--border"), drawTicks: false }, border: { display: false, dash: [3, 4] },
              ticks: Object.assign({ maxTicksLimit: 5, padding: 8, callback: function (v) { return v >= 1000 ? (v / 1000) + "k" : v; } }, ticks) },
            y1: { position: "right", beginAtZero: true, grid: { display: false }, border: { display: false },
              ticks: Object.assign({ maxTicksLimit: 5, padding: 8, callback: function (v) { return v + " ms"; } }, ticks) }
          },
          plugins: {
            legend: { display: false },
            tooltip: {
              backgroundColor: css("--btn-dark"), titleColor: css("--btn-dark-text"), bodyColor: css("--btn-dark-text"),
              padding: 10, cornerRadius: 10, boxPadding: 4, usePointStyle: true,
              titleFont: { family: css("--sans"), weight: "600" }, bodyFont: { family: css("--sans") },
              callbacks: {
                title: function (items) { return items.length ? spanLabel(b[items[0].dataIndex].t, hours) : ""; },
                label: function (c) {
                  if (c.dataset.type === "line") return " Avg response: " + fmtMs(c.parsed.y);
                  return " " + c.dataset.label + ": " + fmtN(c.parsed.y || 0);
                }
              }
            }
          }
        }
      };
      if (dashChart) dashChart.destroy();
      dashChart = new window.Chart(dash, cfg);
    };
    renderers.push(drawDash);
    var loadDash = function () {
      fetch(dash.getAttribute("data-series") + "?range=" + encodeURIComponent(dashRange), { credentials: "same-origin" })
        .then(function (r) { return r.json(); })
        .then(function (d) { dashData = d; fillSummary(); drawDash(); });
    };
    var dashRanges = document.querySelector("[data-dash-ranges]");
    if (dashRanges) {
      dashRanges.querySelectorAll("button").forEach(function (x) { x.classList.toggle("active", x.getAttribute("data-range") === dashRange); });
      dashRanges.addEventListener("click", function (e) {
        var btn = e.target.closest("button[data-range]");
        if (!btn) return;
        dashRange = btn.getAttribute("data-range");
        store.sset("miq-dash-range", dashRange);
        dashRanges.querySelectorAll("button").forEach(function (x) { x.classList.toggle("active", x === btn); });
        loadDash();
      });
    }
    loadDash();
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
          plugins: { legend: { labels: { color: css("--text"), boxWidth: 12, font: { family: css("--sans") } } } }
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

// ---------------------------------------------------------------- e-mail settings: default port per connection type
(function () {
  var sec = document.querySelector("[data-smtp-security]");
  var port = document.querySelector("[data-smtp-port]");
  if (!sec || !port) return;
  var defaults = { starttls: "587", tls: "465", none: "25" };
  sec.addEventListener("change", function () {
    var known = Object.keys(defaults).map(function (k) { return defaults[k]; });
    if (port.value === "" || known.indexOf(port.value) >= 0) port.value = defaults[sec.value];
  });
})();
