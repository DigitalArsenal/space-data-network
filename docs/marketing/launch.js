/*
 * Launch on spacedatanetwork.org/marketing.html: the launch targets in waves, a plan for each, and the shared launch
 * kit. Read from marketing/launch.json; the summary counts come from its targets. Filters by wave, fit, product and
 * search.
 */
(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const esc = (value) =>
    String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const CHIP = { High: "ok", Medium: "teal", Low: "part", Skip: "gap" };
  const f = { wave: "all", fit: "", product: "", q: "" };
  let data = null;

  const chip = (fit) => `<span class="po-chip po-${CHIP[fit] || "none"}">${esc(fit)}</span>`;
  const host = (url) => url.replace(/^https?:\/\//, "");
  const waveName = (w) => w === null ? "Not used" : `${w} · ${data.waves[w].name}`;

  function rows() {
    const s = f.q.trim().toLowerCase();
    return data.targets.filter((t) =>
      (f.wave === "all" || String(t.wave) === f.wave) && (!f.fit || t.fit === f.fit) &&
      (!f.product || t.product.includes(f.product)) &&
      (!s || [t.name, t.url, t.kind, t.cost, t.watch, ...t.plan].some((x) => x.toLowerCase().includes(s))))
      .sort((a, b) => (a.wave ?? 9) - (b.wave ?? 9) || Object.keys(CHIP).indexOf(a.fit) - Object.keys(CHIP).indexOf(b.fit));
  }

  function frame() {
    $("[data-ln=copy]").textContent = data.copy;
    $("[data-ln=stats]").innerHTML = Object.keys(CHIP).map((fit) =>
      `<div class="inv-stat"><b>${data.targets.filter((t) => t.fit === fit).length}</b><span>${fit === "Skip" ? "skipped" : `${fit.toLowerCase()} fit`}</span></div>`).join("");
    $("[data-ln=waves]").innerHTML = data.waves.map((w) => `<div class="pl-group"><h3>${w.id} · ${esc(w.name)}</h3>
      <p>${esc(w.when)} · ${data.targets.filter((t) => t.wave === w.id).length} targets</p><ul><li>${esc(w.goal)}</li></ul></div>`).join("");
    $("[data-ln=kit]").innerHTML = data.kit.map((k) => `<div><h3>${esc(k.title)}</h3><ul>${k.items.map((x) => `<li>${esc(x)}</li>`).join("")}</ul></div>`).join("");
    $("[data-ln=controls]").innerHTML = `
      <div class="pl-seg" role="radiogroup" aria-label="Wave">${[["all", "All waves"], ...data.waves.map((w) => [String(w.id), w.name]), ["null", "Not used"]].map(([v, t]) =>
        `<button type="button" role="radio" aria-checked="${v === f.wave}" data-lwave="${v}">${esc(t)}</button>`).join("")}</div>
      <div class="ud-row">
        <label class="ud-search"><span class="sr">Search launch targets</span><input type="search" placeholder="Search a target, kind or step (review, badge, Korean)" data-lq></label>
        <label class="ud-sel"><span>Fit</span><select data-lfit><option value="">Every fit</option>${Object.keys(CHIP).map((x) =>
          `<option value="${x}">${x}</option>`).join("")}</select></label>
        <label class="ud-sel"><span>Launches</span><select data-lproduct><option value="">Every product</option>${Object.entries(data.products).map(([id, p]) =>
          `<option value="${id}">${esc(p.name)}</option>`).join("")}</select></label>
      </div>
      <p class="ud-legend">${Object.entries(data.fits).map(([k, t]) => `<span>${chip(k)} ${esc(t)}</span>`).join("")}</p>`;
  }

  function render() {
    const list = rows();
    $("[data-ln=table]").innerHTML = `<table class="ud-table ln-table"><thead><tr><th scope="col">Target</th><th scope="col">Wave</th><th scope="col">Fit</th>
      <th scope="col">Launches</th><th scope="col">Cost</th><th scope="col">Plan</th><th scope="col">Watch for</th></tr></thead>
      <tbody>${list.map((t) => `<tr><th scope="row"><b>${esc(t.name)}</b><a href="${esc(t.url)}" target="_blank" rel="noopener">${esc(host(t.url))}</a><small>${esc(t.kind)}</small></th>
        <td>${esc(waveName(t.wave))}</td><td>${chip(t.fit)}</td>
        <td>${t.product.map((p) => esc(data.products[p].name)).join("<br>")}</td><td class="ud-note">${esc(t.cost)}</td>
        <td class="ln-plan"><ol>${t.plan.map((x) => `<li>${esc(x)}</li>`).join("")}</ol></td><td class="ud-note">${esc(t.watch)}</td></tr>`).join("") ||
        `<tr><td colspan="7">No targets match. Clear the search or pick another wave.</td></tr>`}</tbody></table>`;
    $("[data-ln=result]").textContent = `${list.length} of ${data.targets.length} targets`;
    for (const b of document.querySelectorAll("[data-lwave]")) b.setAttribute("aria-checked", String(b.dataset.lwave === f.wave));
  }

  function wire() {
    const box = $("[data-ln=controls]");
    box.addEventListener("click", (e) => {
      const w = e.target.closest("[data-lwave]")?.dataset.lwave;
      if (w) { f.wave = w; render(); }
    });
    box.addEventListener("input", (e) => { if (e.target.matches("[data-lq]")) { f.q = e.target.value; render(); } });
    box.addEventListener("change", (e) => {
      if (e.target.matches("[data-lfit]")) f.fit = e.target.value;
      else if (e.target.matches("[data-lproduct]")) f.product = e.target.value;
      else return;
      render();
    });
  }

  fetch("marketing/launch.json", { cache: "no-cache" }).then((r) => r.json()).then((d) => {
    data = d;
    frame();
    wire();
    render();
  }).catch(() => {
    $("[data-ln=table]").innerHTML = `<p class="inv-empty">Launch targets did not load. Reload the page.</p>`;
  });
})();
