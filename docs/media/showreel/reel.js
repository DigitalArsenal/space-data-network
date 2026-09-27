// Showreel player: the hero feature. Starts as soon as the page loads, plays
// once, then rests on the last card. Plays only while on screen, never
// autoplays when reduced motion is requested (the poster shows instead), and
// goes full screen (the stage on desktop so the footer bar stays; the native
// player on iPhone), and the progress bar seeks (drag, click, arrow keys). Muted. Its controls and Play again stay hidden until
// someone moves a mouse over it, taps it or tabs into it.
(function () {
  var video = document.getElementById('reelVideo');
  if (!video) return;
  var stage = video.parentElement;
  var big = stage.querySelector('.reel-big-play');
  var again = stage.querySelector('.reel-again');
  var play = stage.querySelector('.reel-play');
  var fs = stage.querySelector('.reel-fs');
  var track = stage.querySelector('.reel-progress');
  var bar = track.querySelector('span');
  var knob = track.querySelector('.reel-knob');
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

  // Progress and seeking
  function duration() { return isFinite(video.duration) && video.duration > 0 ? video.duration : 45; }
  function clock(s) { s = Math.round(s); return Math.floor(s / 60) + ':' + ('0' + (s % 60)).slice(-2); }
  function tick() {
    var d = duration();
    var f = Math.min(1, video.currentTime / d);
    bar.style.transform = 'scaleX(' + f + ')';
    knob.style.left = (f * 100) + '%';
    track.setAttribute('aria-valuemax', String(Math.round(d)));
    track.setAttribute('aria-valuenow', String(Math.round(video.currentTime)));
    track.setAttribute('aria-valuetext', clock(video.currentTime) + ' of ' + clock(d));
    if (!video.paused) requestAnimationFrame(tick);
  }
  video.addEventListener('play', function () { requestAnimationFrame(tick); });
  video.addEventListener('seeked', tick);
  video.addEventListener('loadedmetadata', tick);

  function seek(time) {
    video.currentTime = Math.max(0, Math.min(duration() - 0.05, time));
    if (stage.classList.contains('is-ended') && video.currentTime < duration() - 0.1) {
      stage.classList.remove('is-ended');
      sync();
    }
    tick();
  }
  function seekTo(clientX) {
    var r = track.getBoundingClientRect();
    seek(Math.max(0, Math.min(1, (clientX - r.left) / r.width)) * duration());
  }
  var seeking = false;
  var resume = false;
  track.addEventListener('pointerdown', function (e) {
    e.preventDefault();
    e.stopPropagation();
    wake();
    seeking = true;
    resume = !video.paused;
    if (resume) video.pause();
    stage.classList.add('is-seeking');
    if (track.setPointerCapture) track.setPointerCapture(e.pointerId);
    seekTo(e.clientX);
  });
  track.addEventListener('pointermove', function (e) {
    if (!seeking) return;
    wake();
    seekTo(e.clientX);
  });
  function endSeek() {
    if (!seeking) return;
    seeking = false;
    stage.classList.remove('is-seeking');
    if (resume) { userPaused = false; start(); }
  }
  track.addEventListener('pointerup', endSeek);
  track.addEventListener('pointercancel', endSeek);
  track.addEventListener('keydown', function (e) {
    var step = { ArrowLeft: -2, ArrowDown: -2, ArrowRight: 2, ArrowUp: 2, PageDown: -10, PageUp: 10 }[e.key];
    if (step) seek(video.currentTime + step);
    else if (e.key === 'Home') seek(0);
    else if (e.key === 'End') seek(duration());
    else return;
    e.preventDefault();
  });

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
