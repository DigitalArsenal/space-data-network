// spacedatanetwork.org/marketing.html: the SpaceAware module catalog.
// Everything on the page comes from catalog.json beside this script, so adding
// a module or a bundle is a JSON edit. After rendering, this script loads the
// shared earth.js and site.js, so the Earth sees every section keyframe and
// the scroll reveals see every card. Motion is transform, opacity or one 2D
// canvas (orbit.js), drawn only while on screen; reduced motion gets a still,
// complete page. After the first render the page only rewrites existing text
// nodes and attributes, never adds or removes nodes: section-links.js
// re-lands on the address's section whenever nodes are added.
(function () {
  'use strict';
  var HERE = document.currentScript.src;
  var REDUCED = matchMedia('(prefers-reduced-motion: reduce)').matches;
  var root = document.documentElement;
  var $ = function (sel) { return document.querySelector('[data-mk="' + sel + '"]'); };

  function el(tag, cls, text) {
    var node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text != null) node.appendChild(document.createTextNode(String(text)));
    return node;
  }
  function setText(node, text) { node.firstChild.data = String(text); }
  function icon(category) {
    var wrap = document.createElement('span');
    wrap.innerHTML = '<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + (ICONS[category] || ICONS.other) + '</svg>';
    return wrap.firstChild;
  }
  function link(label, href, primary) {
    var a = el('a', 'btn' + (primary ? ' btn-primary' : ''), label);
    a.href = href;
    if (/^https?:/.test(href) && href.indexOf(location.host) < 0) { a.target = '_blank'; a.rel = 'noopener'; }
    return a;
  }
  function onScreen(node, cb, margin) {
    if (!('IntersectionObserver' in window)) { cb(true); return; }
    new IntersectionObserver(function (e) { cb(e[e.length - 1].isIntersecting); }, { rootMargin: margin || '0px' }).observe(node);
  }
  function headerHeight() { return parseFloat(getComputedStyle(root).getPropertyValue('--sdn-stack-header-height')) || 52; }

  // Presentation only: a line icon per discipline, and a camera keyframe for
  // each discipline's section (earth.js documents the seven numbers).
  var ICONS = {
    console: '<circle cx="12" cy="12" r="8.5"/><path d="M3.5 12h17M12 3.5c2.6 2.4 2.6 14.6 0 17M12 3.5c-2.6 2.4-2.6 14.6 0 17"/>',
    'space-safety': '<path d="M12 3l7 3v6c0 4.4-3 7.7-7 9-4-1.3-7-4.6-7-9V6l7-3z"/><path d="M9 12l2 2 4-4"/>',
    sda: '<circle cx="12" cy="12" r="8.5"/><circle cx="12" cy="12" r="4.5"/><path d="M12 12l6-6"/>',
    'flight-dynamics': '<path d="M4 18c3-9 11-13 16-12"/><path d="M16 4l4 2-2 4"/><circle cx="4" cy="18" r="1.6"/>',
    'comms-rf': '<path d="M12 13v8M9 21h6"/><circle cx="12" cy="11" r="1.6"/><path d="M8.5 7.5a5 5 0 0 0 0 7M15.5 7.5a5 5 0 0 1 0 7M5.6 4.6a9 9 0 0 0 0 12.8M18.4 4.6a9 9 0 0 1 0 12.8"/>',
    'earth-sky': '<circle cx="9" cy="15" r="5.5"/><path d="M17 3l.9 2.1L20 6l-2.1.9L17 9l-.9-2.1L14 6l2.1-.9z"/><path d="M4.5 13c2 .6 3.4 2 4 4.3"/>',
    network: '<circle cx="5" cy="12" r="2.2"/><circle cx="19" cy="5" r="2.2"/><circle cx="19" cy="19" r="2.2"/><path d="M7 11l10-5M7 13l10 5M19 7.2v9.6"/>',
    other: '<rect x="4" y="4" width="16" height="16" rx="3"/><path d="M9 12h6M12 9v6"/>'
  };
  var KEYFRAMES = ['2.9 20 28 1.05 0.05 118 12', '3.3 70 8 -1.25 0.1 162 6', '2.7 100 25 1.1 0.05 120 10',
    '2.9 -40 -12 -1.05 0.05 210 8', '2.8 100 -20 1.05 0.05 125 8', '2.7 -75 38 1.1 0.05 122 10', '2.9 -160 20 -1.05 0.05 240 10'];
  var TIER_COPY = {
    free: 'Open spaceaware.io and start. Conjunction assessment, the camera and the analysis tools work with no account.',
    account: 'Sign in with a password, seed phrase, passkey or external wallet, with no email address. Signing in opens the track, detection, maneuver and communications modes.',
    'add-on': 'Paid modules are delivered encrypted to your account. Request pricing for a single module or a bundle.'
  };

  var C, byId = {}, catById = {}, bundlesOf = {}, drawer, sheets = {}, builder;

  function pricingHref(subject) { return C.pricing.cta.href + (subject ? encodeURIComponent(': ' + subject) : ''); }
  function priceLabel(b) { return C.pricing.approvedPriceList && b.price ? b.price : 'Pricing on request'; }
  function badges(m) {
    var row = el('div', 'mk-badges');
    row.appendChild(el('span', 'mk-badge access-' + m.access, C.access[m.access]));
    row.appendChild(el('span', 'mk-badge delivery', C.delivery[m.delivery]));
    return row;
  }

  // ---- Hero: letters rise, a ticker names each discipline, numbers count up.
  function hero() {
    var h1 = $('title'), text = h1.textContent.trim(), i = 0;
    h1.setAttribute('aria-label', text);
    h1.textContent = '';
    text.split(/\s+/).forEach(function (w, wi) {
      var word = el('span', 'mk-word');
      word.setAttribute('aria-hidden', 'true');
      Array.prototype.forEach.call(w, function (ch) {
        var s = el('span', 'mk-ch', ch);
        s.style.setProperty('--i', i++);
        word.appendChild(s);
      });
      if (wi) h1.appendChild(document.createTextNode(' '));
      h1.appendChild(word);
    });

    // Two spans trade places: the old name slides up, the next one rises.
    var rot = $('ticker'), names = C.categories.map(function (c) { return c.name; }), k = 0, timer = 0;
    var pair = [el('span', '', names[0]), el('span', 'mk-out', ' ')];
    rot.textContent = '';
    pair.forEach(function (s) { rot.appendChild(s); });
    if (!REDUCED) onScreen(rot, function (on) {
      clearInterval(timer);
      if (on) timer = setInterval(function () {
        if (document.hidden) return;
        k = (k + 1) % names.length;
        setText(pair[1], names[k]);
        pair[0].className = 'mk-out';
        pair[1].className = 'mk-in';
        pair.reverse();
      }, 2600);
    });

    var free = C.modules.filter(function (m) { return m.access === 'free'; }).length;
    var box = $('stats'), nums = [];
    [[C.modules.length, 'Modules'], [C.bundles.length, 'Bundles'], [C.categories.length, 'Disciplines'], [free, C.access.free]].forEach(function (s) {
      var d = el('div'), n = el('strong', '', REDUCED ? s[0] : 0);
      nums.push([n, s[0]]);
      d.appendChild(n);
      d.appendChild(el('span', '', s[1]));
      box.appendChild(d);
    });
    if (!REDUCED) {
      var t0 = performance.now() + 500;
      requestAnimationFrame(function count(now) {
        var t = Math.max(0, Math.min(1, (now - t0) / 1400)), e = 1 - Math.pow(1 - t, 3);
        nums.forEach(function (n) { setText(n[0], Math.round(n[1] * e)); });
        if (t < 1) requestAnimationFrame(count);
      });
    }
    $('hero-actions').appendChild(link(C.pricing.cta.label, pricingHref()));
  }

  // ---- Standards marquee: every standard the modules speak, in two rows.
  function marquee() {
    var seen = {};
    C.modules.forEach(function (m) { (m.standards || []).forEach(function (s) { seen[s] = 1; }); });
    var all = Object.keys(seen).sort(), half = Math.ceil(all.length / 2), box = $('marquee');
    [all.slice(0, half), all.slice(half)].forEach(function (list, r) {
      var row = el('div', 'mk-marquee-row' + (r ? ' rev' : ''));
      row.style.setProperty('--mk-dur', list.length * 3.2 + 's');
      for (var copy = 0; copy < 2; copy++) {
        var grp = el('div', 'mk-marquee-grp');
        if (copy) grp.setAttribute('aria-hidden', 'true');
        list.forEach(function (s) {
          var chip = el('span'), m = /^(CCSDS|SDS|ITU-R|OGC)\s(.+)$/.exec(s);
          if (m) { chip.appendChild(el('b', '', m[1])); chip.appendChild(document.createTextNode(' ' + m[2])); } else chip.textContent = s;
          grp.appendChild(chip);
        });
        row.appendChild(grp);
      }
      box.appendChild(row);
    });
    if (!REDUCED) onScreen(box, function (on) { box.toggleAttribute('data-paused', !on); });
  }

  // ---- One section per discipline, with a filter bar over them.
  function card(m) {
    var a = el('article', 'card mk-card');
    a.dataset.id = m.id;
    var orbit = el('span', 'mk-card-orbit');
    orbit.innerHTML = '<svg viewBox="0 0 48 22" aria-hidden="true"><ellipse cx="24" cy="11" rx="23" ry="10" fill="none" stroke="currentColor" stroke-width="1.2"/></svg><span class="sat"></span>';
    orbit.style.setProperty('--a0', Math.round(Math.random() * 100) + '%');
    a.appendChild(orbit);
    var top = el('div', 'mk-card-top'), h3 = el('h3'), b = el('button', '', m.name);
    b.type = 'button';
    b.addEventListener('click', function () { openDrawer(m.id); });
    top.appendChild(icon(m.category));
    h3.appendChild(b);
    top.appendChild(h3);
    a.appendChild(top);
    a.appendChild(el('p', '', m.tagline));
    a.appendChild(badges(m));
    a.appendChild(el('span', 'more-link', 'Details'));
    if (matchMedia('(pointer: fine)').matches) a.addEventListener('pointermove', function (e) {
      var r = a.getBoundingClientRect();
      a.style.setProperty('--mx', (e.clientX - r.left) + 'px');
      a.style.setProperty('--my', (e.clientY - r.top) + 'px');
    });
    return a;
  }

  function categories() {
    var host = $('categories'), sections = [], cards = [];
    C.categories.forEach(function (c, i) {
      var mods = C.modules.filter(function (m) { return m.category === c.id; });
      var s = el('section', 'block mk-cat');
      s.id = 'cat-' + c.id;
      s.dataset.cat = c.id;
      s.setAttribute('data-earth', KEYFRAMES[i % KEYFRAMES.length]);
      var wrap = el('div', 'wrap'), head = el('div', 'head'), grid = el('div', 'mk-grid');
      head.appendChild(el('p', 'eyebrow', mods.length + (mods.length === 1 ? ' module' : ' modules')));
      head.appendChild(el('h2', '', c.name));
      head.appendChild(el('p', 'lede', c.blurb));
      grid.setAttribute('data-section-links', 'off');
      mods.forEach(function (m) { var k = card(m); cards.push(k); grid.appendChild(k); });
      wrap.appendChild(head);
      wrap.appendChild(grid);
      s.appendChild(wrap);
      host.appendChild(s);
      sections.push(s);
    });

    var state = { cat: 'all', access: 'all' }, bar = $('filter'), chips = [];
    function jump() {
      var y = document.getElementById('modules').getBoundingClientRect().top + scrollY - headerHeight();
      if (scrollY > y + 4 || scrollY < y - innerHeight) scrollTo({ top: y, behavior: REDUCED ? 'auto' : 'smooth' });
    }
    function count(cat, access) {
      return C.modules.filter(function (m) { return (cat === 'all' || m.category === cat) && (access === 'all' || m.access === access); }).length;
    }
    function chip(label, value) {
      var b = el('button', 'mk-chip', label);
      b.type = 'button';
      b.dataset.value = value;
      b.appendChild(el('small', '', 0));
      b.addEventListener('click', function () { state.cat = value; apply(); jump(); });
      chips.push(b);
      bar.appendChild(b);
    }
    chip('All', 'all');
    C.categories.forEach(function (c) { chip(c.name, c.id); });
    bar.appendChild(el('span', 'mk-chip-sep'));
    var sel = el('select', 'mk-select');
    sel.setAttribute('aria-label', 'Access');
    [['all', 'Any access']].concat(Object.keys(C.access).map(function (k) { return [k, C.access[k]]; })).forEach(function (o) {
      var opt = el('option', '', o[1]);
      opt.value = o[0];
      sel.appendChild(opt);
    });
    sel.addEventListener('change', function () { state.access = sel.value; apply(); jump(); });
    bar.appendChild(sel);

    function apply() {
      var shown = {};
      cards.forEach(function (k) {
        var m = byId[k.dataset.id];
        var on = (state.cat === 'all' || m.category === state.cat) && (state.access === 'all' || m.access === state.access);
        k.classList.toggle('is-filtered', !on);
        if (on) shown[m.category] = 1;
      });
      sections.forEach(function (s) { s.classList.toggle('is-hidden', !shown[s.dataset.cat]); });
      chips.forEach(function (b) {
        b.setAttribute('aria-pressed', String(state.cat === b.dataset.value));
        setText(b.lastChild, count(b.dataset.value, state.access));
      });
    }
    apply();
  }

  // ---- Bundle builder: pick a bundle and its modules light up.
  function bundles() {
    var host = $('builder'), list = el('ul', 'mk-bundle-list'), panel = el('div', 'mk-bundle-panel');
    var copy = el('div', 'mk-bundle-copy'), matrix = el('div', 'mk-matrix'), chips = {}, buttons = [];
    var name = el('h3', '', ' '), summary = el('p', '', ' '), meta = el('p', 'mk-bundle-meta');
    var count = el('b', '', 0), who = el('span', '', ' '), price = el('span', '', ' '), n = el('span');
    n.appendChild(count);
    n.appendChild(document.createTextNode(' modules'));
    [n, who, price].forEach(function (x) { meta.appendChild(x); });
    [name, summary, meta].forEach(function (x) { copy.appendChild(x); });
    var actions = el('div', 'actions'), ask = link(C.pricing.cta.label, pricingHref(), true);
    actions.appendChild(ask);
    actions.appendChild(link(C.pricing.open.label, C.pricing.open.href));
    C.categories.forEach(function (c) {
      var g = el('div', 'mk-matrix-cat'), row = el('div');
      g.appendChild(el('span', '', c.name));
      C.modules.forEach(function (m) {
        if (m.category !== c.id) return;
        var b = el('button', 'mk-mod', m.name);
        b.type = 'button';
        b.insertAdjacentHTML('beforeend', '<svg aria-hidden="true"><rect x="0" y="0" width="100%" height="100%" rx="8" pathLength="1"/></svg>');
        b.addEventListener('click', function () { openDrawer(m.id); });
        chips[m.id] = b;
        row.appendChild(b);
      });
      g.appendChild(row);
      matrix.appendChild(g);
    });
    [copy, matrix, actions].forEach(function (x) { panel.appendChild(x); });

    var current = -1, auto = !REDUCED;
    function stopAuto() { auto = false; host.removeAttribute('data-auto'); }
    function select(i, user) {
      if (user) stopAuto();
      if (i === current) return;
      current = i;
      var b = C.bundles[i], order = {};
      b.modules.forEach(function (id, j) { order[id] = j; });
      buttons.forEach(function (x, j) { x.setAttribute('aria-pressed', String(j === i)); });
      setText(name, b.name);
      setText(summary, b.summary);
      setText(count, b.modules.length);
      setText(who, b.audience);
      setText(price, priceLabel(b));
      if (!REDUCED) { copy.classList.remove('mk-swap-in'); void copy.offsetWidth; copy.classList.add('mk-swap-in'); }
      Object.keys(chips).forEach(function (id) {
        var on = id in order;
        chips[id].style.setProperty('--d', (on ? Math.min(order[id] * 35, 700) : 0) + 'ms');
        chips[id].classList.toggle('on', on);
      });
      ask.href = pricingHref(b.name + ' bundle');
    }
    C.bundles.forEach(function (b, i) {
      var li = el('li'), btn = el('button', 'mk-bundle-btn');
      btn.type = 'button';
      btn.appendChild(el('b', '', b.name));
      btn.appendChild(el('span', '', b.tagline));
      btn.appendChild(el('i'));
      btn.addEventListener('click', function () { select(i, true); });
      // The bar under the lit bundle fills; when it is full the next one lights.
      btn.addEventListener('animationend', function () { if (host.hasAttribute('data-auto')) select((current + 1) % C.bundles.length); });
      buttons.push(btn);
      li.appendChild(btn);
      list.appendChild(li);
    });
    host.appendChild(list);
    host.appendChild(panel);
    select(0);
    host.addEventListener('pointerdown', stopAuto);
    host.addEventListener('keydown', stopAuto);
    onScreen(host, function (on) { host.toggleAttribute('data-auto', auto && on); }, '-20% 0px');
    return {
      select: function (id) {
        var i = C.bundles.findIndex(function (b) { return b.id === id; });
        if (i >= 0) select(i, true);
      }
    };
  }

  // ---- Access tiers.
  function tiers() {
    var host = $('tiers');
    Object.keys(C.access).forEach(function (k, i) {
      var li = el('li', 'mk-tier'), ul = el('ul');
      li.appendChild(el('span', 'depth-num', i + 1));
      li.appendChild(el('h3', '', C.access[k]));
      if (TIER_COPY[k]) li.appendChild(el('p', '', TIER_COPY[k]));
      C.modules.forEach(function (m) {
        if (m.access !== k) return;
        var item = el('li'), b = el('button', '', m.name);
        b.type = 'button';
        b.addEventListener('click', function () { openDrawer(m.id); });
        item.appendChild(b);
        ul.appendChild(item);
      });
      li.appendChild(ul);
      host.appendChild(li);
    });
  }

  // ---- Module drawer: one sheet per module, built once and shown on demand.
  function sheet(m) {
    var body = el('div', 'mk-drawer-body');
    body.hidden = true;
    var top = el('div', 'mk-card-top');
    top.appendChild(icon(m.category));
    top.appendChild(el('p', 'eyebrow', catById[m.category].name));
    body.appendChild(top);
    var h = el('h3', '', m.name);
    h.id = 'mk-sheet-' + m.id;
    body.appendChild(h);
    body.appendChild(el('p', 'lede', m.tagline));
    body.appendChild(el('p', '', m.summary));
    body.appendChild(badges(m));
    function group(title, node) { var g = el('div'); g.appendChild(el('h4', '', title)); g.appendChild(node); body.appendChild(g); }
    var caps = el('ul', 'mk-caps');
    m.capabilities.forEach(function (c) { caps.appendChild(el('li', '', c)); });
    group('What it does', caps);
    if (m.standards && m.standards.length) {
      var st = el('div', 'mk-badges');
      m.standards.forEach(function (s) { st.appendChild(el('span', 'mk-badge', s)); });
      group('Standards', st);
    }
    var ins = el('div', 'mk-badges');
    (bundlesOf[m.id] || []).forEach(function (b) {
      var c = el('button', 'mk-chip', b.name);
      c.type = 'button';
      c.addEventListener('click', function () {
        drawer.close();
        builder.select(b.id);
        document.getElementById('bundles').scrollIntoView({ behavior: REDUCED ? 'auto' : 'smooth' });
      });
      ins.appendChild(c);
    });
    group('In bundles', ins);
    var act = el('div', 'actions'), first = true;
    function add(label, href) { act.appendChild(link(label, href, first)); first = false; }
    if (m.access !== 'free') add(C.pricing.cta.label, pricingHref(m.name));
    if (m.delivery === 'console') add(C.pricing.open.label, C.pricing.open.href);
    if (m.learnMore && m.learnMore.replace(/\/$/, '') !== C.pricing.open.href.replace(/\/$/, '')) add('Learn more', m.learnMore);
    body.appendChild(act);
    return body;
  }
  function drawerInit() {
    drawer = $('drawer');
    var close = el('div', 'mk-drawer-close'), x = el('button', '', '×');
    x.type = 'button';
    x.setAttribute('aria-label', 'Close');
    x.addEventListener('click', function () { drawer.close(); });
    close.appendChild(x);
    drawer.appendChild(close);
    C.modules.forEach(function (m) { sheets[m.id] = sheet(m); drawer.appendChild(sheets[m.id]); });
    drawer.addEventListener('click', function (e) { if (e.target === drawer) drawer.close(); });
    drawer.addEventListener('close', function () {
      var earth = document.getElementById('earth');
      if (earth) earth.removeAttribute('data-idle');
    });
  }
  function openDrawer(id) {
    if (!sheets[id]) return;
    Object.keys(sheets).forEach(function (k) { sheets[k].hidden = k !== id; });
    drawer.setAttribute('aria-labelledby', 'mk-sheet-' + id);
    var earth = document.getElementById('earth');
    if (earth) earth.setAttribute('data-idle', '');    // the sheet covers the page: rest the Earth
    if (!drawer.open) drawer.showModal();
    drawer.scrollTop = 0;
  }

  function finish() {
    $('final-actions').appendChild(link(C.pricing.cta.label, pricingHref()));
    var d = new Date(C.updated + 'T00:00:00Z');
    $('updated').textContent = (C.pricing.approvedPriceList ? '' : 'Prices on request. ') + 'Catalog updated ' +
      d.toLocaleDateString('en-GB', { day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC' }) + '.';
  }

  function render(catalog) {
    C = catalog;
    C.modules.forEach(function (m) { byId[m.id] = m; });
    C.categories.forEach(function (c) { catById[c.id] = c; });
    C.bundles.forEach(function (b) { b.modules.forEach(function (id) { (bundlesOf[id] = bundlesOf[id] || []).push(b); }); });
    hero();
    marquee();
    categories();
    builder = bundles();
    tiers();
    drawerInit();
    finish();
    window.SDNMarketingOrbit({ catalog: C, open: openDrawer, reduced: REDUCED });
  }

  // The shared scene scripts run once the catalog is on the page.
  function scene() {
    ['../earth.js', '../site.js'].forEach(function (src) {
      var s = document.createElement('script');
      s.src = new URL(src, HERE).href;
      s.async = false;
      document.body.appendChild(s);
    });
  }

  fetch(new URL('catalog.json', HERE).href, { cache: 'no-cache' })
    .then(function (r) { if (!r.ok) throw new Error('catalog ' + r.status); return r.json(); })
    .then(render)
    .catch(function (err) {
      console.warn('marketing:', err);
      var s = el('section', 'block'), w = el('div', 'wrap'), p = el('p', 'lede', 'The module catalog did not load. ');
      p.appendChild(link('Open SpaceAware', 'https://spaceaware.io', true));
      w.appendChild(p);
      s.appendChild(w);
      $('categories').appendChild(s);
    })
    .then(scene);
})();
