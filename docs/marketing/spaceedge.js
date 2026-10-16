/*
 * What SpaceEdge adds, on spacedatanetwork.org/marketing.html: security features and the certifications targeted, the
 * warfighting functions in academic and military wording, real-time collaboration, legacy integration at the edge and
 * CDAO environments, each with its status. Read from marketing/spaceedge.json; the text follows the page's audience
 * level (plans.js sends plans:level), and the functions' wording follows its own switch.
 */
(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const esc = (value) =>
    String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  let data = null, level = new URLSearchParams(location.hash.slice(1)).get("level") || "executive", wording = "both";

  // Live (in some form) is green, a target amber, anything offered as a service blue.
  const status = (s) => `<span class="po-chip ${/^Live/.test(s) ? "po-ok" : /^Offered/.test(s) ? "po-teal" : "po-part"}">${esc(s)}</span>`;
  const list = (rows, key) => `<table class="ud-table se-table"><tbody>${rows.map((r) => `<tr><td>${esc(r[key])}</td><td class="ud-t">${status(r.status)}</td></tr>`).join("")}</tbody></table>`;

  function render() {
    $("[data-se=copy]").textContent = data.copy[level] ?? data.copy.executive;
    $("[data-se=security]").innerHTML = `
      <div><h4>Security features</h4><ul>${data.security.live.map((x) => `<li>${esc(x)}</li>`).join("")}</ul></div>
      <div><h4>Certifications</h4><table class="ud-table se-table"><tbody>${data.security.certifications.map((c) =>
        `<tr><td><b>${esc(c.name)}</b><br><span class="ud-note">${esc(c.detail)}</span></td><td class="ud-t">${status(c.status)}</td></tr>`).join("")}</tbody></table></div>`;
    const cols = wording === "academic" ? ["academic"] : wording === "military" ? ["military"] : ["academic", "military"];
    $("[data-se=warfighting]").innerHTML = `<table class="ud-table se-table"><thead><tr>${cols.map((c) => `<th scope="col">${c === "academic" ? "Academic wording" : "Military wording"}</th>`).join("")}<th scope="col" class="ud-t">Status</th></tr></thead>
      <tbody>${data.warfighting.map((r) => `<tr>${cols.map((c) => `<td>${esc(r[c])}</td>`).join("")}<td class="ud-t">${status(r.status)}</td></tr>`).join("")}</tbody></table>`;
    for (const b of document.querySelectorAll("[data-wording]")) b.setAttribute("aria-checked", String(b.dataset.wording === wording));
    $("[data-se=collaboration]").innerHTML = list(data.collaboration, "feature");
    $("[data-se=legacy]").innerHTML = list(data.legacy, "feature");
    $("[data-se=cdao]").innerHTML = list(data.cdao, "feature");
  }

  document.addEventListener("plans:level", (e) => { level = e.detail; if (data) render(); });
  $("[data-se=wording]")?.addEventListener("click", (e) => { const w = e.target.closest("[data-wording]")?.dataset.wording; if (w) { wording = w; render(); } });
  fetch("marketing/spaceedge.json", { cache: "no-cache" }).then((r) => r.json()).then((d) => { data = d; render(); }).catch(() => {
    $("[data-se=warfighting]").innerHTML = `<p class="inv-empty">SpaceEdge features did not load. Reload the page.</p>`;
  });
})();
