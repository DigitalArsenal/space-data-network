/* Soundtrack player for the style guide: each track plays in the page over its
 * waveform. The bars already played stay lit; the bars under the playhead stay
 * on the baseline and grow, as if nearer the viewer. One track plays at a time.
 * The deck above the list plays the queue in list order: previous, play or
 * pause, next, shuffle, repeat (off, all, one). Rows reorder by drag (the
 * number), the up and down arrows, or Alt plus an arrow key on the number.
 * Order and modes are kept in localStorage. The waveform is a precomputed RMS
 * profile (data-peaks, 0..99), so nothing is decoded until a track is played. */
(function () {
  'use strict';

  var LIFT_SIGMA = 6.5;      // bars either side of the playhead that rise
  var LIFT_SCALE = 0.6;      // how much taller a fully lifted bar gets
  var LIFT_WIDEN = 0.5;      // how much wider a fully lifted bar gets
  var BAR_PITCH = 6;         // px per bar at most (thinner screens merge bars)
  var RESTART_AFTER = 3;     // seconds: Previous restarts a track played past this
  var STATE_KEY = 'sdn.soundtrack';
  var REPEAT_MODES = ['off', 'all', 'one'];

  var reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var audio = new Audio();
  audio.preload = 'none';
  var current = null;        // the player whose src is loaded
  var players = [];          // the queue, in play order
  var repeat = 'off';        // 'off' | 'all' | 'one'
  var shuffle = false;
  var history = [];          // shuffle: tracks played before the current one
  var round = [];            // shuffle: tracks heard since the queue last wrapped
  var dragSrc = null;        // the player whose number is being dragged
  var dropTarget = null, dropAfter = false;
  var deckEl = null, mainBtn = null, nowEl = null, shuffleBtn = null, repeatBtn = null;

  function fmt(t) {
    if (!isFinite(t) || t < 0) t = 0;
    var m = Math.floor(t / 60), s = Math.floor(t % 60);
    return m + ':' + (s < 10 ? '0' : '') + s;
  }

  function pad2(n) { return (n < 10 ? '0' : '') + n; }

  function parseColor(str) {
    var c = document.createElement('canvas').getContext('2d');
    c.fillStyle = '#000';
    c.fillStyle = str.trim();
    var v = c.fillStyle;
    if (v[0] === '#') {
      return [parseInt(v.slice(1, 3), 16), parseInt(v.slice(3, 5), 16), parseInt(v.slice(5, 7), 16), 1];
    }
    var m = v.match(/[\d.]+/g) || [0, 0, 0, 1];
    return [+m[0], +m[1], +m[2], m[3] === undefined ? 1 : +m[3]];
  }

  var palette = null;
  function readPalette() {
    var cs = getComputedStyle(document.documentElement);
    var light = document.documentElement.getAttribute('data-theme') === 'light';
    palette = {
      light: light,
      accent: parseColor(cs.getPropertyValue('--accent') || '#f5a524'),
      text: parseColor(cs.getPropertyValue('--text') || '#f5f5f7')
    };
  }
  function rgba(c, a) { return 'rgba(' + c[0] + ',' + c[1] + ',' + c[2] + ',' + a + ')'; }
  function mix(a, b, t) { return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t, 1].map(function (v, i) { return i < 3 ? Math.round(v) : v; }); }

  function Player(el) {
    this.el = el;
    this.src = el.getAttribute('data-src');
    this.title = el.getAttribute('data-title') || '';
    this.duration = parseFloat(el.getAttribute('data-duration')) || 0;
    this.peaks = (el.getAttribute('data-peaks') || '').split(',').map(function (v) { return (+v || 0) / 99; });
    this.btn = el.querySelector('.st-play');
    this.wave = el.querySelector('.st-wave');
    this.canvas = this.wave.querySelector('canvas');
    this.timeEl = el.querySelector('.st-time');
    this.grip = el.querySelector('.st-grip');
    this.noEl = el.querySelector('.st-no');
    this.up = el.querySelector('[data-act="up"]');
    this.down = el.querySelector('[data-act="down"]');
    this.pos = 0;            // 0..1 playback position
    this.hover = -1;         // 0..1 hover position, -1 when none
    this.lift = 0;           // eased 0..1: how far the playhead section is raised
    this.playing = false;
    this.bind();
    this.resize();
    this.updateTime();
  }

  Player.prototype.bind = function () {
    var self = this;
    this.btn.addEventListener('click', function () { self.toggle(); });
    this.up.addEventListener('click', function () { nudge(self, -1); });
    this.down.addEventListener('click', function () { nudge(self, 1); });
    this.grip.addEventListener('keydown', function (e) {
      if (!e.altKey) return;
      if (e.key === 'ArrowUp') { e.preventDefault(); nudge(self, -1); }
      else if (e.key === 'ArrowDown') { e.preventDefault(); nudge(self, 1); }
    });
    this.grip.addEventListener('dragstart', function (e) {
      dragSrc = self;
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', self.title);
      e.dataTransfer.setDragImage(self.el, 24, 24);
      self.el.classList.add('is-dragging');
    });
    this.grip.addEventListener('dragend', function () {
      self.el.classList.remove('is-dragging');
      dragSrc = null;
      clearDrop();
    });
    var dragging = false;
    function at(e) {
      var r = self.wave.getBoundingClientRect();
      return Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
    }
    this.wave.addEventListener('pointerdown', function (e) {
      dragging = true;
      try { self.wave.setPointerCapture(e.pointerId); } catch (err) { /* pointer already gone */ }
      self.seek(at(e), true);
    });
    this.wave.addEventListener('pointermove', function (e) {
      if (dragging) self.seek(at(e), false);
      if (e.pointerType === 'mouse') { self.hover = at(e); self.draw(); }
    });
    this.wave.addEventListener('pointerup', function () { dragging = false; });
    this.wave.addEventListener('pointercancel', function () { dragging = false; });
    this.wave.addEventListener('pointerleave', function () { self.hover = -1; self.draw(); });
    this.wave.addEventListener('keydown', function (e) {
      var d = self.duration || 1, step = 0;
      if (e.key === 'ArrowRight' || e.key === 'ArrowUp') step = 5;
      else if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') step = -5;
      else if (e.key === 'PageUp') step = 30;
      else if (e.key === 'PageDown') step = -30;
      else if (e.key === 'Home') { self.seek(0, false); e.preventDefault(); return; }
      else if (e.key === 'End') { self.seek(0.999, false); e.preventDefault(); return; }
      else if (e.key === ' ' || e.key === 'Enter') { self.toggle(); e.preventDefault(); return; }
      else return;
      e.preventDefault();
      self.seek(Math.min(1, Math.max(0, self.pos + step / d)), false);
    });
  };

  Player.prototype.load = function () {
    if (current === this) return;
    if (current) current.stopped();
    current = this;
    audio.src = this.src;
    audio.currentTime = this.pos * (this.duration || 0);
    updateDeck();
  };

  Player.prototype.toggle = function () {
    if (current === this && !audio.paused) { audio.pause(); return; }
    this.load();
    var self = this;
    var p = audio.play();
    if (p && p.catch) p.catch(function () { self.failed(); });
  };

  // Starts this track from its beginning, whatever it was doing.
  Player.prototype.restart = function () {
    this.pos = 0;
    this.load();
    audio.currentTime = 0;
    this.updateTime();
    this.draw();
    var self = this;
    var p = audio.play();
    if (p && p.catch) p.catch(function () { self.failed(); });
  };

  Player.prototype.seek = function (frac, startIfIdle) {
    this.pos = frac;
    if (current === this) {
      var d = audio.duration || this.duration;
      if (d) audio.currentTime = frac * d;
    } else if (startIfIdle) {
      this.toggle();
    }
    this.updateTime();
    this.draw();
  };

  Player.prototype.stopped = function () {
    this.playing = false;
    this.el.classList.remove('is-playing');
    this.btn.setAttribute('aria-label', 'Play ' + this.title);
    animate();
    updateDeck();
  };

  Player.prototype.failed = function () {
    this.stopped();
    this.timeEl.textContent = 'Could not play';
  };

  Player.prototype.updateTime = function () {
    var d = (current === this && audio.duration) || this.duration;
    var t = this.pos * d;
    this.timeEl.textContent = fmt(t) + ' / ' + fmt(d);
    this.wave.setAttribute('aria-valuemax', String(Math.round(d)));
    this.wave.setAttribute('aria-valuenow', String(Math.round(t)));
    this.wave.setAttribute('aria-valuetext', fmt(t) + ' of ' + fmt(d));
  };

  Player.prototype.resize = function () {
    var r = this.wave.getBoundingClientRect();
    var dpr = Math.min(window.devicePixelRatio || 1, 3);
    this.w = Math.max(1, r.width);
    this.h = Math.max(1, r.height);
    this.canvas.width = Math.round(this.w * dpr);
    this.canvas.height = Math.round(this.h * dpr);
    this.dpr = dpr;
    // Merge peaks so a bar is never thinner than BAR_PITCH allows.
    var n = Math.max(24, Math.min(this.peaks.length, Math.floor(this.w / BAR_PITCH)));
    var bars = [], src = this.peaks, k = src.length / n;
    for (var i = 0; i < n; i++) {
      var a = Math.floor(i * k), b = Math.max(a + 1, Math.floor((i + 1) * k)), m = 0;
      for (var j = a; j < b; j++) m = Math.max(m, src[j]);
      bars.push(m);
    }
    this.bars = bars;
    this.draw();
  };

  Player.prototype.draw = function () {
    if (!palette) readPalette();
    var ctx = this.canvas.getContext('2d');
    var dpr = this.dpr, w = this.w, h = this.h, bars = this.bars, n = bars.length;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);

    // Room either side for a widened bar at the very start or end.
    var pad = 3;
    var pitch = (w - pad * 2) / n, bw = Math.max(1.5, pitch * 0.62);
    var mid = h - 3;                          // baseline: every bar stands on it
    // Unlifted bars leave headroom so a fully lifted bar still fits.
    var maxH = (mid - 2) / (1 + LIFT_SCALE);
    var head = this.pos * n;                  // playhead in bar units
    var hov = this.hover >= 0 ? this.hover * n : -1;
    var acc = palette.accent, txt = palette.text;
    var hot = mix(acc, palette.light ? [255, 214, 120, 1] : [255, 236, 190, 1], 0.55);
    var near = mix(hot, [255, 255, 255, 1], palette.light ? 0.2 : 0.45);
    var lift = this.lift;

    // Draw far-to-near: unlifted bars first, then the nearest the playhead last,
    // so a grown bar sits in front of its neighbors.
    var order = [];
    for (var i = 0; i < n; i++) order.push(i);
    order.sort(function (a, b) { return Math.abs(b + 0.5 - head) - Math.abs(a + 0.5 - head); });

    for (var o = 0; o < n; o++) {
      i = order[o];
      var c = i + 0.5;
      var played = c <= head;
      var L = lift * Math.exp(-Math.pow((c - head) / LIFT_SIGMA, 2));
      var hovered = hov >= 0 && c <= hov && !played;
      var bh = Math.max(2, bars[i] * maxH) * (1 + LIFT_SCALE * L);
      var bwL = bw * (1 + LIFT_WIDEN * L);    // grows about its own center
      var x = pad + c * pitch - bwL / 2;
      var top = mid - bh;

      var g = ctx.createLinearGradient(0, top, 0, mid);
      if (L > 0.02) {
        g.addColorStop(0, rgba(mix(hot, near, L), 1));
        g.addColorStop(1, rgba(mix(acc, hot, L), 1));
      } else if (played) {
        g.addColorStop(0, rgba(acc, 0.95));
        g.addColorStop(1, rgba(acc, 0.6));
      } else if (hovered) {
        g.addColorStop(0, rgba(acc, 0.5));
        g.addColorStop(1, rgba(acc, 0.3));
      } else {
        g.addColorStop(0, rgba(txt, palette.light ? 0.3 : 0.32));
        g.addColorStop(1, rgba(txt, palette.light ? 0.16 : 0.14));
      }
      ctx.fillStyle = g;
      roundRect(ctx, x, top, bwL, bh, Math.min(2, bwL / 2));
      ctx.fill();
    }

    // Baseline.
    ctx.fillStyle = rgba(txt, palette.light ? 0.12 : 0.1);
    ctx.fillRect(0, mid + 1, w, 1);

    // The play bar at the current position, standing out nearest the viewer.
    if (this.pos > 0 || lift > 0.01) {
      var px = pad + head * pitch, k = Math.max(lift, 0.35);
      var top = 3;
      ctx.save();
      var pg = ctx.createLinearGradient(0, top, 0, mid);
      pg.addColorStop(0, rgba(hot, k));
      pg.addColorStop(0.75, rgba(mix(hot, acc, 0.5), k));
      pg.addColorStop(1, rgba(acc, 0));
      ctx.fillStyle = pg;
      ctx.fillRect(px - 0.5, top, 1, mid - top);   // one pixel, no cap
      ctx.restore();
    }
  };

  function roundRect(ctx, x, y, w, h, r) {
    ctx.beginPath();
    ctx.moveTo(x + r, y);
    ctx.lineTo(x + w - r, y);
    ctx.quadraticCurveTo(x + w, y, x + w, y + r);
    ctx.lineTo(x + w, y + h);
    ctx.lineTo(x, y + h);
    ctx.lineTo(x, y + r);
    ctx.quadraticCurveTo(x, y, x + r, y);
    ctx.closePath();
  }

  // One animation loop for all players: eases each lift toward its target and
  // follows the playhead while a track plays; it stops when nothing moves.
  var raf = 0, last = 0;
  function animate() { if (!raf) { last = performance.now(); raf = requestAnimationFrame(tick); } }
  function tick(now) {
    raf = 0;
    var dt = Math.min(0.1, (now - last) / 1000); last = now;
    var busy = false;
    for (var i = 0; i < players.length; i++) {
      var p = players[i];
      var target = p.playing ? 1 : 0;
      if (reduceMotion) p.lift = target;
      else p.lift += (target - p.lift) * Math.min(1, dt * 6);
      if (Math.abs(target - p.lift) < 0.002) p.lift = target;
      if (p === current && p.playing && audio.duration) {
        p.pos = audio.currentTime / audio.duration;
        p.updateTime();
      }
      if (p.playing || p.lift !== target || (p.lift > 0 && p.lift < 1)) busy = true;
      if (p.playing || p.lift > 0 || p._wasLifted) p.draw();
      p._wasLifted = p.lift > 0;
    }
    if (busy) raf = requestAnimationFrame(tick);
  }

  // ---------------------------------------------------------------- queue

  // The track that comes after (dir 1) or before (dir -1) the current one.
  // Automatic moves (a track ended) stop at the end of the queue unless
  // repeat is "all"; manual moves always wrap.
  function neighbor(dir, auto) {
    var list = players, n = list.length;
    if (!n) return null;
    var wrap = !auto || repeat === 'all';
    if (shuffle && dir > 0) {
      var pool = list.filter(function (p) { return p !== current && round.indexOf(p) < 0; });
      if (!pool.length) {
        if (!wrap) return null;
        round = [];
        pool = list.filter(function (p) { return p !== current; });
        if (!pool.length) return current || list[0];   // a single track repeats
      }
      return pool[Math.floor(Math.random() * pool.length)];
    }
    var i = current ? list.indexOf(current) : -1;
    var j = i < 0 ? (dir > 0 ? 0 : n - 1) : i + dir;
    if (j < 0 || j >= n) {
      if (!wrap) return null;
      j = (j + n) % n;
    }
    return list[j];
  }

  function go(p, fromHistory) {
    if (!p) return;
    if (!fromHistory && shuffle && current && current !== p) {
      history.push(current);
      if (history.length > 100) history.shift();
    }
    if (shuffle && round.indexOf(p) < 0) round.push(p);
    p.restart();
  }

  function previous() {
    if (!current) return go(neighbor(-1, false));
    if (audio.currentTime > RESTART_AFTER) return current.restart();
    if (shuffle && history.length) return go(history.pop(), true);
    go(neighbor(-1, false));
  }

  function next() { go(neighbor(1, false)); }

  function playPause() {
    if (current) current.toggle();
    else go(neighbor(1, false));
  }

  function setShuffle(on) {
    shuffle = on;
    history = [];
    round = current ? [current] : [];
    saveState();
    updateDeck();
  }

  function cycleRepeat() {
    repeat = REPEAT_MODES[(REPEAT_MODES.indexOf(repeat) + 1) % REPEAT_MODES.length];
    saveState();
    updateDeck();
  }

  // ---------------------------------------------------------------- order

  function renumber() {
    players.forEach(function (p, i) { p.noEl.textContent = pad2(i + 1); });
  }

  // Puts the rows in queue order and renumbers them. Moving a row in the DOM
  // does not interrupt a track that is playing.
  function applyOrder() {
    var box = players[0].el.parentNode;
    players.forEach(function (p) { box.appendChild(p.el); });
    renumber();
    saveState();
  }

  function nudge(p, dir) {
    var i = players.indexOf(p), j = i + dir;
    if (j < 0 || j >= players.length) return;
    players.splice(i, 1);
    players.splice(j, 0, p);
    applyOrder();
    (dir < 0 ? p.up : p.down).focus();
  }

  function place(src, target, after) {
    if (src === target) return;
    players.splice(players.indexOf(src), 1);
    players.splice(players.indexOf(target) + (after ? 1 : 0), 0, src);
    applyOrder();
  }

  function resetOrder() {
    players.sort(function (a, b) { return a.orig - b.orig; });
    applyOrder();
  }

  function playerAt(node) {
    for (var i = 0; i < players.length; i++) if (players[i].el === node) return players[i];
    return null;
  }

  function clearDrop() {
    players.forEach(function (p) { p.el.classList.remove('is-drop-before', 'is-drop-after'); });
    dropTarget = null;
    dropAfter = false;
  }

  function bindDrop(box) {
    box.addEventListener('dragover', function (e) {
      if (!dragSrc) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      var node = e.target.closest ? e.target.closest('.st-track') : null;
      var target = node ? playerAt(node) : null;
      clearDrop();
      if (!target || target === dragSrc) return;
      var r = node.getBoundingClientRect();
      dropAfter = e.clientY > r.top + r.height / 2;
      dropTarget = target;
      target.el.classList.add(dropAfter ? 'is-drop-after' : 'is-drop-before');
    });
    box.addEventListener('dragleave', function (e) {
      if (!box.contains(e.relatedTarget)) clearDrop();
    });
    box.addEventListener('drop', function (e) {
      if (!dragSrc) return;
      e.preventDefault();
      if (dropTarget) place(dragSrc, dropTarget, dropAfter);
      clearDrop();
    });
  }

  // ---------------------------------------------------------------- deck

  function updateDeck() {
    if (!deckEl) return;
    var playing = !!(current && current.playing);
    deckEl.classList.toggle('is-playing', playing);
    mainBtn.setAttribute('aria-label', playing ? 'Pause' : 'Play');
    nowEl.textContent = current ? current.title : 'Pick a track';
    shuffleBtn.setAttribute('aria-pressed', String(shuffle));
    shuffleBtn.classList.toggle('is-on', shuffle);
    repeatBtn.textContent = 'Repeat ' + repeat;
    repeatBtn.classList.toggle('is-on', repeat !== 'off');
  }

  function bindDeck() {
    deckEl = document.querySelector('[data-soundtrack]');
    if (!deckEl) return;
    mainBtn = deckEl.querySelector('[data-act="toggle"]');
    nowEl = deckEl.querySelector('.st-title');
    shuffleBtn = deckEl.querySelector('[data-act="shuffle"]');
    repeatBtn = deckEl.querySelector('[data-act="repeat"]');
    deckEl.querySelector('[data-act="prev"]').addEventListener('click', previous);
    mainBtn.addEventListener('click', playPause);
    deckEl.querySelector('[data-act="next"]').addEventListener('click', next);
    shuffleBtn.addEventListener('click', function () { setShuffle(!shuffle); });
    repeatBtn.addEventListener('click', cycleRepeat);
    deckEl.querySelector('[data-act="reset"]').addEventListener('click', resetOrder);
    var total = players.reduce(function (sum, p) { return sum + p.duration; }, 0);
    deckEl.querySelector('.st-count').textContent = players.length + ' tracks · ' + fmt(total);
  }

  // Order and modes survive a reload. A saved title not in the page is ignored.
  function saveState() {
    try {
      localStorage.setItem(STATE_KEY, JSON.stringify({
        order: players.map(function (p) { return p.title; }),
        repeat: repeat,
        shuffle: shuffle
      }));
    } catch (err) { /* storage off: the page still works */ }
  }

  function loadState() {
    var s = null;
    try { s = JSON.parse(localStorage.getItem(STATE_KEY) || 'null'); } catch (err) { s = null; }
    if (!s) return;
    if (Array.isArray(s.order)) {
      var rank = function (p) { var r = s.order.indexOf(p.title); return r < 0 ? 999 + p.orig : r; };
      players.sort(function (a, b) { return rank(a) - rank(b); });
      applyOrder();
    }
    if (REPEAT_MODES.indexOf(s.repeat) >= 0) repeat = s.repeat;
    shuffle = s.shuffle === true;
    round = current ? [current] : [];
  }

  // ---------------------------------------------------------------- audio

  audio.addEventListener('play', function () {
    if (!current) return;
    current.playing = true;
    current.el.classList.add('is-playing');
    current.btn.setAttribute('aria-label', 'Pause ' + current.title);
    animate();
    updateDeck();
  });
  audio.addEventListener('pause', function () { if (current) current.stopped(); });
  audio.addEventListener('ended', function () {
    if (!current) return;
    var me = current;
    me.pos = 0;
    me.updateTime();
    me.stopped();
    if (repeat === 'one') { me.restart(); return; }
    go(neighbor(1, true));
  });
  audio.addEventListener('loadedmetadata', function () {
    if (!current) return;
    // A seek made before the file loaded is applied now.
    if (current.pos > 0 && audio.duration && Math.abs(audio.currentTime - current.pos * audio.duration) > 0.5) {
      audio.currentTime = current.pos * audio.duration;
    }
    current.updateTime();
  });
  audio.addEventListener('error', function () { if (current && audio.src) current.failed(); });

  function init() {
    var els = document.querySelectorAll('.st-track');
    for (var i = 0; i < els.length; i++) {
      var p = new Player(els[i]);
      p.orig = i;
      players.push(p);
    }
    if (!players.length) return;
    var box = players[0].el.parentNode;
    bindDrop(box);
    bindDeck();
    loadState();
    renumber();
    updateDeck();
    if (window.ResizeObserver) {
      var ro = new ResizeObserver(function (entries) {
        entries.forEach(function (en) {
          players.forEach(function (p) { if (p.wave === en.target) p.resize(); });
        });
      });
      players.forEach(function (p) { ro.observe(p.wave); });
    } else {
      window.addEventListener('resize', function () { players.forEach(function (p) { p.resize(); }); });
    }
    new MutationObserver(function () { readPalette(); players.forEach(function (p) { p.draw(); }); })
      .observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
