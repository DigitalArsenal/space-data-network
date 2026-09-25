// Showreel player: plays only while on screen, never autoplays when reduced
// motion is requested, and lets chapter buttons seek. Muted, no sound track.
(function () {
  var video = document.getElementById('reelVideo');
  if (!video) return;
  var stage = video.parentElement;
  var toggle = stage.querySelector('.reel-toggle');
  var bar = stage.querySelector('.reel-progress span');
  var chapters = Array.prototype.slice.call(document.querySelectorAll('.reel-chapters button'));
  var reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
  var userPaused = reduced;
  var visible = false;

  function sync() {
    var playing = !video.paused;
    stage.classList.toggle('is-playing', playing);
    toggle.setAttribute('aria-pressed', String(playing));
    toggle.setAttribute('aria-label', playing ? 'Pause the showreel' : 'Play the showreel');
  }
  function play() {
    var p = video.play();
    if (p && p.catch) p.catch(function () { sync(); });
  }

  toggle.addEventListener('click', function () {
    if (video.paused) { userPaused = false; play(); } else { userPaused = true; video.pause(); }
  });
  video.addEventListener('play', sync);
  video.addEventListener('pause', sync);

  chapters.forEach(function (btn) {
    btn.addEventListener('click', function () {
      video.currentTime = Number(btn.getAttribute('data-t')) + 0.01;
      userPaused = false;
      play();
    });
  });

  var starts = chapters.map(function (b) { return Number(b.getAttribute('data-t')); });
  function tick() {
    var d = video.duration || 15;
    bar.style.transform = 'scaleX(' + (video.currentTime / d) + ')';
    var active = 0;
    for (var i = 0; i < starts.length; i++) if (video.currentTime >= starts[i]) active = i;
    chapters.forEach(function (b, i) { b.classList.toggle('is-active', i === active); });
    if (!video.paused) requestAnimationFrame(tick);
  }
  video.addEventListener('play', function () { requestAnimationFrame(tick); });
  video.addEventListener('seeked', tick);

  if ('IntersectionObserver' in window) {
    new IntersectionObserver(function (entries) {
      visible = entries[0].isIntersecting;
      if (visible && !userPaused) play();
      if (!visible && !video.paused) video.pause();
    }, { threshold: 0.35 }).observe(stage);
  }
  sync();
})();
