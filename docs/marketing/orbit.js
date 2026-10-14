// The orbit stage of spacedatanetwork.org/marketing.html: one 2D canvas over
// the scroll-driven Earth (earth.js). As its track scrolls past, the modules
// ride one orbit per discipline, gather into their disciplines, then light up
// bundle by bundle. It draws only while the track is on screen. After the
// first render it never adds or removes DOM nodes (text changes go through
// existing text nodes), so it cannot disturb section-links.js.
//
//   SDNMarketingOrbit({ catalog, open: function (moduleId) {}, reduced: bool })
window.SDNMarketingOrbit = function (opts) {
  'use strict';
  var C = opts.catalog, REDUCED = opts.reduced, root = document.documentElement;
  var q = function (sel) { return document.querySelector('[data-mk="' + sel + '"]'); };
  var track = q('orbit-track'), canvas = q('orbit-canvas'), ctx = canvas.getContext('2d');
  var phases = q('orbit-phases').querySelectorAll('li');
  var N = C.categories.length, TAN = Math.tan(22 * Math.PI / 180);
  var W = 0, H = 0, dpr = 1, cx = 0, cy = 0, earthR = 60, narrow = false, col = {}, glow = null, font = '', dark = true;

  // Text nodes made once and rewritten in place.
  var tip = document.createElement('div'), tipText = document.createTextNode('');
  tip.className = 'mk-orbit-tip';
  tip.appendChild(tipText);
  canvas.parentNode.appendChild(tip);
  var cap = document.createElement('span'), capB = document.createElement('b');
  var capStrong = document.createTextNode(''), capRest = document.createTextNode('');
  capB.appendChild(capStrong);
  cap.appendChild(capB);
  cap.appendChild(capRest);
  q('orbit-caption').appendChild(cap);

  var nodes = C.modules.map(function (m) {
    var k = C.categories.findIndex(function (c) { return c.id === m.category; });
    return { m: m, k: k, j: 0, n: 1, a0: Math.random() * Math.PI * 2, x: 0, y: 0, front: true };
  });
  var rings = C.categories.map(function (c, k) {
    var mine = nodes.filter(function (n) { return n.k === k; });
    mine.forEach(function (n, j) { n.j = j; n.n = mine.length; });
    return { tilt: (-24 + ((k * 37) % 48)) * Math.PI / 180, sq: 0.3 + ((k * 0.11) % 0.18), count: mine.length, r: 0, w: 0, cxk: 0, cyk: 0, rc: 0 };
  });

  // The keyframe distance makes the planet about a seventh of the short side
  // across; the orbits ring it.
  var want = Math.min(innerWidth, innerHeight) * (innerWidth < 700 ? 0.15 : 0.13);
  var dist = 1 / Math.sin(Math.atan(want / (innerHeight / 2) * TAN));
  document.querySelectorAll('[data-mk="orbit-kf"]').forEach(function (k) {
    k.setAttribute('data-earth', dist.toFixed(2) + ' 30 18 0 0 70 16');
  });

  function colors() {
    var cs = getComputedStyle(root);
    ['--text', '--accent', '--border-strong', '--panel-solid', '--bg', '--font-sans'].forEach(function (v) { col[v] = cs.getPropertyValue(v).trim(); });
    font = col['--font-sans'];
    dark = root.getAttribute('data-theme') !== 'light';
    glow = document.createElement('canvas');
    glow.width = glow.height = 64;
    var g = glow.getContext('2d'), rg = g.createRadialGradient(32, 32, 0, 32, 32, 32);
    rg.addColorStop(0, col['--accent']);
    rg.addColorStop(1, 'rgba(0,0,0,0)');
    g.globalAlpha = 0.55;
    g.fillStyle = rg;
    g.fillRect(0, 0, 64, 64);
  }
  function layout() {
    var r = canvas.getBoundingClientRect();
    W = r.width; H = r.height; dpr = Math.min(devicePixelRatio || 1, 2);
    canvas.width = Math.round(W * dpr); canvas.height = Math.round(H * dpr);
    narrow = W < 640;
    var head = parseFloat(getComputedStyle(root).getPropertyValue('--sdn-stack-header-height')) || 52;
    cx = W / 2; cy = innerHeight / 2 - head;
    earthR = Math.tan(Math.asin(1 / dist)) / TAN * innerHeight / 2;
    var rMax = Math.min(W / 2 - 14, (H / 2 - 40) / 0.5), rMin = earthR * 1.3;
    var s = Math.max(0.62, Math.min(1, Math.min(W, H) / 820));
    // Disciplines sit on an ellipse around the planet, clear of the heading
    // above and the caption below.
    var Ry = Math.max(earthR + 50, Math.min(cy - (narrow ? 150 : 170), H - cy - (narrow ? 175 : 165)));
    var Rx = narrow ? W / 2 - 52 : Math.min(W / 2 - 190, Ry * 1.75);
    rings.forEach(function (g, k) {
      g.r = rMin + (rMax - rMin) * (N > 1 ? k / (N - 1) : 0);
      g.w = 0.55 * Math.pow(g.r / rMin, -1.5);           // Kepler: outer orbits are slower
      var dir = -Math.PI / 2 + k * 2 * Math.PI / N;
      g.cxk = cx + Math.cos(dir) * Rx;
      g.cyk = cy + Math.sin(dir) * Ry;
      g.rc = (14 + g.count * 3.2) * s;
    });
  }

  var p = 0, t = 0, last = 0, running = false, hover = null, capKey = '', barState = [];
  function clamp(x) { return x < 0 ? 0 : x > 1 ? 1 : x; }
  function ease(x) { x = clamp(x); return x * x * (3 - 2 * x); }
  function progress() {
    if (REDUCED) return 0.5;
    var r = track.getBoundingClientRect();
    return clamp(-r.top / Math.max(1, r.height - innerHeight));
  }
  function caption(key, strong, rest) {
    if (key === capKey) return;
    capKey = key;
    capStrong.data = strong;
    capRest.data = rest;
    if (REDUCED) return;
    cap.classList.remove('mk-swap');
    void cap.offsetWidth;
    cap.classList.add('mk-swap');
  }
  function hoverCat() { return hover ? hover.k : -1; }

  function draw() {
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, W, H);
    var mix = REDUCED ? 1 : ease((p - 0.26) / 0.14);   // orbits -> disciplines
    var activeCat = -1, activeB = -1, inB = null, PI2 = Math.PI * 2;
    if (!REDUCED && p >= 0.4 && p < 0.7) activeCat = Math.min(N - 1, Math.floor((p - 0.4) / 0.3 * N));
    if (!REDUCED && p >= 0.7) {
      activeB = Math.min(C.bundles.length - 1, Math.floor((p - 0.7) / 0.3 * C.bundles.length));
      inB = {};
      C.bundles[activeB].modules.forEach(function (id) { inB[id] = 1; });
    }

    if (!root.classList.contains('has-earth')) {     // a stand-in core without the WebGL Earth
      ctx.globalAlpha = 1;
      ctx.fillStyle = col['--panel-solid'];
      ctx.strokeStyle = col['--border-strong'];
      ctx.lineWidth = 1.5;
      ctx.beginPath(); ctx.arc(cx, cy, earthR, 0, PI2); ctx.fill(); ctx.stroke();
      ctx.strokeStyle = col['--accent']; ctx.lineWidth = 2.5; ctx.lineCap = 'round';
      ctx.beginPath(); ctx.arc(cx, cy, earthR, Math.PI / 2, Math.PI * 1.5); ctx.stroke();
    }

    // Orbits: the far half clipped by the planet's disk, then the near half.
    if (mix < 1) {
      ctx.lineWidth = 1;
      ctx.strokeStyle = col['--border-strong'];
      rings.forEach(function (g, k) {
        ctx.globalAlpha = (1 - mix) * (k === hoverCat() ? 1 : 0.8);
        ctx.save();
        ctx.beginPath(); ctx.rect(0, 0, W, H); ctx.arc(cx, cy, earthR, 0, PI2, true); ctx.clip('evenodd');
        ctx.beginPath(); ctx.ellipse(cx, cy, g.r, g.r * g.sq, g.tilt, Math.PI, PI2); ctx.stroke();
        ctx.restore();
        ctx.beginPath(); ctx.ellipse(cx, cy, g.r, g.r * g.sq, g.tilt, 0, Math.PI); ctx.stroke();
      });
    }

    // Positions: on the orbit, easing into the discipline's cluster.
    nodes.forEach(function (n, i) {
      var g = rings[n.k], a = n.a0 + t * g.w, ca = Math.cos(a), sa = Math.sin(a), ct = Math.cos(g.tilt), st = Math.sin(g.tilt);
      var ex = g.r * ca, ey = g.r * g.sq * sa;
      var ox = cx + ex * ct - ey * st, oy = cy + ex * st + ey * ct;
      var b = n.j / n.n * PI2 + t * 0.12;
      var e = ease(mix * 1.25 - (i % 12) * 0.02);
      n.a = a;
      n.x = ox + (g.cxk + Math.cos(b) * g.rc - ox) * e;
      n.y = oy + (g.cyk + Math.sin(b) * g.rc - oy) * e;
      n.front = e > 0.5 || sa > 0 || Math.hypot(n.x - cx, n.y - cy) >= earthR;
    });

    // Comet tails while on orbit.
    if (mix < 1) {
      ctx.strokeStyle = col['--text'];
      ctx.lineWidth = 1.6;
      nodes.forEach(function (n) {
        if (!n.front) return;
        var g = rings[n.k];
        for (var s = 0; s < 3; s++) {
          ctx.globalAlpha = (1 - mix) * (0.35 - s * 0.1);
          ctx.beginPath(); ctx.ellipse(cx, cy, g.r, g.r * g.sq, g.tilt, n.a - 0.12 * (s + 1), n.a - 0.12 * s); ctx.stroke();
        }
      });
    }

    // Spokes inside each discipline.
    if (mix > 0) {
      ctx.lineWidth = 1;
      nodes.forEach(function (n) {
        var g = rings[n.k], hot = n.k === activeCat || n.k === hoverCat();
        ctx.globalAlpha = mix * (inB ? 0.12 : hot ? 0.7 : 0.22);
        ctx.strokeStyle = hot && !inB ? col['--accent'] : col['--border-strong'];
        ctx.beginPath(); ctx.moveTo(g.cxk, g.cyk); ctx.lineTo(n.x, n.y); ctx.stroke();
      });
    }
    // Bundle links run from the planet's limb to each member.
    if (inB) {
      ctx.setLineDash([4, 6]);
      ctx.lineDashOffset = -t * 24;
      ctx.strokeStyle = col['--accent'];
      ctx.lineWidth = 1.2;
      ctx.globalAlpha = 0.65;
      nodes.forEach(function (n) {
        if (!inB[n.m.id]) return;
        var dx = n.x - cx, dy = n.y - cy, d = Math.hypot(dx, dy) || 1;
        var sx = cx + dx / d * earthR, sy = cy + dy / d * earthR;
        ctx.beginPath(); ctx.moveTo(sx, sy);
        ctx.quadraticCurveTo((sx + n.x) / 2 - dy / d * 18, (sy + n.y) / 2 + dx / d * 18, n.x, n.y);
        ctx.stroke();
      });
      ctx.setLineDash([]);
    }

    // Satellites.
    nodes.forEach(function (n) {
      var lit = (inB && inB[n.m.id]) || (!inB && n.k === activeCat) || n === hover;
      ctx.globalAlpha = !n.front ? 0.12 : inB && !lit ? 0.25 : 1;
      if (lit && n.front && dark) ctx.drawImage(glow, n.x - 16, n.y - 16, 32, 32);
      ctx.fillStyle = lit ? col['--accent'] : col['--text'];
      ctx.beginPath(); ctx.arc(n.x, n.y, (lit ? 4.2 : 3) * (narrow ? 0.9 : 1), 0, PI2); ctx.fill();
      if (n === hover) {
        ctx.strokeStyle = col['--accent']; ctx.lineWidth = 1.5;
        ctx.beginPath(); ctx.arc(n.x, n.y, 9, 0, PI2); ctx.stroke();
      }
    });

    // Discipline names once the clusters form (phones: the lit one only),
    // each on a knock-out halo so bundle links and satellites never cross it.
    if (mix > 0.5) {
      ctx.textBaseline = 'middle';
      ctx.textAlign = 'center';
      ctx.font = '600 ' + (narrow ? 11 : 13) + 'px ' + font;
      ctx.lineJoin = 'round';
      ctx.lineWidth = 6;
      ctx.strokeStyle = col['--bg'];
      rings.forEach(function (g, k) {
        var hot = k === activeCat || k === hoverCat();
        if (narrow && !hot && !REDUCED) return;
        var a = (mix - 0.5) * 2, name = C.categories[k].name, ly = g.cyk + g.rc + 16, cut = name.lastIndexOf(' ', 16);
        var lines = narrow && name.length > 18 && cut > 0 ? [name.slice(0, cut), name.slice(cut + 1)] : [name];
        ctx.globalAlpha = a * 0.9;
        lines.forEach(function (s, i) { ctx.strokeText(s, g.cxk, ly + i * 14); });
        ctx.globalAlpha = a * (inB ? 0.7 : hot ? 1 : 0.75);
        ctx.fillStyle = hot ? col['--accent'] : col['--text'];
        lines.forEach(function (s, i) { ctx.fillText(s, g.cxk, ly + i * 14); });
      });
    }
    ctx.globalAlpha = 1;

    if (inB) {
      var B = C.bundles[activeB];
      caption('b' + activeB, B.name + ' bundle', ' · ' + B.modules.length + ' modules · ' + B.tagline);
    } else if (activeCat >= 0) {
      caption('c' + activeCat, C.categories[activeCat].name, ' · ' + C.categories[activeCat].blurb);
    } else if (REDUCED) {
      caption('r', C.modules.length + ' modules', ' in ' + N + ' disciplines. Select any satellite for its details.');
    } else {
      caption('o', C.modules.length + ' modules', ' on ' + N + ' orbits, one for each discipline. ' + (narrow ? 'Tap' : 'Select') + ' any satellite for its details.');
    }
    var fills = [clamp(p / 0.4), clamp((p - 0.4) / 0.3), clamp((p - 0.7) / 0.3)];
    phases.forEach(function (li, i) {
      var v = (REDUCED ? (i === 1 ? 1 : 0) : fills[i]).toFixed(3);
      var on = REDUCED ? i === 1 : (i === 0 ? p < 0.4 : i === 1 ? p >= 0.4 && p < 0.7 : p >= 0.7);
      if (barState[i] === v + on) return;
      barState[i] = v + on;
      li.lastElementChild.style.setProperty('--p', v);
      li.classList.toggle('on', on);
    });
  }

  function frame(now) {
    if (!running) return;
    var dt = Math.min(0.05, (now - last) / 1000 || 0);
    last = now;
    t += dt;
    var goal = progress();
    p += (goal - p) * (1 - Math.exp(-dt * 7));
    if (Math.abs(goal - p) < 0.0005) p = goal;
    draw();
    requestAnimationFrame(frame);
  }
  function start() { if (running || REDUCED) return; running = true; last = performance.now(); requestAnimationFrame(frame); }
  function stop() { running = false; }

  function pick(e) {
    var r = canvas.getBoundingClientRect(), x = e.clientX - r.left, y = e.clientY - r.top, best = null, bd = 18;
    nodes.forEach(function (n) { var d = Math.hypot(n.x - x, n.y - y); if (n.front && d < bd) { bd = d; best = n; } });
    return best;
  }
  canvas.addEventListener('pointermove', function (e) {
    var n = pick(e);
    if (n === hover) return;
    hover = n;
    canvas.style.cursor = n ? 'pointer' : '';
    if (n) {
      tipText.data = n.m.name;
      var tx = Math.max(8, Math.min(W - 8 - tip.offsetWidth, n.x - tip.offsetWidth / 2));
      tip.style.transform = 'translate(' + tx + 'px,' + (n.y - 42) + 'px)';
    }
    tip.classList.toggle('on', !!n);
    if (REDUCED) draw();
  });
  canvas.addEventListener('pointerleave', function () { hover = null; tip.classList.remove('on'); if (REDUCED) draw(); });
  canvas.addEventListener('click', function (e) { var n = pick(e); if (n) opts.open(n.m.id); });

  colors();
  layout();
  p = progress();
  draw();
  var visible = false;
  if ('IntersectionObserver' in window) {
    new IntersectionObserver(function (e) {
      visible = e[e.length - 1].isIntersecting;
      if (visible && !document.hidden) start(); else stop();
    }).observe(track);
  } else start();
  document.addEventListener('visibilitychange', function () { if (document.hidden) stop(); else if (visible) start(); });
  var rz = 0;
  addEventListener('resize', function () { cancelAnimationFrame(rz); rz = requestAnimationFrame(function () { layout(); draw(); }); });
  addEventListener('sdn-theme', function () { requestAnimationFrame(function () { colors(); draw(); }); });
};
