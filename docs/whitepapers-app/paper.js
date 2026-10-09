// Whitepaper pages (docs/build-whitepapers.mjs): reading progress, the
// contents rail and its phone sheet, the side panel that runs a section's
// interactive model, and resting the header's Earth once it is off screen.
(function () {
  var root = document.documentElement;
  var body = document.body;
  var article = document.querySelector('.paper');
  var hero = document.querySelector('.paper-hero');
  var toc = document.querySelector('.paper-toc');
  var fab = document.querySelector('.toc-fab');
  var fabLabel = fab && fab.querySelector('.toc-fab-label');
  var panel = document.querySelector('.paper-panel');
  var scrim = document.querySelector('.paper-scrim');
  var earth = document.getElementById('earth');
  if (!article) return;
  var reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;

  // Reading progress: how far the article has scrolled past the top bar.
  var queued = false;
  function measure() {
    queued = false;
    var r = article.getBoundingClientRect();
    var span = r.height - window.innerHeight * 0.6;
    var p = Math.min(1, Math.max(0, (window.innerHeight * 0.4 - r.top) / Math.max(1, span)));
    root.style.setProperty('--progress', p.toFixed(4));
    body.classList.toggle('reading', r.top < window.innerHeight * 0.5 && r.bottom > window.innerHeight * 0.5);
  }
  function queue() { if (!queued) { queued = true; requestAnimationFrame(measure); } }
  addEventListener('scroll', queue, { passive: true });
  addEventListener('resize', queue);
  measure();

  // The header's Earth only draws while the header is on screen.
  if (earth && hero && 'IntersectionObserver' in window) {
    new IntersectionObserver(function (entries) {
      if (entries[0].isIntersecting) earth.removeAttribute('data-idle');
      else earth.setAttribute('data-idle', '');
    }).observe(hero);
  }

  // Contents: the section being read is marked, and its subsections open.
  var links = toc ? Array.prototype.slice.call(toc.querySelectorAll('a[href^="#"]')) : [];
  var byId = {};
  links.forEach(function (a) { byId[decodeURIComponent(a.getAttribute('href').slice(1))] = a; });
  var heads = Array.prototype.slice.call(article.querySelectorAll('h2[id], h3[id]')).filter(function (h) { return byId[h.id]; });
  var current = null;
  function mark() {
    var line = (parseFloat(getComputedStyle(root).getPropertyValue('--sdn-stack-header-height')) || 52) + 80;
    var at = null;
    for (var i = 0; i < heads.length; i++) {
      if (heads[i].getBoundingClientRect().top <= line) at = heads[i];
      else break;
    }
    if (at === current) return;
    current = at;
    links.forEach(function (a) { a.classList.remove('is-active'); });
    toc.querySelectorAll('li.is-open').forEach(function (li) { li.classList.remove('is-open'); });
    if (!at) { if (fabLabel) fabLabel.textContent = 'Contents'; return; }
    var a = byId[at.id];
    a.classList.add('is-active');
    var top = a.closest('ol').closest('li') || a.parentElement;
    top.classList.add('is-open');
    if (fabLabel) fabLabel.textContent = Array.prototype.map.call(top.querySelector('a').children, function (c) { return c.textContent; }).join(' ').trim();
    // Keep the marked entry in view inside the rail.
    if (getComputedStyle(toc).position === 'sticky') {
      var tr = toc.getBoundingClientRect(), ar = a.getBoundingClientRect();
      if (ar.top < tr.top + 40 || ar.bottom > tr.bottom - 40) toc.scrollTop += ar.top - tr.top - tr.height / 3;
    }
  }
  var markQueued = false;
  addEventListener('scroll', function () {
    if (!markQueued) { markQueued = true; requestAnimationFrame(function () { markQueued = false; mark(); }); }
  }, { passive: true });
  mark();

  // Sheets (the phone contents and the model panel) share the scrim.
  function showScrim(on) {
    if (!scrim) return;
    if (on) { scrim.hidden = false; void scrim.offsetWidth; scrim.classList.add('is-open'); }
    else { scrim.classList.remove('is-open'); setTimeout(function () { if (!scrim.classList.contains('is-open')) scrim.hidden = true; }, reduced ? 0 : 300); }
  }
  var sheetMode = matchMedia('(max-width: 1199px)');
  function openToc(on) {
    body.classList.toggle('toc-open', on);
    fab.setAttribute('aria-expanded', String(on));
    showScrim(on);
    if (on) { var a = toc.querySelector('a.is-active') || links[0]; if (a) a.scrollIntoView({ block: 'center' }); }
  }
  if (fab && toc) {
    fab.addEventListener('click', function () { openToc(!body.classList.contains('toc-open')); });
    toc.querySelector('.toc-close').addEventListener('click', function () { openToc(false); fab.focus(); });
    links.forEach(function (a) { a.addEventListener('click', function () { if (sheetMode.matches) openToc(false); }); });
  }

  // The model panel.
  var frame = panel && panel.querySelector('iframe');
  var frameBox = panel && panel.querySelector('.panel-body');
  var title = panel && panel.querySelector('#panel-title');
  var openLink = panel && panel.querySelector('.panel-open');
  var opener = null;
  var closeTimer = 0;
  if (frame) frame.addEventListener('load', function () { if (frame.getAttribute('src')) frameBox.classList.add('is-loaded'); });
  // An isolated page can only frame isolated pages; the paper's service worker
  // serves the models under models-proxy/ with the headers GitHub Pages lacks.
  var MODELS = 'https://digitalarsenal.github.io/orbit-accuracy-experiments/';
  function frameSource(url) {
    if (!window.crossOriginIsolated || !navigator.serviceWorker || !navigator.serviceWorker.controller) return url;
    if (url.indexOf(MODELS) !== 0) return url;
    return 'models-proxy/' + url.slice(MODELS.length);
  }
  function openPanel(button) {
    var url = button.getAttribute('data-model-url');
    clearTimeout(closeTimer);
    document.querySelectorAll('.run-it.is-current').forEach(function (b) { b.classList.remove('is-current'); });
    button.classList.add('is-current');
    opener = button;
    title.textContent = button.getAttribute('data-model-title');
    openLink.href = url;
    var src = frameSource(url);
    if (frame.getAttribute('src') !== src) { frameBox.classList.remove('is-loaded'); frame.setAttribute('src', src); }
    // Reading beside the panel reflows the column; the control stays where it was on screen.
    var before = button.getBoundingClientRect().top;
    panel.hidden = false;
    void panel.offsetWidth;
    panel.classList.add('is-open');
    body.classList.add('panel-open');
    window.scrollBy({ top: button.getBoundingClientRect().top - before, behavior: 'instant' });
    if (!matchMedia('(min-width: 1200px)').matches) showScrim(true);
    panel.querySelector('.panel-close').focus({ preventScroll: true });
  }
  function closePanel() {
    if (!panel || panel.hidden) return;
    var before = opener ? opener.getBoundingClientRect().top : 0;
    panel.classList.remove('is-open');
    body.classList.remove('panel-open');
    if (opener) window.scrollBy({ top: opener.getBoundingClientRect().top - before, behavior: 'instant' });
    showScrim(false);
    document.querySelectorAll('.run-it.is-current').forEach(function (b) { b.classList.remove('is-current'); });
    closeTimer = setTimeout(function () { panel.hidden = true; }, reduced ? 0 : 400);
    if (opener) opener.focus({ preventScroll: true });
  }
  if (panel) {
    article.addEventListener('click', function (e) {
      var button = e.target.closest('.run-it');
      if (button) openPanel(button);
    });
    panel.querySelector('.panel-close').addEventListener('click', closePanel);
  }
  if (scrim) scrim.addEventListener('click', function () { closePanel(); if (body.classList.contains('toc-open')) openToc(false); });
  addEventListener('keydown', function (e) {
    if (e.key !== 'Escape') return;
    if (body.classList.contains('toc-open')) openToc(false);
    else closePanel();
  });
})();
