/*
 * SpaceAware module inventory (spacedatanetwork.org/marketing.html).
 * A working reference for the marketing team: every module, bundle, console
 * card and left-out item from marketing/catalog.json, searchable, filterable,
 * sortable and exportable. The JSON is the single source; this file only reads it.
 */
(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const esc = (value) =>
    String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const norm = (value) => String(value ?? "").toLowerCase();

  const VIEWS = [
    { id: "modules", label: "Modules" },
    { id: "matrix", label: "Bundle matrix" },
    { id: "bundles", label: "Bundles" },
    { id: "cards", label: "Console cards" },
    { id: "excluded", label: "Not listed" },
  ];
  const COLUMNS = [
    { id: "name", label: "Module" },
    { id: "category", label: "Category" },
    { id: "access", label: "Access" },
    { id: "delivery", label: "Delivery" },
    { id: "bundles", label: "Bundles" },
    { id: "standards", label: "Standards" },
  ];
  const DEFAULT = { view: "modules", q: "", category: "", access: "", delivery: "", bundle: "", standard: "", group: "", sort: "name", dir: "asc" };

  let catalog = null;
  let state = { ...DEFAULT };
  const open = new Set();

  function readHash() {
    const params = new URLSearchParams(location.hash.slice(1));
    const next = { ...DEFAULT };
    for (const key of Object.keys(DEFAULT)) if (params.has(key)) next[key] = params.get(key);
    if (!VIEWS.some((v) => v.id === next.view)) next.view = DEFAULT.view;
    return next;
  }
  // The hash is shared with plans.js (level, plan builder): keep its keys, write only ours.
  function writeHash() {
    const params = new URLSearchParams(location.hash.slice(1));
    for (const [key, value] of Object.entries(state)) value !== DEFAULT[key] ? params.set(key, value) : params.delete(key);
    const hash = params.toString();
    history.replaceState(null, "", hash ? `#${hash}` : location.pathname + location.search);
  }

  // Lookups built once from the catalog.
  let byCategory, bundlesOf, coveredBy, excludedReason, allStandards;
  function index() {
    byCategory = new Map(catalog.categories.map((c) => [c.id, c]));
    bundlesOf = new Map(catalog.modules.map((m) => [m.id, []]));
    for (const b of catalog.bundles) for (const id of b.modules) bundlesOf.get(id)?.push(b);
    coveredBy = new Map();
    for (const m of catalog.modules) for (const key of m.covers ?? []) {
      if (!coveredBy.has(key)) coveredBy.set(key, []);
      coveredBy.get(key).push(m);
    }
    excludedReason = new Map((catalog.notListed ?? []).map((n) => [n.key, n.reason]));
    allStandards = [...new Set(catalog.modules.flatMap((m) => m.standards ?? []))].sort((a, b) => a.localeCompare(b));
  }

  const categoryName = (id) => byCategory.get(id)?.name ?? id;
  const accessName = (id) => catalog.access[id] ?? id;
  const deliveryName = (id) => catalog.delivery[id] ?? id;

  function haystack(m) {
    return norm([
      m.id, m.name, m.tagline, m.summary, ...(m.capabilities ?? []), ...(m.standards ?? []),
      categoryName(m.category), accessName(m.access), deliveryName(m.delivery),
      ...(bundlesOf.get(m.id) ?? []).map((b) => b.name), ...(m.covers ?? []), m.evidence,
    ].join(" "));
  }

  function filteredModules() {
    const terms = norm(state.q).split(/\s+/).filter(Boolean);
    const list = catalog.modules.filter((m) => {
      if (state.category && m.category !== state.category) return false;
      if (state.access && m.access !== state.access) return false;
      if (state.delivery && m.delivery !== state.delivery) return false;
      if (state.bundle && !(bundlesOf.get(m.id) ?? []).some((b) => b.id === state.bundle)) return false;
      if (state.standard && !(m.standards ?? []).includes(state.standard)) return false;
      if (terms.length) {
        const text = haystack(m);
        if (!terms.every((t) => text.includes(t))) return false;
      }
      return true;
    });
    const key = (m) => {
      switch (state.sort) {
        case "category": return categoryName(m.category);
        case "access": return accessName(m.access);
        case "delivery": return deliveryName(m.delivery);
        case "bundles": return (bundlesOf.get(m.id) ?? []).length;
        case "standards": return (m.standards ?? []).length;
        default: return m.name;
      }
    };
    const sign = state.dir === "desc" ? -1 : 1;
    list.sort((a, b) => {
      const ka = key(a), kb = key(b);
      const c = typeof ka === "number" ? ka - kb : String(ka).localeCompare(String(kb));
      return c !== 0 ? c * sign : a.name.localeCompare(b.name);
    });
    return list;
  }

  function groupKey(m) {
    switch (state.group) {
      case "category": return [categoryName(m.category)];
      case "access": return [accessName(m.access)];
      case "delivery": return [deliveryName(m.delivery)];
      case "bundle": {
        const names = (bundlesOf.get(m.id) ?? []).map((b) => b.name);
        return names.length ? names : ["No bundle"];
      }
      default: return [""];
    }
  }

  // ---- Toolbar ----------------------------------------------------------
  function option(value, label, current) {
    return `<option value="${esc(value)}"${value === current ? " selected" : ""}>${esc(label)}</option>`;
  }
  function renderToolbar() {
    const t = $("[data-inv=toolbar]");
    t.innerHTML = `
      <label class="inv-search"><span class="sr">Search</span>
        <input type="search" data-key="q" placeholder="Search names, capabilities, standards, evidence" value="${esc(state.q)}" autocomplete="off" spellcheck="false">
      </label>
      <div class="inv-selects">
        <label><span>Category</span><select data-key="category">${option("", "All", state.category)}${catalog.categories.map((c) => option(c.id, c.name, state.category)).join("")}</select></label>
        <label><span>Access</span><select data-key="access">${option("", "All", state.access)}${Object.entries(catalog.access).map(([k, v]) => option(k, v, state.access)).join("")}</select></label>
        <label><span>Delivery</span><select data-key="delivery">${option("", "All", state.delivery)}${Object.entries(catalog.delivery).map(([k, v]) => option(k, v, state.delivery)).join("")}</select></label>
        <label><span>Bundle</span><select data-key="bundle">${option("", "All", state.bundle)}${catalog.bundles.map((b) => option(b.id, b.name, state.bundle)).join("")}</select></label>
        <label><span>Standard</span><select data-key="standard">${option("", "All", state.standard)}${allStandards.map((s) => option(s, s, state.standard)).join("")}</select></label>
        <label><span>Group by</span><select data-key="group">${option("", "None", state.group)}${option("category", "Category", state.group)}${option("access", "Access", state.group)}${option("delivery", "Delivery", state.group)}${option("bundle", "Bundle", state.group)}</select></label>
      </div>
      <div class="inv-actions">
        <button type="button" class="btn btn-small" data-act="csv">Export CSV</button>
        <a class="btn btn-small" href="marketing/catalog.json" download>Download JSON</a>
        <button type="button" class="btn btn-small" data-act="expand">Expand all</button>
        <button type="button" class="btn btn-small" data-act="reset">Reset</button>
      </div>`;
  }

  function renderTabs() {
    $("[data-inv=tabs]").innerHTML = VIEWS.map((v) =>
      `<button type="button" role="tab" data-view="${v.id}" aria-selected="${v.id === state.view}">${esc(v.label)}<small data-count="${v.id}"></small></button>`).join("");
  }

  // ---- Summary -------------------------------------------------------------
  function renderSummary() {
    const mods = catalog.modules;
    const count = (pred) => mods.filter(pred).length;
    const stat = (n, label) => `<div class="inv-stat"><b>${n}</b><span>${esc(label)}</span></div>`;
    const accessStats = Object.entries(catalog.access).map(([k, v]) => stat(count((m) => m.access === k), v)).join("");
    $("[data-inv=summary]").innerHTML =
      stat(mods.length, "Modules") + stat(catalog.bundles.length, "Bundles") + stat(catalog.categories.length, "Categories") + accessStats +
      stat((catalog.notListed ?? []).length, "Not listed");
    const priced = catalog.pricing?.approvedPriceList ? "Approved price list on file" : "No approved prices yet: every bundle is “request pricing”";
    $("[data-inv=meta]").innerHTML =
      `Catalog version ${esc(catalog.version)} &middot; updated ${esc(catalog.updated)} &middot; reviewed against console build ${esc(catalog.consoleBuild)} &middot; ${esc(priced)}`;
  }

  // ---- Modules view -----------------------------------------------------------
  function chip(text, cls = "") {
    return `<span class="inv-chip ${cls}">${esc(text)}</span>`;
  }
  function moduleRow(m) {
    const bundles = bundlesOf.get(m.id) ?? [];
    const isOpen = open.has(m.id);
    const detail = isOpen ? `
      <tr class="inv-detail"><td colspan="${COLUMNS.length}">
        <div class="inv-detail-grid">
          <div>
            <h4>Summary</h4><p>${esc(m.summary)}</p>
            <h4>Capabilities</h4><ul>${(m.capabilities ?? []).map((c) => `<li>${esc(c)}</li>`).join("")}</ul>
          </div>
          <div>
            <h4>Bundles</h4><p>${bundles.map((b) => chip(b.name)).join(" ") || "None"}</p>
            <h4>Standards</h4><p>${(m.standards ?? []).map((s) => chip(s)).join(" ") || "None"}</p>
            ${(m.covers ?? []).length ? `<h4>Console cards</h4><p>${m.covers.map((c) => chip(c, "mono")).join(" ")}</p>` : ""}
            ${m.learnMore ? `<h4>Learn more</h4><p><a href="${esc(m.learnMore)}" target="_blank" rel="noopener">${esc(m.learnMore)}</a></p>` : ""}
            <h4>Evidence</h4><p class="inv-evidence">${esc(m.evidence)}</p>
            <p class="inv-id">id: <code>${esc(m.id)}</code></p>
          </div>
        </div>
      </td></tr>` : "";
    return `
      <tr class="inv-row${isOpen ? " open" : ""}" data-id="${esc(m.id)}" tabindex="0" aria-expanded="${isOpen}">
        <td data-col="Module"><span class="inv-name">${esc(m.name)}</span><span class="inv-tag">${esc(m.tagline)}</span></td>
        <td data-col="Category">${esc(categoryName(m.category))}</td>
        <td data-col="Access">${chip(accessName(m.access), `acc-${m.access}`)}</td>
        <td data-col="Delivery">${esc(deliveryName(m.delivery))}</td>
        <td data-col="Bundles"><span class="inv-num">${bundles.length}</span></td>
        <td data-col="Standards">${(m.standards ?? []).map((s) => chip(s)).join(" ")}</td>
      </tr>${detail}`;
  }
  function renderModules(list) {
    const head = COLUMNS.map((c) => {
      const active = state.sort === c.id;
      const arrow = active ? (state.dir === "asc" ? " ↑" : " ↓") : "";
      return `<th scope="col" aria-sort="${active ? (state.dir === "asc" ? "ascending" : "descending") : "none"}"><button type="button" data-sort="${c.id}">${esc(c.label)}${arrow}</button></th>`;
    }).join("");
    let body = "";
    if (!state.group) body = list.map(moduleRow).join("");
    else {
      const groups = new Map();
      for (const m of list) for (const g of groupKey(m)) {
        if (!groups.has(g)) groups.set(g, []);
        groups.get(g).push(m);
      }
      const names = [...groups.keys()].sort((a, b) => a.localeCompare(b));
      body = names.map((g) => `<tr class="inv-group"><th colspan="${COLUMNS.length}" scope="rowgroup">${esc(g)} <small>${groups.get(g).length}</small></th></tr>${groups.get(g).map(moduleRow).join("")}`).join("");
    }
    return list.length
      ? `<table class="inv-table inv-modules"><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table>`
      : `<p class="inv-empty">No modules match these filters.</p>`;
  }

  // ---- Bundle matrix ------------------------------------------------------------
  function renderMatrix(list) {
    const bundles = state.bundle ? catalog.bundles.filter((b) => b.id === state.bundle) : catalog.bundles;
    const head = `<th scope="col" class="inv-mx-mod">Module</th>` + bundles.map((b) => `<th scope="col" title="${esc(b.summary)}"><span>${esc(b.name)}</span></th>`).join("");
    const rows = list.map((m) => {
      const ids = new Set((bundlesOf.get(m.id) ?? []).map((b) => b.id));
      return `<tr><th scope="row" class="inv-mx-mod">${esc(m.name)}<small>${esc(categoryName(m.category))} &middot; ${esc(accessName(m.access))}</small></th>${bundles.map((b) => `<td class="${ids.has(b.id) ? "in" : ""}">${ids.has(b.id) ? "●" : ""}</td>`).join("")}</tr>`;
    }).join("");
    const totals = `<tr class="inv-mx-total"><th scope="row">In this view</th>${bundles.map((b) => `<td>${list.filter((m) => b.modules.includes(m.id)).length}</td>`).join("")}</tr>`;
    return list.length
      ? `<div class="inv-scroll"><table class="inv-table inv-matrix"><thead><tr>${head}</tr></thead><tbody>${rows}${totals}</tbody></table></div>`
      : `<p class="inv-empty">No modules match these filters.</p>`;
  }

  // ---- Bundles ----------------------------------------------------------------
  function renderBundles() {
    const terms = norm(state.q).split(/\s+/).filter(Boolean);
    const list = catalog.bundles.filter((b) => {
      if (state.bundle && b.id !== state.bundle) return false;
      const text = norm([b.name, b.tagline, b.summary, b.audience, ...b.modules].join(" "));
      return terms.every((t) => text.includes(t));
    });
    if (!list.length) return `<p class="inv-empty">No bundles match.</p>`;
    const mods = new Map(catalog.modules.map((m) => [m.id, m]));
    return `<div class="inv-bundles">${list.map((b) => {
      const members = b.modules.map((id) => mods.get(id)).filter(Boolean);
      const byAccess = Object.keys(catalog.access).map((k) => [k, members.filter((m) => m.access === k).length]).filter(([, n]) => n);
      const price = b.price != null ? esc(b.price) : "Request pricing";
      return `<article class="inv-bundle">
        <header><h3>${esc(b.name)}</h3><span class="inv-num">${members.length} modules</span></header>
        <p class="inv-tag">${esc(b.tagline)}</p>
        <p>${esc(b.summary)}</p>
        <dl><dt>Audience</dt><dd>${esc(b.audience)}</dd><dt>Price</dt><dd>${price}</dd><dt>Access mix</dt><dd>${byAccess.map(([k, n]) => `${n} ${esc(accessName(k).toLowerCase())}`).join(" &middot; ")}</dd></dl>
        <ul class="inv-members">${members.map((m) => `<li><button type="button" data-jump="${esc(m.id)}">${esc(m.name)}</button>${chip(accessName(m.access), `acc-${m.access}`)}</li>`).join("")}</ul>
      </article>`;
    }).join("")}</div>`;
  }

  // ---- Console cards ----------------------------------------------------------
  function renderCards() {
    const terms = norm(state.q).split(/\s+/).filter(Boolean);
    const rows = (catalog.consoleCards ?? []).map((key) => {
      const mods = coveredBy.get(key) ?? [];
      const mode = key.split(":")[0];
      const reason = excludedReason.get(key) ?? excludedReason.get(mode) ?? "";
      return { key, mode, mods, reason };
    }).filter((r) => {
      if (state.bundle && !r.mods.some((m) => (bundlesOf.get(m.id) ?? []).some((b) => b.id === state.bundle))) return false;
      const text = norm([r.key, r.reason, ...r.mods.map((m) => m.name)].join(" "));
      return terms.every((t) => text.includes(t));
    });
    if (!rows.length) return `<p class="inv-empty">No console cards match.</p>`;
    return `<table class="inv-table inv-cards"><thead><tr><th scope="col">Mode</th><th scope="col">Card</th><th scope="col">Marketed as</th><th scope="col">Note</th></tr></thead><tbody>${rows.map((r) => `
      <tr><td data-col="Mode"><code>${esc(r.mode)}</code></td><td data-col="Card">${esc(r.key.slice(r.mode.length + 1) || r.key)}</td>
      <td data-col="Marketed as">${r.mods.map((m) => `<button type="button" class="inv-link" data-jump="${esc(m.id)}">${esc(m.name)}</button>`).join(", ") || (r.reason ? "—" : "<em>Not covered</em>")}</td>
      <td data-col="Note">${esc(r.reason)}</td></tr>`).join("")}</tbody></table>`;
  }

  // ---- Not listed ---------------------------------------------------------------
  function renderExcluded() {
    const terms = norm(state.q).split(/\s+/).filter(Boolean);
    const rows = (catalog.notListed ?? []).filter((n) => terms.every((t) => norm(`${n.key} ${n.reason}`).includes(t)));
    if (!rows.length) return `<p class="inv-empty">Nothing matches.</p>`;
    return `<p class="inv-note">Capabilities that exist but are deliberately left out of the catalog, with the reason. Revisit these when the reason changes.</p>
      <table class="inv-table inv-excluded"><thead><tr><th scope="col">Item</th><th scope="col">Reason</th></tr></thead><tbody>${rows.map((n) => `<tr><td data-col="Item"><code>${esc(n.key)}</code></td><td data-col="Reason">${esc(n.reason)}</td></tr>`).join("")}</tbody></table>`;
  }

  // ---- Render loop ------------------------------------------------------------
  function render() {
    const list = filteredModules();
    const counts = {
      modules: list.length,
      matrix: list.length,
      bundles: catalog.bundles.length,
      cards: (catalog.consoleCards ?? []).length,
      excluded: (catalog.notListed ?? []).length,
    };
    for (const tab of document.querySelectorAll("[data-inv=tabs] [role=tab]")) {
      tab.setAttribute("aria-selected", String(tab.dataset.view === state.view));
      const small = tab.querySelector("small");
      if (small) small.textContent = String(counts[tab.dataset.view] ?? "");
    }
    const view = $("[data-inv=view]");
    view.innerHTML =
      state.view === "matrix" ? renderMatrix(list)
      : state.view === "bundles" ? renderBundles()
      : state.view === "cards" ? renderCards()
      : state.view === "excluded" ? renderExcluded()
      : renderModules(list);
    const filtersApply = state.view === "modules" || state.view === "matrix";
    document.body.classList.toggle("inv-module-filters-off", !filtersApply);
    $("[data-inv=result]").textContent = filtersApply ? `${list.length} of ${catalog.modules.length} modules` : "";
    writeHash();
  }

  function toCsv(list) {
    const cell = (v) => `"${String(v ?? "").replace(/"/g, '""')}"`;
    const header = ["id", "name", "category", "access", "delivery", "bundles", "standards", "tagline", "summary", "capabilities", "console cards", "learn more", "evidence"];
    const rows = list.map((m) => [
      m.id, m.name, categoryName(m.category), accessName(m.access), deliveryName(m.delivery),
      (bundlesOf.get(m.id) ?? []).map((b) => b.name).join("; "), (m.standards ?? []).join("; "),
      m.tagline, m.summary, (m.capabilities ?? []).join("; "), (m.covers ?? []).join("; "), m.learnMore ?? "", m.evidence ?? "",
    ].map(cell).join(","));
    return [header.map(cell).join(","), ...rows].join("\r\n");
  }

  function wire() {
    const toolbar = $("[data-inv=toolbar]");
    toolbar.addEventListener("input", (e) => {
      const key = e.target.dataset?.key;
      if (!key) return;
      state[key] = e.target.value;
      render();
    });
    toolbar.addEventListener("click", (e) => {
      const act = e.target.closest("[data-act]")?.dataset.act;
      if (act === "reset") {
        state = { ...DEFAULT, view: state.view };
        open.clear();
        renderToolbar();
        render();
      } else if (act === "expand") {
        const list = filteredModules();
        const all = list.every((m) => open.has(m.id));
        for (const m of list) all ? open.delete(m.id) : open.add(m.id);
        if (state.view !== "modules") state.view = "modules";
        render();
      } else if (act === "csv") {
        const blob = new Blob([toCsv(filteredModules())], { type: "text/csv" });
        const a = document.createElement("a");
        a.href = URL.createObjectURL(blob);
        a.download = `spaceaware-modules-${catalog.updated}.csv`;
        a.click();
        setTimeout(() => URL.revokeObjectURL(a.href), 1000);
      }
    });
    $("[data-inv=tabs]").addEventListener("click", (e) => {
      const view = e.target.closest("[data-view]")?.dataset.view;
      if (!view) return;
      state.view = view;
      render();
    });
    const view = $("[data-inv=view]");
    view.addEventListener("click", (e) => {
      const sort = e.target.closest("[data-sort]")?.dataset.sort;
      if (sort) {
        state.dir = state.sort === sort && state.dir === "asc" ? "desc" : "asc";
        state.sort = sort;
        render();
        return;
      }
      const jump = e.target.closest("[data-jump]")?.dataset.jump;
      if (jump) {
        state = { ...DEFAULT, view: "modules", q: "" };
        open.clear();
        open.add(jump);
        renderToolbar();
        render();
        document.querySelector(`.inv-row[data-id="${CSS.escape(jump)}"]`)?.scrollIntoView({ block: "center" });
        return;
      }
      if (e.target.closest("a, button, .inv-detail")) return;
      const row = e.target.closest(".inv-row");
      if (!row) return;
      const id = row.dataset.id;
      open.has(id) ? open.delete(id) : open.add(id);
      render();
    });
    view.addEventListener("keydown", (e) => {
      if (e.key !== "Enter" && e.key !== " ") return;
      const row = e.target.closest?.(".inv-row");
      if (!row || e.target !== row) return;
      e.preventDefault();
      const id = row.dataset.id;
      open.has(id) ? open.delete(id) : open.add(id);
      render();
      document.querySelector(`.inv-row[data-id="${CSS.escape(id)}"]`)?.focus();
    });
    addEventListener("hashchange", () => {
      state = readHash();
      renderToolbar();
      render();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "/" && document.activeElement?.tagName !== "INPUT") {
        e.preventDefault();
        $("[data-key=q]")?.focus();
      }
    });
  }

  async function start() {
    try {
      const response = await fetch("marketing/catalog.json", { cache: "no-cache" });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      catalog = await response.json();
    } catch (error) {
      $("[data-inv=view]").innerHTML = `<p class="inv-empty">The inventory could not load marketing/catalog.json (${esc(error.message)}).</p>`;
      return;
    }
    index();
    state = readHash();
    renderSummary();
    renderToolbar();
    renderTabs();
    wire();
    render();
  }

  start();
})();
