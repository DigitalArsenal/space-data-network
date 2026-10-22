/*
 * Deployed on the Network (marketing/deployed.html): every module and flow on each host, from deployments.json - what
 * scripts/build-marketing-deployments.mjs read from the network. Searchable, filterable, sortable, exportable; the
 * filters and sort live in the URL hash so a link shows the same view. This file only reads the JSON.
 */
(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const esc = (value) =>
    String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const norm = (value) => String(value ?? "").toLowerCase();

  const ACCESS = { free: "Free", "signed-in": "Signed in", paid: "Paid" };
  const KIND = { module: "Module", flow: "Flow" };
  const ORIGIN = { produced: "Published here", mirrored: "Mirror of another host" };
  const COLUMNS = [
    { id: "host", label: "Host" },
    { id: "name", label: "Module or flow" },
    { id: "kind", label: "Kind" },
    { id: "version", label: "Version" },
    { id: "what", label: "What it does" },
    { id: "publishes", label: "Publishes" },
    { id: "access", label: "Access" },
    { id: "when", label: "Last published" },
  ];
  const FILTERS = [
    { id: "host", label: "Host" },
    { id: "kind", label: "Kind", names: KIND },
    { id: "access", label: "Access", names: ACCESS },
    { id: "origin", label: "Origin", names: ORIGIN },
  ];
  const DEFAULT = { q: "", host: "", kind: "", access: "", origin: "", sort: "host", dir: "asc" };

  let data = null;
  let state = { ...DEFAULT };
  const open = new Set();

  function readHash() {
    const params = new URLSearchParams(location.hash.slice(1)), next = { ...DEFAULT };
    for (const key of Object.keys(DEFAULT)) if (params.has(key)) next[key] = params.get(key);
    return next;
  }
  function writeHash() {
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(state)) if (value !== DEFAULT[key]) params.set(key, value);
    const hash = params.toString();
    history.replaceState(null, "", hash ? `#${hash}` : location.pathname + location.search);
  }

  const when = (item) => (item.kind === "flow" ? item.lastPublished : item.updated) || "";
  const day = (iso) => (iso ? iso.slice(0, 16).replace("T", " ") + "Z" : "");
  const publishes = (item) => (item.publishes ? [item.publishes.schema, item.publishes.provider, item.publishes.source].filter(Boolean).join(" · ") : item.family || "");
  const keyOf = (item) => `${item.host}|${item.kind}|${item.id}`;
  const value = (item, col) => ({ host: item.host, name: item.name, kind: item.kind, version: item.version || "", what: item.what, publishes: publishes(item), access: item.access, when: when(item) })[col];
  const haystack = (item) => norm([item.host, item.id, item.name, item.what, publishes(item), item.license, item.citation, item.sourceUrl, item.version, item.family].join(" "));

  function filtered() {
    const terms = norm(state.q).split(/\s+/).filter(Boolean);
    const list = data.items.filter((item) =>
      (!state.host || item.host === state.host) && (!state.kind || item.kind === state.kind) && (!state.access || item.access === state.access) &&
      (!state.origin || item.origin === state.origin) && terms.every((t) => haystack(item).includes(t)));
    const dir = state.dir === "desc" ? -1 : 1;
    return list.sort((a, b) => dir * String(value(a, state.sort)).localeCompare(String(value(b, state.sort)), undefined, { numeric: true }) || a.name.localeCompare(b.name));
  }

  function renderHosts() {
    $('[data-dp="hosts"]').innerHTML = data.hosts.map((h) => {
      const items = data.items.filter((i) => i.host === h.id), modules = items.filter((i) => i.kind === "module").length;
      const counts = h.reachable ? [modules ? `${modules} modules in its catalog` : "", `${h.flows ?? 0} flows published here`, h.mirrored ? `${h.mirrored} mirrored` : ""].filter(Boolean).join(" · ") : "";
      return `<article class="dp-host">
        <h3><span>${esc(h.id)}</span><span class="dp-state${h.reachable ? "" : " off"}">${h.reachable ? "Answering" : "Not read"}</span></h3>
        <p>${esc(h.role || h.name)}</p>
        ${counts ? `<p class="dp-counts">${esc(counts)}</p>` : `<p>${esc(h.note)}</p>`}
        ${h.endpoint ? `<p><a href="${esc(h.endpoint)}" rel="noopener">${esc(new URL(h.endpoint).hostname.slice(0, 40))}${new URL(h.endpoint).hostname.length > 40 ? "…" : ""}</a></p>` : ""}
        ${h.build ? `<p>${esc(h.build)}</p>` : ""}
      </article>`;
    }).join("");
  }

  function renderFilters() {
    const options = (f) => {
      const values = f.id === "host" ? data.hosts.map((h) => h.id) : Object.keys(f.names);
      return `<option value="">All</option>${values.map((v) => `<option value="${esc(v)}"${state[f.id] === v ? " selected" : ""}>${esc(f.names ? f.names[v] : v)}</option>`).join("")}`;
    };
    $('[data-dp="filters"]').innerHTML = FILTERS.map((f) => `<label><span>${esc(f.label)}</span><select data-filter="${f.id}">${options(f)}</select></label>`).join("") +
      `<button type="button" class="btn btn-small" data-dp="csv">Export CSV</button>`;
  }

  function renderTable() {
    const list = filtered();
    $('[data-dp="result"]').textContent = `${list.length} of ${data.items.length}`;
    const head = COLUMNS.map((c) => {
      const sorted = state.sort === c.id, arrow = sorted ? (state.dir === "asc" ? " ↑" : " ↓") : "";
      return `<th scope="col"${sorted ? ` aria-sort="${state.dir === "asc" ? "ascending" : "descending"}"` : ""}><button type="button" data-sort="${c.id}">${esc(c.label)}${arrow}</button></th>`;
    }).join("");
    const body = list.map((item) => {
      const k = keyOf(item), isOpen = open.has(k);
      const row = `<tr class="inv-row${isOpen ? " open" : ""}" data-key="${esc(k)}" tabindex="0" aria-expanded="${isOpen}">
        <td data-col="Host">${esc(item.host)}</td>
        <td data-col="Module or flow"><b>${esc(item.name)}</b>${item.origin === "mirrored" ? ` <span class="dp-tag">mirror</span>` : ""}</td>
        <td data-col="Kind">${esc(KIND[item.kind])}</td>
        <td data-col="Version">${esc(item.version || "")}</td>
        <td data-col="What it does" class="dp-what">${esc(item.what)}</td>
        <td data-col="Publishes"><code>${esc(publishes(item))}</code></td>
        <td data-col="Access"><span class="dp-tag ${esc(item.access)}">${esc(ACCESS[item.access] || item.access)}</span></td>
        <td data-col="Last published">${esc(day(when(item)))}</td></tr>`;
      if (!isOpen) return row;
      const facts = [["Id", item.id], ["Origin", ORIGIN[item.origin]], ["Records", item.records?.toLocaleString()], ["Publications", item.publications], ["Licence", item.license],
        ["Citation", item.citation], ["Source", item.sourceUrl], ["Producer peer", item.producer], ["Family", item.family]].filter(([, v]) => v !== undefined && v !== "");
      return row + `<tr class="dp-detail"><td colspan="${COLUMNS.length}"><dl>${facts.map(([t, v]) => `<dt>${esc(t)}</dt><dd>${/^https?:/.test(String(v)) ? `<a href="${esc(v)}" rel="noopener">${esc(v)}</a>` : esc(v)}</dd>`).join("")}</dl></td></tr>`;
    }).join("");
    $('[data-dp="table"]').innerHTML = list.length
      ? `<div class="inv-scroll"><table class="inv-table"><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table></div>`
      : `<p class="inv-empty">Nothing matches. Clear the search or a filter.</p>`;
  }

  function csv() {
    const cols = ["host", "kind", "id", "name", "version", "what", "publishes", "access", "origin", "last published", "records", "licence"];
    const rows = filtered().map((i) => [i.host, i.kind, i.id, i.name, i.version || "", i.what, publishes(i), i.access, i.origin || "", when(i), i.records ?? "", i.license || ""]);
    const text = [cols, ...rows].map((r) => r.map((v) => `"${String(v).replace(/"/g, '""')}"`).join(",")).join("\n");
    const a = document.createElement("a"); a.href = URL.createObjectURL(new Blob([text], { type: "text/csv" })); a.download = `sdn-deployed-${data.generatedAt.slice(0, 10)}.csv`; a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  }

  function render() { renderTable(); writeHash(); }

  async function start() {
    try {
      const r = await fetch("deployments.json", { cache: "no-cache" });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      data = await r.json();
    } catch (e) {
      $('[data-dp="meta"]').textContent = `Could not read deployments.json (${e.message}).`;
      return;
    }
    state = readHash();
    const reached = data.hosts.filter((h) => h.reachable).length;
    $('[data-dp="meta"]').innerHTML = `As of ${esc(day(data.generatedAt))}: ${data.items.length} modules and flows on ${reached} of ${data.hosts.length} hosts, read over the network by <code>${esc(data.generator)}</code> (each host's identity, its signed module catalog and the data lanes it publishes over SDN, ${data.schemasAsked} schemas asked).`;
    renderHosts(); renderFilters();
    $('[data-dp="q"]').value = state.q;
    $('[data-dp="q"]').addEventListener("input", (e) => { state.q = e.target.value; render(); });
    $('[data-dp="filters"]').addEventListener("change", (e) => { const f = e.target.dataset.filter; if (f) { state[f] = e.target.value; render(); } });
    $('[data-dp="filters"]').addEventListener("click", (e) => { if (e.target.closest('[data-dp="csv"]')) csv(); });
    $('[data-dp="table"]').addEventListener("click", (e) => {
      const sort = e.target.closest("[data-sort]");
      if (sort) { const id = sort.dataset.sort; state.dir = state.sort === id && state.dir === "asc" ? "desc" : "asc"; state.sort = id; render(); return; }
      const row = e.target.closest(".inv-row"); if (!row) return;
      const k = row.dataset.key; open.has(k) ? open.delete(k) : open.add(k); renderTable();
    });
    $('[data-dp="table"]').addEventListener("keydown", (e) => { if ((e.key === "Enter" || e.key === " ") && e.target.closest(".inv-row")) { e.preventDefault(); e.target.click(); } });
    render();
  }
  start();
})();
