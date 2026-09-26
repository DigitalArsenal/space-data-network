// Showreel player: the hero feature. Starts as soon as the page loads, plays
// once, then rests on the last card. Plays only while on screen, never
// autoplays when reduced motion is requested (the poster shows instead), and
// goes full screen (the stage on desktop so the footer bar stays; the native
// player on iPhone). Muted. Its controls and Play again stay hidden until
// someone moves a mouse over it, taps it or tabs into it.
(function () {
  var video = document.getElementById('reelVideo');
  if (!video) return;
  var stage = video.parentElement;
  var big = stage.querySelector('.reel-big-play');
  var again = stage.querySelector('.reel-again');
  var play = stage.querySelector('.reel-play');
  var fs = stage.querySelector('.reel-fs');
  var bar = stage.querySelector('.reel-progress span');
  var reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
  var userPaused = reduced;

  // The poster is a mid-reel frame, so it only shows when the reel will not
  // start on its own; otherwise it would flash before the opening black frame.
  function showPoster() {
    var src = video.getAttribute('data-poster');
    if (src && !video.getAttribute('poster')) video.setAttribute('poster', src);
    stage.classList.remove('is-starting');
  }
  if (reduced) showPoster(); else stage.classList.add('is-starting');
  video.addEventListener('playing', function () {
    stage.classList.remove('is-starting');
    stage.classList.add('has-played');
  });

  // Controls wake on interaction and sleep again after a short idle.
  var idle = 0;
  var wasAwake = false;
  var pointer = 'mouse';
  function wake() {
    stage.classList.add('is-awake');
    clearTimeout(idle);
    idle = setTimeout(function () { stage.classList.remove('is-awake'); }, 2500);
  }
  function sleep() {
    clearTimeout(idle);
    stage.classList.remove('is-awake');
  }
  stage.addEventListener('pointermove', function (e) { if (e.pointerType === 'mouse') wake(); });
  stage.addEventListener('pointerleave', function (e) { if (e.pointerType === 'mouse') sleep(); });
  stage.addEventListener('pointerdown', function (e) {
    pointer = e.pointerType;
    wasAwake = stage.classList.contains('is-awake');
    wake();
  });
  stage.addEventListener('keydown', wake);

  function sync() {
    var playing = !video.paused;
    stage.classList.toggle('is-playing', playing);
    play.setAttribute('aria-pressed', String(playing));
    play.setAttribute('aria-label', playing ? 'Pause the showreel' : 'Play the showreel');
  }
  function start() {
    var p = video.play();
    if (p && p.catch) p.catch(function () { showPoster(); sync(); });
  }
  function toggle() {
    if (video.paused) { userPaused = false; start(); } else { userPaused = true; video.pause(); }
  }
  big.addEventListener('click', toggle);
  again.addEventListener('click', function () {
    video.currentTime = 0;
    userPaused = false;
    start();
  });
  video.addEventListener('ended', function () {
    userPaused = true; // do not replay just because it scrolls back into view
    stage.classList.add('is-ended');
    sync();
    tick();
  });
  video.addEventListener('play', function () { stage.classList.remove('is-ended'); });
  play.addEventListener('click', toggle);
  // A tap on sleeping controls only wakes them; a click (or a tap once awake) plays or pauses.
  video.addEventListener('click', function () {
    if (stage.classList.contains('is-ended')) return;
    if (pointer !== 'mouse' && !wasAwake) return;
    toggle();
  });
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

  // Progress
  function tick() {
    var d = video.duration || 15;
    bar.style.transform = 'scaleX(' + (video.currentTime / d) + ')';
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
  } else if (!userPaused) {
    start();
  }
  sync();
  tick();
})();
