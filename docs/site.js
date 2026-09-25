// Shared page behavior for spacedatanetwork.org: background video, scroll reveals, copy buttons, mobile menu.
(function () {
  var video = document.getElementById('bgVideo');
  if (video && !window.SDN_EARTH && !matchMedia('(prefers-reduced-motion: reduce)').matches) {
    video.src = 'space-bg.mp4';
    video.addEventListener('loadeddata', function () { video.classList.add('loaded'); });
    video.play().catch(function () {});
  }
  // Content rises into place as it scrolls into view; siblings arrive in sequence.
  if ('IntersectionObserver' in window && !matchMedia('(prefers-reduced-motion: reduce)').matches) {
    var items = document.querySelectorAll('main section:not(.hero):not(.page-hero) :is(.head, .card, .figure, .chapter, .photo-grid figure, .steps li, .table-wrap, .note, .actions)');
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target); }
      });
    }, { rootMargin: '0px 0px -8% 0px' });
    items.forEach(function (el) {
      var i = Array.prototype.indexOf.call(el.parentElement.children, el);
      el.style.setProperty('--reveal-delay', Math.min(i, 5) * 70 + 'ms');
      el.classList.add('reveal');
      io.observe(el);
    });
  }
  // Light / dark theme: dark by default, the choice is remembered on this browser.
  document.querySelectorAll('.theme-toggle').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var next = document.documentElement.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
      document.documentElement.setAttribute('data-theme', next);
      try { localStorage.setItem('sdn-theme', next); } catch (e) {}
    });
  });
  document.querySelectorAll('.copy').forEach(function (button) {
    button.addEventListener('click', function () {
      var text = button.parentElement.querySelector('code').textContent;
      navigator.clipboard.writeText(text).then(function () {
        button.textContent = 'Copied';
        setTimeout(function () { button.textContent = 'Copy'; }, 1600);
      });
    });
  });
  document.querySelectorAll('.nav-menu a').forEach(function (a) {
    a.addEventListener('click', function () { a.closest('details').removeAttribute('open'); });
  });
})();
