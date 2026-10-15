/* Soundtrack player for the style guide: each track plays in the page over its
 * waveform. The bars already played stay lit; the bars under the playhead rise
 * out of the page towards the viewer (an extruded, glowing section). One track
 * plays at a time. The waveform is a precomputed RMS profile (data-peaks,
 * 0..99), so nothing is decoded until a track is played. */
(function () {
  'use strict';

  var LIFT_SIGMA = 6.5;      // bars either side of the playhead that rise
  var LIFT_SCALE = 0.6;      // how much taller a fully lifted bar gets
  var LIFT_DEPTH = 13;       // px of extrusion at full lift
  var BAR_PITCH = 6;         // px per bar at most (thinner screens merge bars)

  var reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var audio = new Audio();
  audio.preload = 'none';
  var current = null;        // the player whose src is loaded
  var players = [];

  function fmt(t) {
    if (!isFinite(t) || t < 0) t = 0;
    var m = Math.floor(t / 60), s = Math.floor(t % 60);
    return m + ':' + (s < 10 ? '0' : '') + s;
  }

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
  };

  Player.prototype.toggle = function () {
    if (current === this && !audio.paused) { audio.pause(); return; }
    this.load();
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

    // Room on the left for a raised bar at the very start, which moves up and left.
    var padL = Math.ceil(LIFT_DEPTH * 0.6);
    var pitch = (w - padL) / n, bw = Math.max(1.5, pitch * 0.62);
    var mid = h * 0.74;                       // baseline: bars above, a faint reflection below
    // Unlifted bars leave headroom so a fully lifted bar still fits.
    var maxH = (mid - LIFT_DEPTH * 0.75 - 2) / (1 + LIFT_SCALE);
    var head = this.pos * n;                  // playhead in bar units
    var hov = this.hover >= 0 ? this.hover * n : -1;
    var acc = palette.accent, txt = palette.text;
    var hot = mix(acc, palette.light ? [255, 214, 120, 1] : [255, 236, 190, 1], 0.55);
    var side = mix(acc, [0, 0, 0, 1], palette.light ? 0.35 : 0.55);
    var lift = this.lift;

    // A pool of light on the page under the raised section.
    if (lift > 0.01) {
      var gx = padL + head * pitch, gr = pitch * LIFT_SIGMA * 2.6;
      var squash = Math.min(0.32, (h - mid) * 0.92 / gr);   // the pool stays inside the canvas
      var glow = ctx.createRadialGradient(gx, mid, 0, gx, mid, gr);
      glow.addColorStop(0, rgba(acc, (palette.light ? 0.22 : 0.32) * lift));
      glow.addColorStop(1, rgba(acc, 0));
      ctx.save();
      ctx.translate(gx, mid);
      ctx.scale(1, squash);
      ctx.translate(-gx, -mid);
      ctx.fillStyle = glow;
      ctx.fillRect(gx - gr, mid - gr, gr * 2, gr * 2);
      ctx.restore();
    }

    // Draw far-to-near: unlifted bars first, then lifted ones nearest the playhead last.
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
      var x = padL + i * pitch + (pitch - bw) / 2;
      var d = LIFT_DEPTH * L;                 // extrusion depth: the bar comes out of the page
      // Both sides lean in toward the play bar: a bar left of it moves right, a
      // bar right of it moves left, so the raised section meets at the playhead.
      var toward = c < head ? 1 : -1;
      var fx = x + toward * d * 0.55, fy = mid - bh - d * 0.75;
      var ex = toward > 0 ? 0 : bw;            // the outer edge, whose side face shows

      // Reflection under the baseline.
      ctx.fillStyle = played ? rgba(acc, 0.16 + 0.2 * L) : rgba(txt, palette.light ? 0.06 : 0.05);
      ctx.fillRect(x, mid + 3, bw, bh * 0.32);

      if (L > 0.02) {
        // Side and bottom faces of the extrusion, then a soft shadow on the page.
        ctx.fillStyle = rgba(side, 0.9);
        ctx.beginPath();
        ctx.moveTo(fx + ex, fy);
        ctx.lineTo(x + ex, mid - bh);
        ctx.lineTo(x + ex, mid);
        ctx.lineTo(fx + ex, mid - d * 0.75);
        ctx.closePath();
        ctx.fill();
        ctx.beginPath();
        ctx.moveTo(fx, mid - d * 0.75);
        ctx.lineTo(fx + bw, mid - d * 0.75);
        ctx.lineTo(x + bw, mid);
        ctx.lineTo(x, mid);
        ctx.closePath();
        ctx.fill();
        ctx.shadowColor = rgba(acc, palette.light ? 0.45 : 0.75);
        ctx.shadowBlur = 16 * L;
      }

      var g = ctx.createLinearGradient(0, fy, 0, mid);
      if (L > 0.02) {
        g.addColorStop(0, rgba(hot, 1));
        g.addColorStop(1, rgba(mix(hot, acc, 0.6), 1));
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
      roundRect(ctx, fx, fy, bw, mid - d * 0.75 - fy, Math.min(2, bw / 2));
      ctx.fill();
      ctx.shadowBlur = 0;
      ctx.shadowColor = 'transparent';
    }

    // Baseline.
    ctx.fillStyle = rgba(txt, palette.light ? 0.12 : 0.1);
    ctx.fillRect(0, mid + 1, w, 1);

    // The play bar at the current position, standing out nearest the viewer.
    if (this.pos > 0 || lift > 0.01) {
      var px = padL + head * pitch, k = Math.max(lift, 0.35);
      var top = 3, pw = Math.max(2.5, bw * 0.75);
      ctx.save();
      ctx.shadowColor = rgba(acc, palette.light ? 0.6 : 0.9);
      ctx.shadowBlur = 18 * k;
      var pg = ctx.createLinearGradient(0, top, 0, mid + 8);
      pg.addColorStop(0, rgba(hot, k));
      pg.addColorStop(0.75, rgba(mix(hot, acc, 0.5), k));
      pg.addColorStop(1, rgba(acc, 0));
      ctx.fillStyle = pg;
      roundRect(ctx, px - pw / 2, top, pw, mid + 8 - top, pw / 2);
      ctx.fill();
      ctx.shadowBlur = 0;
      ctx.beginPath();                        // the cap
      ctx.arc(px, top + pw / 2, pw * 1.15, 0, Math.PI * 2);
      ctx.fillStyle = rgba(palette.light ? hot : [255, 248, 230, 1], k);
      ctx.fill();
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

  audio.addEventListener('play', function () {
    if (!current) return;
    current.playing = true;
    current.el.classList.add('is-playing');
    current.btn.setAttribute('aria-label', 'Pause ' + current.title);
    animate();
  });
  audio.addEventListener('pause', function () { if (current) current.stopped(); });
  audio.addEventListener('ended', function () {
    if (!current) return;
    current.pos = 0;
    current.updateTime();
    current.stopped();
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
    for (var i = 0; i < els.length; i++) players.push(new Player(els[i]));
    if (!players.length) return;
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
