// Section links for every page of spacedatanetwork.org (owner 2026-10-05: "all
// sections on all pages to have section links so that I can copy a URL and
// send people to sections").
//
// Every h2 and h3 in the page content gets a stable id: its section's id when
// it heads one (existing links keep working), its own id when it has one,
// else a slug of its text. A "#" beside the heading copies the section's URL
// and puts it in the address bar. A URL that names a section lands on it,
// clear of the sticky top bar, including on pages whose content arrives later.
(function () {
  'use strict';
  if (window.__sdnSectionLinks) return;
  window.__sdnSectionLinks = true;

  var SKIP = '.sdn-header, .sdn-footer, [data-sdn-stack], nav, .ai-credits, [data-section-links="off"]';
  var used = Object.create(null);

  var css =
    '.sdn-anchor{display:inline-flex;align-items:center;justify-content:center;margin-left:.4em;padding:0 .25em;' +
    'border-radius:4px;color:inherit;opacity:0;text-decoration:none;font-weight:400;font-size:.75em;line-height:1;' +
    'vertical-align:middle;cursor:pointer;transition:opacity .15s,color .15s,background .15s;user-select:none;-webkit-user-select:none}' +
    '.sdn-anchored:hover>.sdn-anchor,.sdn-anchor:focus-visible,.sdn-anchor.copied{opacity:.55}' +
    '.sdn-anchor:hover{opacity:1;color:#f5a524;background:rgba(245,165,36,.12)}' +
    '.sdn-anchor.copied{opacity:1;color:#f5a524}' +
    '@media (hover:none){.sdn-anchor{opacity:.35}}' +
    '.sdn-anchor-toast{position:fixed;z-index:2147483600;padding:6px 10px;border-radius:6px;background:#f5a524;color:#000;' +
    'font:600 13px/1.2 system-ui,-apple-system,sans-serif;pointer-events:none;transform:translate(-50%,-130%);' +
    'animation:sdn-anchor-toast 1.6s ease forwards}' +
    '@keyframes sdn-anchor-toast{0%{opacity:0}10%{opacity:1}75%{opacity:1}100%{opacity:0}}' +
    '[data-sdn-section]{scroll-margin-top:calc(var(--sdn-stack-header-height,52px) + 20px)}';

  function addStyle() {
    var style = document.createElement('style');
    style.textContent = css;
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
    while (used[id] || (document.getElementById(id) && !used[id])) id = base + '-' + n++;
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

  function headingText(heading) {
    var clone = heading.cloneNode(true);
    var anchors = clone.querySelectorAll('.sdn-anchor');
    for (var i = 0; i < anchors.length; i++) anchors[i].remove();
    return clone.textContent;
  }

  function anchor(heading) {
    if (heading.hasAttribute('data-sdn-anchored') || heading.closest(SKIP)) return;
    heading.setAttribute('data-sdn-anchored', '');
    var section = owningSection(heading);
    var target = section || heading;
    var id = target.id;
    if (id) used[id] = true;
    else target.id = id = unique(slug(headingText(heading)));
    target.setAttribute('data-sdn-section', '');
    heading.classList.add('sdn-anchored');
    var a = document.createElement('a');
    a.className = 'sdn-anchor';
    a.href = '#' + id;
    a.textContent = '#';
    a.setAttribute('aria-label', 'Copy a link to this section');
    a.title = 'Copy a link to this section';
    a.addEventListener('click', function (event) {
      event.preventDefault();
      var url = location.href.split('#')[0] + '#' + id;
      history.replaceState(null, '', '#' + id);
      target.scrollIntoView({ behavior: 'smooth', block: 'start' });
      copy(url, a);
    });
    heading.appendChild(a);
  }

  function copy(url, from) {
    var done = function () { toast(from); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(url).then(done, done);
    } else {
      done();
    }
  }

  function toast(from) {
    from.classList.add('copied');
    setTimeout(function () { from.classList.remove('copied'); }, 1600);
    var r = from.getBoundingClientRect();
    var t = document.createElement('div');
    t.className = 'sdn-anchor-toast';
    t.textContent = 'Link copied';
    t.style.left = r.left + r.width / 2 + 'px';
    t.style.top = r.top + 'px';
    document.body.appendChild(t);
    setTimeout(function () { t.remove(); }, 1700);
  }

  // A shared link names a section the page may only now have given an id.
  var landed = false;
  function land() {
    if (landed || !location.hash) return;
    var target = document.getElementById(decodeURIComponent(location.hash.slice(1)));
    if (!target) return;
    landed = true;
    target.scrollIntoView({ block: 'start' });
  }

  function scan() {
    var headings = document.querySelectorAll('h2, h3');
    for (var i = 0; i < headings.length; i++) anchor(headings[i]);
    land();
  }

  function start() {
    addStyle();
    scan();
    if ('MutationObserver' in window) {
      var pending = false;
      new MutationObserver(function () {
        if (pending) return;
        pending = true;
        requestAnimationFrame(function () { pending = false; scan(); });
      }).observe(document.body, { childList: true, subtree: true });
    }
    window.addEventListener('hashchange', function () { landed = false; land(); });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start);
  else start();
})();
