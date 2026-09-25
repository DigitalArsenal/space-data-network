// Showreel player: plays only while on screen, never autoplays when reduced
// motion is requested, seeks by chapter, and goes full screen (the stage on
// desktop so the footer bar stays; the native player on iPhone). Muted.
(function () {
  var video = document.getElementById('reelVideo');
  if (!video) return;
  var stage = video.parentElement;
  var big = stage.querySelector('.reel-big-play');
  var play = stage.querySelector('.reel-play');
  var fs = stage.querySelector('.reel-fs');
  var bar = stage.querySelector('.reel-progress span');
  var chapters = Array.prototype.slice.call(stage.querySelectorAll('.reel-chapters button'));
  var reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
  var userPaused = reduced;

  function sync() {
    var playing = !video.paused;
    stage.classList.toggle('is-playing', playing);
    play.setAttribute('aria-pressed', String(playing));
    play.setAttribute('aria-label', playing ? 'Pause the showreel' : 'Play the showreel');
  }
  function start() {
    var p = video.play();
    if (p && p.catch) p.catch(function () { sync(); });
  }
  function toggle() {
    if (video.paused) { userPaused = false; start(); } else { userPaused = true; video.pause(); }
  }
  big.addEventListener('click', toggle);
  play.addEventListener('click', toggle);
  video.addEventListener('click', toggle);
  video.addEventListener('play', sync);
  video.addEventListener('pause', sync);

  // Full screen
  function fullElement() { return document.fullscreenElement || document.webkitFullscreenElement; }
  function toggleFull() {
    if (fullElement()) {
      (document.exitFullscreen || document.webkitExitFullscreen).call(document);
    } else if (stage.requestFullscreen) {
      stage.requestFullscreen();
    } else if (stage.webkitRequestFullscreen) {
      stage.webkitRequestFullscreen();
    } else if (video.webkitEnterFullscreen) {
      video.webkitEnterFullscreen();
    }
    if (video.paused) { userPaused = false; start(); }
  }
  function syncFull() {
    var full = fullElement() === stage;
    stage.classList.toggle('is-full', full);
    fs.setAttribute('aria-label', full ? 'Exit full screen' : 'Full screen');
  }
  fs.addEventListener('click', toggleFull);
  video.addEventListener('dblclick', toggleFull);
  document.addEventListener('fullscreenchange', syncFull);
  document.addEventListener('webkitfullscreenchange', syncFull);

  // Chapters and progress
  chapters.forEach(function (btn) {
    btn.addEventListener('click', function () {
      video.currentTime = Number(btn.getAttribute('data-t')) + 0.01;
      userPaused = false;
      start();
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
      var visible = entries[0].isIntersecting;
      if (visible && !userPaused) start();
      if (!visible && !video.paused && !fullElement()) video.pause();
    }, { threshold: 0.35 }).observe(stage);
  }
  sync();
  tick();
})();
