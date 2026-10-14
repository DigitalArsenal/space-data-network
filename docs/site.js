// Shared page behavior for spacedatanetwork.org: background video, copy buttons.
// The top bar, its menu and the theme switch are the shared sdn-chrome kit.
(function () {
  var video = document.getElementById('bgVideo');
  if (video && !window.SDN_EARTH && !matchMedia('(prefers-reduced-motion: reduce)').matches) {
    video.src = 'space-bg.mp4';
    video.addEventListener('loadeddata', function () { video.classList.add('loaded'); });
    video.play().catch(function () {});
  }
  // No scroll reveals. Content used to start transparent and 28 px low and
  // rise into place as it scrolled into view; over the animated Earth that
  // showed the orbits through half-faded headings and cards and moved the
  // headings after the reader had found them. Content is now painted where it
  // belongs, fully opaque, from the first frame.
  document.querySelectorAll('.copy').forEach(function (button) {
    button.addEventListener('click', function () {
      var text = button.parentElement.querySelector('code').textContent;
      navigator.clipboard.writeText(text).then(function () {
        button.textContent = 'Copied';
        setTimeout(function () { button.textContent = 'Copy'; }, 1600);
      });
    });
  });
})();
