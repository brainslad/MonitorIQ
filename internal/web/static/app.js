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
    var state = { status: "all", type: "all", group: "all", q: "" };
    try { var saved = JSON.parse(store.sget("miq-filters") || "{}"); Object.assign(state, saved); } catch (e) { /* ignore */ }
    var urlQ = new URLSearchParams(window.location.search).get("q");
    if (urlQ !== null) {
      state = { status: "all", type: "all", group: "all", q: urlQ };
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
          (state.group === "all" || r.getAttribute("data-group") === state.group) &&
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

  // A saved group filter may point to a group that no longer exists.
  if (filters && !filters.querySelector('[data-filter-group="group"] [data-value="' + state.group + '"]') && state.group !== "all") {
    state.group = "all"; apply();
  }

  // ---------------------------------------------------------------- monitor list: select and move to a group
  var bulk = document.querySelector("[data-bulk]");
  if (bulk) {
    var bar = document.querySelector("[data-bulk-bar]");
    var boxes = document.querySelectorAll('input[name="monitor"][form="bulk"]');
    var count = bar.querySelector("[data-bulk-count]");
    var target = bar.querySelector("[data-bulk-target]");
    var newName = bar.querySelector("[data-bulk-new]");
    var go = bar.querySelector("[data-bulk-go]");
    var refresh = function () {
      var n = 0;
      boxes.forEach(function (b) { if (b.checked) n++; b.closest("tr").classList.toggle("picked", b.checked); });
      bar.hidden = n === 0;
      document.body.classList.toggle("has-bulk", n > 0);
      count.textContent = n + " selected";
      newName.hidden = target.value !== "new";
      newName.required = target.value === "new";
      go.disabled = !target.value || (target.value === "new" && !newName.value.trim());
      document.querySelectorAll("[data-section]").forEach(function (sec) {
        var all = sec.querySelector("[data-select-all]");
        var own = sec.querySelectorAll('input[name="monitor"]');
        if (!all) return;
        var on = 0;
        own.forEach(function (b) { if (b.checked) on++; });
        all.checked = own.length > 0 && on === own.length;
        all.indeterminate = on > 0 && on < own.length;
        all.disabled = own.length === 0;
      });
    };
    document.querySelectorAll("[data-section] [data-select-all]").forEach(function (all) {
      all.addEventListener("change", function () {
        all.closest("[data-section]").querySelectorAll('input[name="monitor"]').forEach(function (b) { b.checked = all.checked; });
        refresh();
      });
    });
    boxes.forEach(function (b) { b.addEventListener("change", refresh); });
    target.addEventListener("change", function () { refresh(); if (target.value === "new") newName.focus(); });
    newName.addEventListener("input", refresh);
    bar.querySelector("[data-bulk-clear]").addEventListener("click", function () {
      boxes.forEach(function (b) { b.checked = false; });
      refresh();
    });
    refresh();
  }

  // ---------------------------------------------------------------- group form: pick monitors
  var pick = document.querySelector("[data-pick-table]");
  if (pick) {
    var pickAll = pick.querySelector("[data-select-all]");
    var pickRows = pick.querySelectorAll("tbody tr");
    var pickSync = function () {
      var vis = 0, on = 0;
      pickRows.forEach(function (r) { if (!r.hidden) { vis++; if (r.querySelector("input").checked) on++; } });
      pickAll.checked = vis > 0 && on === vis;
      pickAll.indeterminate = on > 0 && on < vis;
    };
    pickAll.addEventListener("change", function () {
      pickRows.forEach(function (r) { if (!r.hidden) r.querySelector("input").checked = pickAll.checked; });
    });
    pick.addEventListener("change", pickSync);
    var pf = document.querySelector("[data-pick-filter]");
    if (pf) pf.addEventListener("input", function () {
      var q = pf.value.toLowerCase();
      pickRows.forEach(function (r) { r.hidden = q && r.getAttribute("data-text").toLowerCase().indexOf(q) < 0; });
      pickSync();
    });
    pickSync();
  }

  // ---------------------------------------------------------------- status page form: tick a whole group
  document.querySelectorAll("[data-pick-group]").forEach(function (b) {
    b.addEventListener("click", function () {
      var rows = document.querySelectorAll('tr[data-group="' + b.getAttribute("data-pick-group") + '"] input[name="monitor"]');
      var all = true;
      rows.forEach(function (c) { if (!c.checked) all = false; });
      rows.forEach(function (c) { c.checked = !all; });
      b.classList.toggle("active", !all);
    });
  });

  // ---------------------------------------------------------------- availability map picker
  var hp = document.querySelector("[data-heat-picker]"), hpOpen = document.querySelector("[data-heat-open]");
  if (hp && hpOpen) {
    var hpMax = parseInt(hp.getAttribute("data-max"), 10) || 10;
    var hpBoxes = Array.prototype.slice.call(hp.querySelectorAll('input[name="monitor"]'));
    // Ticked order: pinned monitors keep their position, new ones go last.
    var order = hpBoxes.filter(function (b) { return b.checked; })
      .sort(function (a, b) { return a.getAttribute("data-pos") - b.getAttribute("data-pos"); });
    var hpSync = function () {
      hpBoxes.forEach(function (b) {
        var li = b.closest("li"), at = order.indexOf(b);
        li.classList.toggle("on", at >= 0);
        li.querySelector(".hp-pos").textContent = at >= 0 ? at + 1 : "";
        if (!b.checked && b.getAttribute("data-off") !== "1") b.disabled = order.length >= hpMax;
      });
      hp.classList.toggle("full", order.length >= hpMax);
      hp.querySelector("[data-heat-count]").textContent = order.length;
    };
    hpBoxes.forEach(function (b) {
      if (b.disabled) b.setAttribute("data-off", "1");
      b.addEventListener("change", function () {
        var at = order.indexOf(b);
        if (b.checked && at < 0) order.push(b);
        if (!b.checked && at >= 0) order.splice(at, 1);
        hpSync();
      });
    });
    // Send the monitors in the ticked order.
    hp.addEventListener("submit", function () {
      order.forEach(function (b) { b.name = ""; });
      order.forEach(function (b) {
        var h = document.createElement("input");
        h.type = "hidden"; h.name = "monitor"; h.value = b.value;
        hp.appendChild(h);
      });
    });
    var hpToggle = function (show) {
      hp.hidden = !show;
      var card = hp.closest(".card"); if (card) card.classList.toggle("picking", show);
      hpOpen.setAttribute("aria-expanded", show ? "true" : "false");
      if (show) { var f = hp.querySelector("[data-heat-filter]"); if (f) f.focus(); }
    };
    hpOpen.addEventListener("click", function () { hpToggle(hp.hidden); });
    hp.querySelector("[data-heat-cancel]").addEventListener("click", function () { hpToggle(false); });
    document.addEventListener("keydown", function (e) { if (e.key === "Escape" && !hp.hidden) hpToggle(false); });
    var hf = hp.querySelector("[data-heat-filter]");
    if (hf) hf.addEventListener("input", function () {
      var v = hf.value.trim().toLowerCase();
      hp.querySelectorAll(".hp-list li").forEach(function (li) { li.hidden = v && li.getAttribute("data-text").toLowerCase().indexOf(v) < 0; });
    });
    hpSync();
  }

  // Selects that save their form as soon as they change (user roles).
  document.querySelectorAll("select[data-autosubmit]").forEach(function (sel) {
    sel.addEventListener("change", function () { if (sel.form) sel.form.submit(); });
  });

  // ---------------------------------------------------------------- branding preview
  var bf = document.querySelector("[data-branding-form]");
  if (bf) {
    var fi = bf.querySelector("[data-footer-input]"), fp = document.querySelector("[data-footer-preview]");
    if (fi && fp) fi.addEventListener("input", function () { fp.textContent = fi.value.trim(); fp.hidden = !fp.textContent; });
    var showLogo = function (which, url) {
      document.querySelectorAll('[data-preview]').forEach(function (box) {
        var mode = box.getAttribute("data-preview");
        if (which === "light" && mode === "dark" && box.getAttribute("data-dark-set") === "1") return;
        if (which === "dark" && mode !== "dark") return;
        var sub = box.querySelector(".brand-sub");
        var slogan = sub ? sub.textContent : "";
        box.textContent = "";
        box.className = "brand custom";
        var text = document.createElement("span"); text.className = "brand-text";
        var lg = document.createElement("span"); lg.className = "brand-logo";
        var img = document.createElement("img"); img.src = url; img.alt = "Logo";
        lg.appendChild(img); text.appendChild(lg);
        var s2 = document.createElement("span"); s2.className = "brand-sub"; s2.textContent = slogan;
        text.appendChild(s2); box.appendChild(text);
        if (which === "dark") box.setAttribute("data-dark-set", "1");
      });
    };
    bf.querySelectorAll("[data-logo-input]").forEach(function (inp) {
      inp.addEventListener("change", function () {
        var f = inp.files && inp.files[0];
        if (!f) return;
        if (f.size > 5 * 1024 * 1024) { inp.value = ""; inp.setCustomValidity("The logo file can be at most 5 MB."); inp.reportValidity(); return; }
        inp.setCustomValidity("");
        // A data: URL, because the page's CSP does not allow blob: images.
        var rd = new FileReader();
        rd.onload = function () {
          var which = inp.getAttribute("data-logo-input");
          if (which === "mini") {
            document.querySelectorAll("[data-mini-preview]").forEach(function (box) {
              box.textContent = "";
              var img = document.createElement("img"); img.src = rd.result; img.alt = "";
              box.appendChild(img);
              box.setAttribute("data-mini-set", "1");
            });
            return;
          }
          showLogo(which, rd.result);
        };
        rd.readAsDataURL(f);
      });
    });
  }

  // ---------------------------------------------------------------- auto refresh (dashboard)
  if (document.getElementById("dash-chart")) {
    setInterval(function () {
      var a = document.activeElement;
      if (a && (a.tagName === "INPUT" || a.tagName === "TEXTAREA")) return;
      // Keep an open drill-down or availability map picker on screen.
      if (document.querySelector("[data-drill]:not([hidden]), [data-heat-picker]:not([hidden])")) return;
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
          onClick: function (evt, els, chart) {
            var hit = chart.getElementsAtEventForMode(evt, "index", { intersect: false }, false);
            if (hit.length) openDrill(hit[0].index);
          },
          onHover: function (evt, els) { evt.native.target.style.cursor = els.length ? "pointer" : "default"; },
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

    // Drill-down: what was recorded during the column that was clicked.
    var drill = document.querySelector("[data-drill]"), drillIdx = -1, drillFilter = "all", drillEvents = [];
    var q = function (sel) { return drill.querySelector(sel); };
    var el = function (tag, cls, text) {
      var e = document.createElement(tag);
      if (cls) e.className = cls;
      if (text != null) e.textContent = text;
      return e;
    };
    var stClass = ["unknown", "up", "degraded", "down"], stLabel = ["Unknown", "Operational", "Degraded", "Down"];
    var numCell = function (n, cls) { var td = el("td", "num " + (n ? cls : "zero"), fmtN(n)); return td; };
    var renderEvents = function () {
      var ul = q("[data-drill-events]");
      ul.textContent = "";
      var list = drillEvents.filter(function (e) { return drillFilter === "all" || e.kind === drillFilter; });
      if (!list.length) {
        ul.appendChild(el("li", "drill-empty", drillFilter === "check" ? "No failed or degraded checks in this period." :
          drillFilter === "change" ? "No status changes in this period." : "Nothing was recorded in this period: every check passed."));
        return;
      }
      var multiDay = ((dashData.summary || {}).hours || 1) >= 24;
      list.forEach(function (e) {
        var li = el("li"), d = new Date(e.t);
        var t = el("time", null, (multiDay ? d.toLocaleDateString([], { day: "numeric", month: "short" }) + " " : "") +
          d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: multiDay ? undefined : "2-digit", hour12: false }));
        t.setAttribute("datetime", e.t);
        t.title = d.toLocaleString();
        li.appendChild(t);
        li.appendChild(el("span", "dot " + stClass[e.status]));
        var main = el("div"), a = el("a", null, e.monitor);
        a.href = "/monitors/" + e.monitor_id;
        main.appendChild(a);
        var what;
        if (e.kind === "change") {
          what = stLabel[e.from] + " → " + stLabel[e.status];
          main.appendChild(el("span", "kind", "status change"));
        } else {
          what = stLabel[e.status] + (e.latency_ms != null ? " · " + fmtMs(e.latency_ms) : "");
          main.appendChild(el("span", "kind", "check"));
        }
        var sm = el("small");
        sm.appendChild(el("span", "c-" + stClass[e.status], what));
        if (e.message) sm.appendChild(document.createTextNode(" · " + e.message));
        main.appendChild(sm);
        li.appendChild(main);
        ul.appendChild(li);
      });
    };
    var renderDrill = function (d) {
      var tb = q("[data-drill-monitors]");
      tb.textContent = "";
      if (!d.monitors.length) {
        var tr = el("tr"), td = el("td", "drill-empty", "No checks were run in this period.");
        td.colSpan = 6; tr.appendChild(td); tb.appendChild(tr);
      }
      d.monitors.forEach(function (m) {
        var tr = el("tr"), td = el("td"), a = el("a", "mon-name", m.name);
        a.href = "/monitors/" + m.id;
        td.appendChild(a);
        td.appendChild(el("div", "muted small", m.host + " · " + m.type.toUpperCase()));
        tr.appendChild(td);
        tr.appendChild(numCell(m.up, ""));
        tr.appendChild(numCell(m.degraded, "c-degraded"));
        tr.appendChild(numCell(m.down, "c-down"));
        tr.appendChild(el("td", "num", fmtMs(m.avg_ms)));
        tr.appendChild(el("td", "num hide-sm", fmtMs(m.max_ms)));
        tb.appendChild(tr);
      });
      drillEvents = d.events;
      var changes = d.events.filter(function (e) { return e.kind === "change"; }).length;
      q("[data-drill-count]").textContent = "· " + (d.truncated ? "latest " : "") + d.events.length + " (" + changes + " status " + (changes === 1 ? "change" : "changes") + ")";
      renderEvents();
    };
    var openDrill = function (i) {
      if (!drill || !dashData) return;
      var b = dashData.buckets || [], hours = (dashData.summary || {}).hours || 1;
      if (i < 0 || i >= b.length) return;
      drillIdx = i;
      var x = b[i], total = x.up + x.degraded + x.down;
      drill.hidden = false;
      drill.classList.add("loading");
      q("[data-drill-title]").textContent = spanLabel(x.t, hours);
      q("[data-drill-sub]").textContent = fmtN(total) + " checks · " + fmtN(x.degraded) + " degraded · " + fmtN(x.down) + " down · avg response " + fmtMs(x.avg_ms);
      q('[data-drill-step="-1"]').disabled = i === 0;
      q('[data-drill-step="1"]').disabled = i === b.length - 1;
      fetch("/api/dashboard/drill?t=" + encodeURIComponent(x.t) + "&hours=" + hours, { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
        .then(function (d) { if (drillIdx === i) { renderDrill(d); drill.classList.remove("loading"); } })
        .catch(function () { drill.classList.remove("loading"); q("[data-drill-sub]").textContent = "Could not load the events of this period."; });
      if (dashChart) {
        dashChart.setActiveElements([{ datasetIndex: 1, index: i }]);
        dashChart.update("none");
      }
      var r = drill.getBoundingClientRect();
      if (r.top > window.innerHeight - 120) drill.scrollIntoView({ behavior: "smooth", block: "nearest" });
    };
    if (drill) {
      q("[data-drill-close]").addEventListener("click", function () { drill.hidden = true; drillIdx = -1; });
      drill.querySelectorAll("[data-drill-step]").forEach(function (btn) {
        btn.addEventListener("click", function () { openDrill(drillIdx + parseInt(btn.getAttribute("data-drill-step"), 10)); });
      });
      q("[data-drill-filter]").addEventListener("click", function (e) {
        var btn = e.target.closest(".fpill");
        if (!btn) return;
        drillFilter = btn.getAttribute("data-value");
        q("[data-drill-filter]").querySelectorAll(".fpill").forEach(function (x) { x.classList.toggle("active", x === btn); });
        renderEvents();
      });
    }

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
        if (drill) { drill.hidden = true; drillIdx = -1; }
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

// ---------------------------------------------------------------- export page: quick periods
(function () {
  var from = document.querySelector("[data-export-from]");
  var to = document.querySelector("[data-export-to]");
  if (!from || !to) return;
  var iso = function (d) {
    var m = d.getMonth() + 1, day = d.getDate();
    return d.getFullYear() + "-" + (m < 10 ? "0" : "") + m + "-" + (day < 10 ? "0" : "") + day;
  };
  document.querySelectorAll("[data-export-preset]").forEach(function (b) {
    b.addEventListener("click", function () {
      var now = new Date(), a = new Date(now), z = new Date(now);
      var p = b.getAttribute("data-export-preset");
      if (p === "yesterday") { a.setDate(a.getDate() - 1); z = new Date(a); }
      else if (p === "month") { a = new Date(now.getFullYear(), now.getMonth(), 1); }
      else if (p === "lastmonth") { a = new Date(now.getFullYear(), now.getMonth() - 1, 1); z = new Date(now.getFullYear(), now.getMonth(), 0); }
      else if (p === "year") { a = new Date(now.getFullYear(), 0, 1); }
      else if (p === "lastyear") { a = new Date(now.getFullYear() - 1, 0, 1); z = new Date(now.getFullYear() - 1, 11, 31); }
      else if (p !== "today") { a.setDate(a.getDate() - (parseInt(p, 10) - 1)); }
      from.value = iso(a);
      to.value = iso(z);
    });
  });
  from.addEventListener("change", function () { if (to.value && from.value > to.value) to.value = from.value; });
  to.addEventListener("change", function () { if (from.value && to.value < from.value) from.value = to.value; });
  // The download does not leave the page: show that it is being prepared.
  var btn = document.querySelector("[data-export-submit]");
  var form = btn && btn.form;
  if (form) form.addEventListener("submit", function () {
    btn.classList.add("busy");
    setTimeout(function () { btn.classList.remove("busy"); }, 8000);
  });
})();

// Network map: zoom with the buttons, the mouse wheel or a double click, and
// pan by dragging. Markers and labels keep their size on screen.
(function () {
  var MW = 1000, MH = 500, ASPECT = 1000 / 390, MINW = 8;
  document.querySelectorAll("[data-geo-zoom]").forEach(function (box) {
    var svg = box.querySelector("svg.geo-map");
    if (!svg) return;
    var v0 = svg.getAttribute("viewBox").split(/\s+/).map(Number);
    var view = v0.slice(), k0 = v0[2] / MW;
    var items = [], marks = [];
    svg.querySelectorAll(".geo-mk").forEach(function (g) {
      var c = g.querySelector("circle:not(.halo)") || g.querySelector("circle");
      if (!c) return;
      var cx = +c.getAttribute("cx"), cy = +c.getAttribute("cy");
      marks.push({ g: g, x: cx, y: cy, r: +c.getAttribute("r") / k0 });
      g.querySelectorAll("circle").forEach(function (el) {
        items.push({ el: el, r: +el.getAttribute("r") / k0 });
      });
      g.querySelectorAll("text").forEach(function (el) {
        items.push({ el: el, cx: cx, cy: cy,
          dx: (+el.getAttribute("x") - cx) / k0, dy: (+el.getAttribute("y") - cy) / k0,
          fs: +el.getAttribute("font-size") / k0 });
      });
    });
    function apply() {
      svg.setAttribute("viewBox", view.map(function (n) { return n.toFixed(2); }).join(" "));
      var k = view[2] / MW;
      items.forEach(function (it) {
        if (it.r !== undefined) { it.el.setAttribute("r", (it.r * k).toFixed(3)); return; }
        it.el.setAttribute("x", (it.cx + it.dx * k).toFixed(3));
        it.el.setAttribute("y", (it.cy + it.dy * k).toFixed(3));
        it.el.setAttribute("font-size", (it.fs * k).toFixed(3));
      });
      spread(k);
    }
    // spread fans out markers that would overlap on screen at this zoom (for
    // example a monitoring system and a target in the same city) around their
    // common centre; they come back to their place once there is room.
    function spread(k) {
      var n = marks.length, grp = [];
      for (var i = 0; i < n; i++) grp[i] = i;
      function root(i) { while (grp[i] !== i) i = grp[i] = grp[grp[i]]; return i; }
      for (i = 0; i < n; i++) for (var j = i + 1; j < n; j++) {
        var a = marks[i], b = marks[j];
        if (Math.hypot(a.x - b.x, a.y - b.y) < (a.r + b.r + 3) * k) grp[root(i)] = root(j);
      }
      var sets = {};
      for (i = 0; i < n; i++) (sets[root(i)] = sets[root(i)] || []).push(marks[i]);
      Object.keys(sets).forEach(function (key) {
        var s = sets[key];
        if (s.length === 1) { s[0].g.removeAttribute("transform"); return; }
        var mx = 0, my = 0, mr = 0;
        s.forEach(function (m) { mx += m.x / s.length; my += m.y / s.length; mr = Math.max(mr, m.r); });
        var ring = (mr + 2) * k / Math.sin(Math.PI / Math.max(s.length, 2));
        s.forEach(function (m, i) {
          var t = -Math.PI / 2 + 2 * Math.PI * i / s.length;
          m.g.setAttribute("transform", "translate(" + (mx + ring * Math.cos(t) - m.x).toFixed(3) + " " + (my + ring * Math.sin(t) - m.y).toFixed(3) + ")");
        });
      });
    }
    function clamp() {
      view[2] = Math.min(MW, Math.max(MINW, view[2]));
      view[3] = view[2] / ASPECT;
      view[0] = Math.min(MW - view[2], Math.max(0, view[0]));
      view[1] = Math.min(MH - view[3], Math.max(0, view[1]));
    }
    // zoom by f (<1 zooms in) around the map point (px, py).
    function zoom(f, px, py) {
      if (px === undefined) { px = view[0] + view[2] / 2; py = view[1] + view[3] / 2; }
      var w = Math.min(MW, Math.max(MINW, view[2] * f)), s = w / view[2];
      view = [px - (px - view[0]) * s, py - (py - view[1]) * s, w, w / ASPECT];
      clamp(); apply();
    }
    // toMap turns a mouse position into map coordinates.
    function toMap(e) {
      var r = svg.getBoundingClientRect(), sc = Math.max(view[2] / r.width, view[3] / r.height);
      return [view[0] + view[2] / 2 + (e.clientX - r.left - r.width / 2) * sc,
              view[1] + view[3] / 2 + (e.clientY - r.top - r.height / 2) * sc, sc];
    }
    box.querySelectorAll("[data-zoom]").forEach(function (b) {
      b.addEventListener("click", function () {
        var a = b.getAttribute("data-zoom");
        if (a === "in") zoom(1 / 1.6);
        else if (a === "out") zoom(1.6);
        else if (a === "fit") { view = v0.slice(); apply(); }
        else { view = [0, 22, MW, 390]; apply(); }
      });
    });
    apply();
    svg.addEventListener("wheel", function (e) {
      e.preventDefault();
      var p = toMap(e);
      zoom(Math.pow(1.0018, e.deltaY * (e.deltaMode === 1 ? 33 : 1)), p[0], p[1]);
    }, { passive: false });
    svg.addEventListener("dblclick", function (e) {
      var p = toMap(e);
      zoom(e.shiftKey ? 2 : 0.5, p[0], p[1]);
    });
    var drag = null;
    svg.addEventListener("pointerdown", function (e) {
      if (e.button !== 0) return;
      drag = { x: e.clientX, y: e.clientY, v: view.slice(), sc: toMap(e)[2] };
      svg.setPointerCapture(e.pointerId);
      box.classList.add("dragging");
    });
    svg.addEventListener("pointermove", function (e) {
      if (!drag) return;
      view[0] = drag.v[0] - (e.clientX - drag.x) * drag.sc;
      view[1] = drag.v[1] - (e.clientY - drag.y) * drag.sc;
      clamp(); apply();
    });
    function end() { drag = null; box.classList.remove("dragging"); }
    svg.addEventListener("pointerup", end);
    svg.addEventListener("pointercancel", end);
  });
})();
