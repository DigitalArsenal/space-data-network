/*
 * SpaceAware plans (spacedatanetwork.org/marketing.html): the audience level switch,
 * the plans table, the plan builder and the SpaceEdge section, all read from
 * marketing/plans.json, with module names from marketing/catalog.json. State lives in
 * the URL hash next to the inventory's keys (each script keeps the other's).
 */
(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const esc = (value) =>
    String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const money = (n) => "$" + Math.round(n).toLocaleString("en-US");
  const GROUP_ORDER = ["weather", "orbits", "solar", "developer"];
  const KEYS = { level: "executive", plan: "pro", g: "orbits", seats: "5", products: "1", bill: "month" };

  let plans = null;
  let catalog = null;
  let state = { ...KEYS };

  // The hash is shared with inventory.js: read and write only this script's keys.
  function readHash() {
    const params = new URLSearchParams(location.hash.slice(1));
    const next = { ...KEYS };
    for (const key of Object.keys(KEYS)) if (params.has(key)) next[key] = params.get(key);
    if (!plans.levels.some((l) => l.id === next.level)) next.level = KEYS.level;
    if (!plans.plans.some((p) => p.id === next.plan)) next.plan = KEYS.plan;
    next.seats = String(Math.max(1, Math.min(10000, Number.parseInt(next.seats, 10) || 1)));
    next.products = String(Math.max(1, Math.min(100, Number.parseInt(next.products, 10) || 1)));
    if (next.bill !== "year") next.bill = "month";
    return next;
  }
  function writeHash() {
    const params = new URLSearchParams(location.hash.slice(1));
    for (const [key, value] of Object.entries(state)) value === KEYS[key] ? params.delete(key) : params.set(key, value);
    const hash = params.toString();
    history.replaceState(null, "", hash ? `#${hash}` : location.pathname + location.search);
  }

  const group = (id) => plans.groups.find((g) => g.id === id);
  const plan = (id) => plans.plans.find((p) => p.id === id);
  const selected = () => state.g.split(",").filter((id) => GROUP_ORDER.includes(id));
  const copy = (key) => plans.copy[key]?.[state.level] ?? "";

  /** Module names a group holds: its own list, or the catalog's modules by category and id. */
  function groupModules(g) {
    if (g.includes) return g.includes;
    const ids = new Set(g.modules ?? []);
    const cats = new Set(g.categories ?? []);
    return (catalog?.modules ?? []).filter((m) => ids.has(m.id) || cats.has(m.category)).map((m) => m.name);
  }

  // ----- Level switch and section copy ---------------------------------------------------

  function renderCaps() {
    $("[data-plans=caps]").innerHTML = plans.capabilities.map((c) =>
      `<article class="pl-cap"><h3>${esc(c.name)}</h3><span class="pl-tag">${esc(c.group)}</span><p>${esc(c[state.level])}</p></article>`).join("");
  }

  function renderLevel() {
    const box = $("[data-plans=levels]");
    box.innerHTML = plans.levels.map((l) =>
      `<button type="button" role="radio" aria-checked="${l.id === state.level}" data-level="${l.id}">${esc(l.label)}</button>`).join("");
    for (const el of document.querySelectorAll("[data-copy]")) el.textContent = copy(el.dataset.copy);
    renderCaps();
  }

  // ----- Plans table ---------------------------------------------------------------------

  function cell(p, gid) {
    if (p.id === "spaceedge") return gid === "suite" ? esc(p.priceLabel) : "In the program";
    const price = p.prices[gid];
    if (gid === "developer") return price == null ? "—" : `${money(price)}<small>${esc(p.developerBasis ?? p.basis)}</small>`;
    if (price == null) return "—";
    if (price === 0) return "Free";
    if (gid === "suite" && p.suiteTo) return `${money(price)}–${money(p.suiteTo)}+`;
    return `${p.from ? "from " : ""}${money(price)}`;
  }

  function renderTable() {
    const cols = [...GROUP_ORDER.map((id) => ({ id, name: group(id).name })), { id: "suite", name: plans.suite.name }];
    const head = `<tr><th scope="col">Plan</th>${cols.map((c) => `<th scope="col">${esc(c.name)}</th>`).join("")}<th scope="col">Adds</th></tr>`;
    const rows = plans.plans.map((p) => `<tr class="${p.id === "spaceedge" ? "pl-edge" : ""}">
      <th scope="row"><b>${esc(p.name)}</b><small>${esc(p.who)}${p.basis ? " · " + esc(p.basis) : ""}</small>${p.state ? `<span class="pl-chip">${esc(p.state)}</span>` : ""}</th>
      ${cols.map((c) => `<td>${cell(p, c.id)}</td>`).join("")}
      <td class="pl-adds">${p.adds.map(esc).join("<br>")}</td></tr>`).join("");
    $("[data-plans=table]").innerHTML = `<table class="pl-table"><thead>${head}</thead><tbody>${rows}</tbody></table>`;
    $("[data-plans=status]").textContent = plans.statusLabel;
    $("[data-plans=groups]").innerHTML = plans.groups.map((g) => `<div class="pl-group"><h3>${esc(g.name)}</h3><p>${esc(g.who)}</p>
      <ul>${groupModules(g).map((n) => `<li>${esc(n)}</li>`).join("")}</ul>${g.link ? `<a href="${esc(g.link)}" target="_blank" rel="noopener">Open the live weather</a>` : ""}</div>`).join("");
  }

  // ----- Plan builder --------------------------------------------------------------------

  /** The list price of a configuration: { total, unit, lines[], note, saving }. */
  function quote() {
    const p = plan(state.plan);
    const g = selected();
    const core = g.filter((id) => plans.suite.groups.includes(id));
    const allCore = plans.suite.groups.every((id) => core.includes(id));
    const seats = Number(state.seats), products = Number(state.products);
    const lines = [];
    let total = 0, unit = "", note = "", saving = null;

    if (p.id === "spaceedge") {
      return { total: null, text: p.priceLabel, unit: p.basis, lines: [p.fees, ...p.routes.map((r) => `Contract route: ${r}`)], note: "", saving: null };
    }
    if (p.id === "free") return { total: 0, text: "Free", unit: "", lines: core.map((id) => `${group(id).name}: view the open data`), note: "", saving: null };

    const sum = core.reduce((s, id) => s + (p.prices[id] ?? 0), 0);
    const suite = p.prices.suite;
    if (allCore && suite != null) {
      total = suite;
      lines.push(`${plans.suite.name}: ${money(suite)}${p.suiteTo ? `–${money(p.suiteTo)}+` : ""}`);
      saving = sum - suite;
    } else {
      for (const id of core) lines.push(`${group(id).name}: ${money(p.prices[id])}`);
      total = sum;
      if (core.length && suite != null && suite <= sum) note = `The ${plans.suite.name.toLowerCase()} costs ${money(suite)} and adds every group.`;
    }

    if (p.id === "pro") {
      unit = state.bill === "year" ? "per year" : "per month";
      if (state.bill === "year") { total *= p.annualMonths; if (saving != null) saving *= p.annualMonths; lines.push(`Yearly: ${p.annualMonths} months' price for 12`); }
    } else if (p.id === "team") {
      total *= seats;
      if (saving != null) saving *= seats;
      unit = "per year";
      lines.push(`${seats} seat${seats === 1 ? "" : "s"}`);
      if (g.includes("developer")) {
        const dev = p.prices.developer * products;
        total += dev;
        lines.push(`Developer: ${money(p.prices.developer)} × ${products} product${products === 1 ? "" : "s"} shipped`);
      }
    } else if (p.id === "enterprise") {
      unit = "per year, site license";
      if (g.includes("developer")) lines.push("Developer: priced with the site license");
    }
    const from = p.from ? "from " : "";
    return { total, text: core.length || g.includes("developer") ? from + money(total) : "Choose a group", unit, lines, note, saving: saving > 0 ? saving : null };
  }

  function renderBuilder() {
    const p = plan(state.plan);
    const g = selected();
    $("[data-plans=plan]").innerHTML = plans.plans.map((x) =>
      `<button type="button" role="radio" aria-checked="${x.id === state.plan}" data-plan="${x.id}">${esc(x.name)}</button>`).join("");
    const groupIds = p.id === "team" || p.id === "enterprise" ? GROUP_ORDER : plans.suite.groups;
    $("[data-plans=pick]").innerHTML = p.id === "spaceedge" ? `<p class="pl-muted">Every group, plus Defense Operations.</p>` :
      groupIds.map((id) => `<label class="pl-check"><input type="checkbox" id="pl-g-${id}" data-group="${id}" ${g.includes(id) ? "checked" : ""}>
        <span>${esc(group(id).name)}</span></label>`).join("") +
      `<button type="button" class="btn btn-small" data-act="suite">${esc(plans.suite.name)}</button>`;
    const showSeats = p.id === "team", showProducts = p.id === "team" && g.includes("developer"), showBill = p.id === "pro";
    $("[data-plans=opts]").innerHTML =
      (showSeats ? `<label class="pl-num"><span>Seats</span><input type="number" id="pl-seats" min="1" max="10000" value="${esc(state.seats)}" data-num="seats"></label>` : "") +
      (showProducts ? `<label class="pl-num"><span>Products shipped</span><input type="number" id="pl-products" min="1" max="100" value="${esc(state.products)}" data-num="products"></label>` : "") +
      (showBill ? `<div class="pl-seg" role="radiogroup" aria-label="Billing">${["month", "year"].map((b) =>
        `<button type="button" role="radio" aria-checked="${state.bill === b}" data-bill="${b}">${b === "month" ? "Monthly" : "Yearly"}</button>`).join("")}</div>` : "");

    const q = quote();
    const includes = (p.id === "spaceedge" ? GROUP_ORDER : g).filter((id) => group(id)).map((id) =>
      `<details><summary>${esc(group(id).name)}</summary><ul>${groupModules(group(id)).map((n) => `<li>${esc(n)}</li>`).join("")}</ul></details>`).join("");
    $("[data-plans=quote]").innerHTML = `
      <div class="pl-price"><b>${esc(q.text)}</b><span>${esc(q.unit)}</span></div>
      ${q.saving ? `<p class="pl-save">The suite saves ${money(q.saving)} against the groups bought separately.</p>` : ""}
      ${q.note ? `<p class="pl-save">${esc(q.note)}</p>` : ""}
      <ul class="pl-lines">${q.lines.filter(Boolean).map((l) => `<li>${esc(l)}</li>`).join("")}</ul>
      <h4>Includes</h4><ul class="pl-lines">${p.adds.map((a) => `<li>${esc(a)}</li>`).join("")}</ul>
      ${includes}
      <button type="button" class="btn btn-small" data-act="udl" data-udl-plan="${p.id}">UDL data in ${esc(p.name)}</button>`;
  }

  // ----- SpaceEdge ----------------------------------------------------------------------

  // The wordmark: the first letter of each word full height, the rest small capitals (plans.css .se-wordmark)
  const wordmark = (name) => name.split(/(?=[A-Z])/).map((w) => `${esc(w[0])}<small>${esc(w.slice(1).toUpperCase())}</small>`).join("");
  function renderEdge() {
    const p = plan("spaceedge");
    $("[data-plans=edge]").innerHTML = `
      <div class="pl-edge-mark"><img class="se-mark-dark" src="${esc(plans.mark.dark)}" alt="" width="64" height="64">
        <img class="se-mark-light" src="${esc(plans.mark.light)}" alt="" width="64" height="64">
        <b class="se-wordmark" aria-label="${esc(plans.governmentName)}">${wordmark(plans.governmentName)}</b></div>
      <div class="pl-edge-cols">
        <div><h3>Adds</h3><ul>${p.adds.map((a) => `<li>${esc(a)}</li>`).join("")}</ul></div>
        <div><h3>Price</h3><p class="pl-price-sm">${esc(p.priceLabel)} <span>${esc(p.basis)}</span></p>
          <p class="pl-muted">${esc(p.fees)}</p><h3>Contract routes</h3><ul>${p.routes.map((r) => `<li>${esc(r)}</li>`).join("")}</ul></div>
      </div>`;
    $("[data-plans=signin]").innerHTML = plans.signinSteps.map((s, i) =>
      `<li><span class="pl-step">${i + 1}</span><div><b>${esc(s.title)}</b><p>${esc(s.text)}</p></div></li>`).join("");
  }


  function render() {
    renderLevel();
    renderBuilder();
    writeHash();
    document.dispatchEvent(new CustomEvent("plans:level", { detail: state.level }));
  }

  function wire() {
    $("[data-plans=levels]").addEventListener("click", (e) => {
      const level = e.target.closest("[data-level]")?.dataset.level;
      if (level) { state.level = level; render(); }
    });
    const builder = $("[data-plans=builder]");
    builder.addEventListener("click", (e) => {
      const t = e.target.closest("[data-plan],[data-bill],[data-act]");
      if (!t) return;
      if (t.dataset.plan) state.plan = t.dataset.plan;
      else if (t.dataset.bill) state.bill = t.dataset.bill;
      else if (t.dataset.act === "suite") state.g = [...plans.suite.groups, ...selected().filter((id) => !plans.suite.groups.includes(id))].join(",");
      else if (t.dataset.act === "udl") {
        document.dispatchEvent(new CustomEvent("udl:plan", { detail: t.dataset.udlPlan }));
        $("#udl")?.scrollIntoView({ behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
        return;
      }
      render();
    });
    builder.addEventListener("change", (e) => {
      const id = e.target.dataset?.group;
      if (id) {
        const g = new Set(selected());
        e.target.checked ? g.add(id) : g.delete(id);
        state.g = GROUP_ORDER.filter((x) => g.has(x)).join(",");
        render();
      }
    });
    builder.addEventListener("input", (e) => {
      const key = e.target.dataset?.num;
      if (!key) return;
      const n = Number.parseInt(e.target.value, 10);
      if (!Number.isFinite(n) || n < 1) return;
      state[key] = String(n);
      const q = quote();
      $("[data-plans=quote] .pl-price b").textContent = q.text;
      writeHash();
    });
    builder.addEventListener("focusout", (e) => { if (e.target.dataset?.num) render(); });
    for (const b of document.querySelectorAll("[data-goto]")) b.addEventListener("click", () =>
      $(`#${b.dataset.goto}`)?.scrollIntoView({ behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" }));
  }

  Promise.all([
    fetch("marketing/plans.json", { cache: "no-cache" }).then((r) => r.json()),
    fetch("marketing/catalog.json", { cache: "no-cache" }).then((r) => r.json()).catch(() => null),
  ]).then(([p, c]) => {
    plans = p;
    catalog = c;
    state = readHash();
    renderTable();
    renderEdge();
    wire();
    render();
  }).catch(() => {
    $("[data-plans=table]").innerHTML = `<p class="inv-empty">Plans did not load. Reload the page.</p>`;
  });
})();
