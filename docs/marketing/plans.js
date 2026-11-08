// The marketing overview reads the same plan definitions as the module reference.
// Priced plans are cards; a plan priced by contact (SpaceEdge) is its own band below them.
(() => {
  "use strict";
  const $ = (selector) => document.querySelector(selector);
  const esc = (value) => String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const price = (p) => p.monthlyPrice === null ? p.priceLabel : p.monthlyPrice === 0 ? "$0" : `$${p.monthlyPrice}`;
  // The SpaceEdge wordmark: the first letter of each word full height, the rest small capitals (plans.css .se-wordmark)
  const wordmark = (name) => name.split(/(?=[A-Z])/).map((w) => `${esc(w[0])}<small>${esc(w.slice(1).toUpperCase())}</small>`).join("");
  const title = (p, size) => p.mark
    ? `<img class="se-mark-dark" src="${esc(p.mark.dark)}" alt="" width="${size}" height="${size}"><img class="se-mark-light" src="${esc(p.mark.light)}" alt="" width="${size}" height="${size}"><span class="se-wordmark" role="img" aria-label="${esc(p.name)}">${wordmark(p.name)}</span>`
    : esc(p.name);
  const examples = (p) => p.examples
    ? `<ul class="mp-asks">${p.examples.map((e) => `<li>“${esc(e)}”</li>`).join("")}</ul>`
    : `<p class="mp-example">${esc(p.example)}</p>`;
  const note = (p) => p.note ? `<p class="mp-note">${esc(p.note)}</p>` : "";
  const load = (url) => fetch(url, { cache: "no-cache" }).then((r) => {
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    return r.json();
  });
  Promise.all([load("marketing/plans.json"), load("marketing/catalog.json")]).then(([data, catalog]) => {
    $("[data-plans=headline]").textContent = data.headline;
    $("[data-plans=intro]").textContent = data.intro;
    $("[data-plans=data-rule]").textContent = data.dataRule;
    const priced = data.plans.filter((p) => p.monthlyPrice !== null);
    const contact = data.plans.filter((p) => p.monthlyPrice === null);
    $("[data-plans=cards]").innerHTML = priced.map((p) => `<article class="mp-plan" data-plan="${esc(p.id)}">
      <p class="mp-audience">${esc(p.audience)}</p><h3>${title(p, 30)}</h3>
      <p class="mp-price"><strong>${esc(price(p))}</strong>${p.monthlyPrice > 0 ? "<span>/ month</span>" : ""}</p>
      <p class="mp-summary">${esc(p.summary)}</p>
      <ul>${p.includes.map((item) => `<li>${esc(item)}</li>`).join("")}</ul>
      ${examples(p)}${note(p)}
      <a class="btn" href="${esc(p.href)}">${esc(p.cta)}</a></article>`).join("");
    $("[data-plans=edge]").innerHTML = contact.map((p) => `<article class="mp-band" data-plan="${esc(p.id)}">
      <div><p class="mp-audience">${esc(p.audience)}</p><h3>${title(p, 34)}</h3><p class="mp-summary">${esc(p.summary)}</p>
        <a class="btn" href="${esc(p.href)}">${esc(p.cta)}</a></div>
      <ul>${p.includes.map((item) => `<li>${esc(item)}</li>`).join("")}</ul></article>`).join("");
    const plans = new Map(data.plans.map((p) => [p.id, p]));
    $("[data-plans=capabilities]").innerHTML = data.capabilities.map((c) => `<tr>
      <th scope="row">${esc(c.name)}</th><td>${esc(c.description)}</td>
      <td><a href="marketing-modules.html#plan=${esc(c.plan)}">${esc(plans.get(c.plan).name)}</a></td></tr>`).join("");
    // Every catalog source sits in one rights group through its SDS license class; empty groups are left out.
    const groups = Object.entries(data.rights).map(([group, g]) =>
      [group, g, catalog.sources.filter((src) => catalog.licenseClasses[src.licenseClass].group === group)]).filter(([, , sources]) => sources.length);
    $("[data-plans=sources-summary]").textContent = `Every source and its terms (${groups.reduce((n, [, , s]) => n + s.length, 0)})`;
    $("[data-plans=rights]").innerHTML = groups.map(([group, g, sources]) =>
      `<section class="mp-rights-group" data-rights="${esc(group)}"><h3>${esc(g.title)} <span>${sources.length}</span></h3><p>${esc(g.text)}</p>
        <ul>${sources.map((src) => `<li data-source="${esc(src.id)}"><a href="${esc(src.termsUrl || src.url)}" target="_blank" rel="noopener">${esc(src.name)}</a><small>${esc(src.terms)}</small></li>`).join("")}</ul></section>`).join("");
  }).catch(() => {
    $("[data-plans=cards]").innerHTML = '<p role="alert">Plans could not load. <a href="marketing.html">Reload the page</a> or <a href="marketing/plans.json">read the plan definitions</a>.</p>';
  });
})();
