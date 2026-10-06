// Section addresses for every page of spacedatanetwork.org.
//
// Owner 2026-10-05: "all sections on all pages to have section links so that
// I can copy a URL and send people to sections". Owner 2026-10-06: "remove
// the hashtags by section and have it automatically be added to the URL
// while scrolling".
//
// Every h2 and h3 in the page content gets a stable id: its section's id when
// it heads one (existing links keep working), its own id when it has one,
// else a slug of its text. While reading, the address bar follows the section
// at the top of the screen (history.replaceState: no history entries, no
// jumps), so copying the URL at any point shares that section. A URL that
// names a section lands on it, clear of the sticky top bar, including on
// pages whose content arrives after load.
(function () {
  'use strict';
  if (window.__sdnSectionLinks) return;
  window.__sdnSectionLinks = true;

  var SKIP = '.sdn-header, .sdn-footer, [data-sdn-stack], nav, .ai-credits, [data-section-links="off"]';
  var used = Object.create(null);
  var targets = [];

  function addStyle() {
    var style = document.createElement('style');
    style.textContent = '[data-sdn-section]{scroll-margin-top:calc(var(--sdn-stack-header-height,52px) + 20px)}';
    document.head.appendChild(style);
  }

  function slug(text) {
    var s = String(text || '')
      .toLowerCase()
      .normalize('NFKD')
      .replace(/[̀-ͯ]/g, '')
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 64);
    return s || 'section';
  }

  function unique(base) {
    var id = base;
    var n = 2;
    while (used[id] || document.getElementById(id)) id = base + '-' + n++;
    used[id] = true;
    return id;
  }

  // The section a heading names, when it is that section's first heading.
  function owningSection(heading) {
    var section = heading.closest('section, article');
    if (!section || !section.id) return null;
    var first = section.querySelector('h1, h2, h3');
    return first === heading ? section : null;
  }

  function mark(heading) {
    if (heading.hasAttribute('data-sdn-anchored') || heading.closest(SKIP)) return;
    heading.setAttribute('data-sdn-anchored', '');
    var target = owningSection(heading) || heading;
    if (target.id) used[target.id] = true;
    else target.id = unique(slug(heading.textContent));
    if (!target.hasAttribute('data-sdn-section')) {
      target.setAttribute('data-sdn-section', '');
      targets.push(target);
    }
  }

  function headerHeight() {
    return parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--sdn-stack-header-height')) || 52;
  }

  // Where a section sits on the page, ignoring the scroll-reveal slide-in
  // (offsetTop is layout, before transforms): a section still sliding in is
  // read where it comes to rest.
  function pageTop(el) {
    var y = 0;
    for (var e = el; e; e = e.offsetParent) y += e.offsetTop;
    return y;
  }

  // A shared link names a section the page may only now have given an id.
  // The browser's own jump reads the section mid-slide and stops short of it,
  // so the page goes there itself: once the id exists, again after load (the
  // browser jumps once more then), and on a link clicked within the page.
  var landed = false;
  function land(behavior) {
    if (!location.hash) return false;
    var target = document.getElementById(decodeURIComponent(location.hash.slice(1)));
    if (!target) return false;
    window.scrollTo({ top: pageTop(target) - headerHeight() - 20, behavior: behavior || 'instant' });
    return true;
  }

  // While a landing is under way the address waits for it; otherwise it
  // would name each section the page passes on the way. It lets go once the
  // page has loaded, landed (or given up on a section that never came) and
  // stopped scrolling.
  var hold = !!location.hash;
  var loaded = false;
  var patience = false;
  var settle = 0;
  function settleSoon() {
    clearTimeout(settle);
    settle = setTimeout(function () {
      if (!loaded || (!landed && !patience)) return;
      hold = false;
      follow();
    }, 200);
  }

  // The address follows the section at the top of the screen.
  var current = location.hash.slice(1);
  var pending = false;
  function follow() {
    pending = false;
    var line = window.scrollY + headerHeight() + 32;
    var id = '';
    var best = -Infinity;
    for (var i = 0; i < targets.length; i++) {
      var top = pageTop(targets[i]);
      if (top <= line && top > best) {
        best = top;
        id = targets[i].id;
      }
    }
    // Sections side by side share a line: the one already in the address stays.
    var held = current && document.getElementById(current);
    if (held && id) {
      var heldTop = pageTop(held);
      if (heldTop <= line && heldTop >= best - 2) id = current;
    }
    if (id === current) return;
    current = id;
    var url = location.pathname + location.search + (id ? '#' + id : '');
    history.replaceState(history.state, '', url);
  }
  function onScroll() {
    if (hold) return settleSoon();
    if (pending) return;
    pending = true;
    requestAnimationFrame(follow);
  }

  function scan() {
    var headings = document.querySelectorAll('h2, h3');
    for (var i = 0; i < headings.length; i++) mark(headings[i]);
    if (!landed) landed = land();
  }

  function start() {
    addStyle();
    scan();
    if ('MutationObserver' in window) {
      var queued = false;
      new MutationObserver(function () {
        if (queued) return;
        queued = true;
        requestAnimationFrame(function () { queued = false; scan(); });
      }).observe(document.body, { childList: true, subtree: true });
    }
    function onLoad() {
      loaded = true;
      requestAnimationFrame(function () {
        if (landed && hold) land();
        settleSoon();
      });
      setTimeout(function () { patience = true; settleSoon(); }, 3000);
    }
    if (document.readyState === 'complete') onLoad();
    else window.addEventListener('load', onLoad);
    window.addEventListener('hashchange', function () {
      current = location.hash.slice(1);
      hold = true;
      if (land('smooth')) landed = true;
      settleSoon();
    });
    window.addEventListener('scroll', onScroll, { passive: true });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start);
  else start();
})();
