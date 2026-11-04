// The marketing overview reads the same plan definitions as the module reference.
(() => {
  "use strict";
  const $ = (selector) => document.querySelector(selector);
  const esc = (value) => String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const price = (p) => p.monthlyPrice === null ? p.priceLabel : p.monthlyPrice === 0 ? "$0" : `$${p.monthlyPrice}`;
  const load = (url) => fetch(url, { cache: "no-cache" }).then((r) => {
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    return r.json();
  });
  Promise.all([load("marketing/plans.json"), load("marketing/catalog.json")]).then(([data, catalog]) => {
    $("[data-plans=headline]").textContent = data.headline;
    $("[data-plans=intro]").textContent = data.intro;
    $("[data-plans=data-rule]").textContent = data.dataRule;
    $("[data-plans=cards]").innerHTML = data.plans.map((p) => `<article class="mp-plan" data-plan="${esc(p.id)}">
      <p class="mp-audience">${esc(p.audience)}</p><h3>${esc(p.name)}</h3>
      <p class="mp-price"><strong>${esc(price(p))}</strong>${p.monthlyPrice > 0 ? "<span>/ month</span>" : ""}</p>
      <p class="mp-summary">${esc(p.summary)}</p>
      <ul>${p.includes.map((item) => `<li>${esc(item)}</li>`).join("")}</ul>
      <p class="mp-example">${esc(p.example)}</p>
      <a class="btn" href="${esc(p.href)}">${esc(p.cta)}</a></article>`).join("");
    const plans = new Map(data.plans.map((p) => [p.id, p]));
    $("[data-plans=capabilities]").innerHTML = data.capabilities.map((c) => `<tr>
      <th scope="row">${esc(c.name)}</th><td>${esc(c.description)}</td>
      <td><a href="marketing-modules.html#plan=${esc(c.plan)}">${esc(plans.get(c.plan).name)}</a></td></tr>`).join("");
    // Every catalog source sits in one rights group through its SDS license class.
    $("[data-plans=rights]").innerHTML = Object.entries(data.rights).map(([group, g]) => {
      const sources = catalog.sources.filter((src) => catalog.licenseClasses[src.licenseClass].group === group);
      return `<section class="mp-rights-group" data-rights="${esc(group)}"><h3>${esc(g.title)} <span>${sources.length}</span></h3><p>${esc(g.text)}</p>
        <ul>${sources.map((src) => `<li data-source="${esc(src.id)}"><a href="${esc(src.termsUrl || src.url)}" target="_blank" rel="noopener">${esc(src.name)}</a><small>${esc(src.terms)}</small></li>`).join("")}</ul></section>`;
    }).join("");
    $("[data-plans=talking-points]").innerHTML = data.talkingPoints.map((t) => `<article><h3>${esc(t.title)}</h3><p>${esc(t.text)}</p></article>`).join("");
  }).catch(() => {
    $("[data-plans=cards]").innerHTML = '<p role="alert">Plans could not load. <a href="marketing.html">Reload the page</a> or <a href="marketing/plans.json">read the plan definitions</a>.</p>';
  });
})();
