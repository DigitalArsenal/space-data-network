// Shared page behavior for spacedatanetwork.org: background video, copy buttons, mobile menu.
(function () {
  var video = document.getElementById('bgVideo');
  if (video && !matchMedia('(prefers-reduced-motion: reduce)').matches) {
    video.src = 'space-bg.mp4';
    video.addEventListener('loadeddata', function () { video.classList.add('loaded'); });
    video.play().catch(function () {});
  }
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
