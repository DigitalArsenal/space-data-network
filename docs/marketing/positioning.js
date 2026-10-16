/*
 * Positioning on spacedatanetwork.org/marketing.html: the incumbent (ASW) and where each side leads, status by
 * capability area, the claims the copy may make and those it may not, and the accuracy targets set by open literature.
 * Read from marketing/positioning.json; the summary counts come from its areas. The text follows the page's audience
 * level (plans.js sends plans:level).
 */
(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const esc = (value) =>
    String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const CHIP = { Ahead: "ok", Parity: "teal", Behind: "part", Missing: "gap" };
  let data = null, level = new URLSearchParams(location.hash.slice(1)).get("level") || "executive";

  const chip = ([state, note]) => `<span class="po-state"><span class="po-chip po-${CHIP[state] || "none"}">${esc(state)}</span>${note ? ` ${esc(note)}` : ""}</span>`;
  const link = (x) => x.url ? `${esc(x.text)} <a href="${esc(x.url)}" target="_blank" rel="noopener">source</a>` : esc(x.text);

  function render() {
    $("[data-po=copy]").textContent = data.copy[level] ?? data.copy.executive;
    const count = (s) => data.areas.filter((a) => a.status.some((x) => x[0] === s)).length;
    $("[data-po=stats]").innerHTML = ["Ahead", "Parity", "Behind", "Missing"].map((s) =>
      `<div class="inv-stat"><b>${count(s)} of ${data.areas.length}</b><span>areas with a part ${s === "Parity" ? "at parity" : s.toLowerCase()}</span></div>`).join("");
    $("[data-po=leads]").innerHTML = `
      <div><h3>Where ASW leads</h3><ul>${data.aswLeads.map((x) => `<li>${link(x)}</li>`).join("")}</ul></div>
      <div><h3>Where we lead</h3><ul>${data.weLead.map((x) => `<li>${link(x)}</li>`).join("")}</ul></div>`;
    $("[data-po=areas]").innerHTML = `<table class="ud-table po-table"><thead><tr><th scope="col">Capability area</th><th scope="col">ASW</th><th scope="col">SpaceAware</th><th scope="col">Parity benchmark</th></tr></thead>
      <tbody>${data.areas.map((a) => `<tr><th scope="row"><b>${esc(a.name)}</b></th><td>${esc(a.asw)}</td><td>${a.status.map(chip).join("<br>")}</td><td class="ud-sds">${esc(a.bar)}</td></tr>`).join("")}</tbody></table>`;
    $("[data-po=say]").innerHTML = `<table class="ud-table po-table"><thead><tr><th scope="col">Say</th><th scope="col">With</th></tr></thead>
      <tbody>${data.say.map((x) => `<tr><td>${esc(x.claim)}</td><td class="ud-note">${esc(x.note)}</td></tr>`).join("")}</tbody></table>`;
    $("[data-po=dont]").innerHTML = `<table class="ud-table po-table"><thead><tr><th scope="col">Do not say</th><th scope="col">Why</th></tr></thead>
      <tbody>${data.dontSay.map((x) => `<tr><td>${esc(x.claim)}</td><td class="ud-note">${esc(x.why)}</td></tr>`).join("")}</tbody></table>`;
    $("[data-po=targets]").innerHTML = `<table class="ud-table po-table"><thead><tr><th scope="col">Target</th><th scope="col">Reference</th><th scope="col">Testable on public data</th></tr></thead>
      <tbody>${data.targets.map((x) => `<tr><td>${esc(x.target)}</td><td class="ud-note">${esc(x.reference)}</td><td>${esc(x.public)}</td></tr>`).join("")}</tbody></table>`;
  }

  document.addEventListener("plans:level", (e) => { level = e.detail; if (data) render(); });
  fetch("marketing/positioning.json", { cache: "no-cache" }).then((r) => r.json()).then((d) => { data = d; render(); }).catch(() => {
    $("[data-po=areas]").innerHTML = `<p class="inv-empty">Positioning did not load. Reload the page.</p>`;
  });
})();
