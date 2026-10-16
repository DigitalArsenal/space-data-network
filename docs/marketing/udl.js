/*
 * The UDL data matrix on spacedatanetwork.org/marketing.html: every Unified Data
 * Library data type, its Space Data Standards record, the module group that uses it
 * and what each plan gets. Read from marketing/udl-matrix.json; the counts are
 * computed from the rows, never typed in.
 */
(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const esc = (value) =>
    String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const PLANS = [
    { id: "free", name: "Free" }, { id: "pro", name: "Pro" }, { id: "team", name: "Team" },
    { id: "enterprise", name: "Enterprise" }, { id: "spaceedge", name: "SpaceEdge" },
  ];
  const GROUPS = ["All groups", "Orbits", "Weather", "Solar System", "Defense Ops"];
  const CHIP = { UDL: "udl", Open: "open", View: "view", Derived: "derived", Partial: "partial", Own: "own" };
  const LEGEND = [["UDL", "Live from UDL"], ["Open", "Open public source"], ["View", "Open source, view only"],
    ["Derived", "Computed from open data"], ["Partial", "Open source covers part"], ["Own", "The customer's own data"], ["—", "Not available"]];

  let data = null;
  const f = { group: "All groups", q: "", plan: "", only: "" };

  const types = (rows) => rows.reduce((n, r) => n + r.udl.length, 0);
  const openTiers = ["free", "pro", "team", "enterprise"];
  const edgeOnly = (r) => openTiers.every((t) => r.plans[t] === "—");
  const publicEquivalent = (r) => r.plans.pro !== "—" || r.plans.team !== "—";
  const gap = (r) => !r.sds.length;

  function stats() {
    const rows = data.rows;
    const items = [
      [types(rows), "UDL data types"],
      [types(rows.filter(publicEquivalent)), "with an open or derived equivalent in Pro and Team"],
      [types(rows.filter(edgeOnly)), "only in SpaceEdge"],
      [types(rows.filter(gap)), "with no SDS record yet"],
    ];
    $("[data-udl=stats]").innerHTML = items.map(([v, t]) => `<div class="inv-stat"><b>${v}</b><span>${esc(t)}</span></div>`).join("");
  }

  function chip(v) {
    return v === "—" ? `<span class="ud-chip ud-none">—</span>` : `<span class="ud-chip ud-${CHIP[v] ?? "none"}">${esc(v)}</span>`;
  }

  function controls() {
    $("[data-udl=controls]").innerHTML = `
      <div class="pl-seg" role="radiogroup" aria-label="Module group">${GROUPS.map((g) =>
        `<button type="button" role="radio" aria-checked="${g === f.group}" data-ugroup="${esc(g)}">${esc(g)}</button>`).join("")}</div>
      <div class="ud-row">
        <label class="ud-search"><span class="sr">Search UDL types</span><input type="search" id="udl-q" placeholder="Search a UDL type or SDS record (elset, CDM, radar)" value="${esc(f.q)}" data-uq></label>
        <label class="ud-sel"><span>Plan</span><select id="udl-plan" data-uplan><option value="">Every plan</option>${PLANS.map((p) =>
          `<option value="${p.id}" ${p.id === f.plan ? "selected" : ""}>${esc(p.name)} gets it</option>`).join("")}</select></label>
        <label class="ud-sel"><span>Show</span><select id="udl-only" data-uonly>
          <option value="">Every type</option><option value="edge" ${f.only === "edge" ? "selected" : ""}>Only in SpaceEdge</option>
          <option value="gap" ${f.only === "gap" ? "selected" : ""}>No SDS record yet</option></select></label>
      </div>
      <p class="ud-legend">${LEGEND.map(([k, t]) => `<span>${chip(k)} ${esc(t)}</span>`).join("")}</p>`;
  }

  function rows() {
    const s = f.q.trim().toLowerCase();
    return data.rows.filter((r) =>
      (f.group === "All groups" || r.group.includes(f.group)) &&
      (!f.plan || r.plans[f.plan] !== "—") &&
      (f.only !== "edge" || edgeOnly(r)) && (f.only !== "gap" || gap(r)) &&
      (!s || r.label.toLowerCase().includes(s) || r.udl.some((u) => u.includes(s)) ||
        r.sds.some((c) => c.toLowerCase() === s) || r.note.toLowerCase().includes(s)));
  }

  function render() {
    const list = rows();
    const area = new Map(data.areas.map((a) => [a.id, a.name]));
    let html = "", last = "";
    for (const r of list) {
      if (r.area !== last) { html += `<tr class="ud-area"><th colspan="${4 + PLANS.length}" scope="colgroup">${esc(area.get(r.area))}</th></tr>`; last = r.area; }
      html += `<tr><th scope="row"><b>${esc(r.label)}</b><code>${r.udl.map(esc).join(", ")}</code></th>
        <td class="ud-sds">${r.sds.length ? r.sds.map(esc).join(" ") : `<span class="ud-gap">No SDS yet</span>`}</td>
        <td>${esc(r.group)}</td>
        ${PLANS.map((p) => `<td class="ud-t">${chip(r.plans[p.id])}</td>`).join("")}
        <td class="ud-note">${esc(r.note)}</td></tr>`;
    }
    $("[data-udl=table]").innerHTML = `<table class="ud-table"><thead><tr><th scope="col">Data</th><th scope="col">SDS record</th><th scope="col">Group</th>
      ${PLANS.map((p) => `<th scope="col" class="ud-t${p.id === "spaceedge" ? " ud-edge" : ""}">${esc(p.name)}</th>`).join("")}<th scope="col">Open source</th></tr></thead>
      <tbody>${html || `<tr><td colspan="${4 + PLANS.length}">No types match. Clear the search or pick another group.</td></tr>`}</tbody></table>`;
    $("[data-udl=result]").textContent = `${list.length} rows · ${types(list)} UDL types`;
    for (const b of document.querySelectorAll("[data-ugroup]")) b.setAttribute("aria-checked", String(b.dataset.ugroup === f.group));
  }

  function wire() {
    const box = $("[data-udl=controls]");
    box.addEventListener("click", (e) => {
      const g = e.target.closest("[data-ugroup]")?.dataset.ugroup;
      if (g) { f.group = g; render(); }
    });
    box.addEventListener("input", (e) => { if (e.target.matches("[data-uq]")) { f.q = e.target.value; render(); } });
    box.addEventListener("change", (e) => {
      if (e.target.matches("[data-uplan]")) f.plan = e.target.value;
      else if (e.target.matches("[data-uonly]")) f.only = e.target.value;
      else return;
      render();
    });
    // The plan builder's "UDL data in <plan>" button.
    document.addEventListener("udl:plan", (e) => {
      f.plan = e.detail === "spaceedge" ? "" : e.detail;
      f.only = e.detail === "spaceedge" ? "edge" : "";
      f.group = "All groups";
      controls();
      render();
    });
  }

  fetch("marketing/udl-matrix.json", { cache: "no-cache" }).then((r) => r.json()).then((d) => {
    data = d;
    $("[data-udl=source]").textContent = d.source;
    stats();
    controls();
    wire();
    render();
  }).catch(() => {
    $("[data-udl=table]").innerHTML = `<p class="inv-empty">UDL data did not load. Reload the page.</p>`;
  });
})();
