// Scroll-driven Earth for spacedatanetwork.org.
// One full-screen WebGL2 shader ray-traces the planet, so there is no mesh and
// no library. Sections marked data-earth="dist lon lat ox oy sunAz sunEl" are
// camera keyframes; scrolling between them moves the camera and swings the Sun.
//   dist   camera distance from Earth's center, in Earth radii
//   lon    camera longitude, degrees
//   lat    camera latitude, degrees
//   ox oy  where Earth's center sits on screen, in half-screens (0 0 = middle)
//   sunAz  Sun angle around the view: 0 behind the camera, 90 from the right,
//          180 behind the planet
//   sunEl  Sun elevation relative to the view, degrees
// Illustrative satellites ride circular orbits over the planet in a second GPU
// pass; one carries its whole orbit as an arc. Without WebGL2, or with reduced
// motion requested, the page keeps its video.
(function () {
  var canvas = document.getElementById('earth');
  var sections = Array.prototype.slice.call(document.querySelectorAll('[data-earth]'));
  if (!canvas || !sections.length) return;
  if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
  var gl = canvas.getContext('webgl2', { antialias: false, alpha: false, powerPreference: 'high-performance' });
  if (!gl) return;
  window.SDN_EARTH = true;
  document.documentElement.classList.add('has-earth');

  var VERT = '#version 300 es\nin vec2 p;out vec2 v;void main(){v=p;gl_Position=vec4(p,0.,1.);}';
  var FRAG = [
    '#version 300 es',
    'precision highp float;',
    'in vec2 v;out vec4 o;',
    'uniform sampler2D uDay,uNight,uClouds;',
    'uniform vec2 uRes,uOff;uniform vec3 uCam,uSun;uniform mat3 uBasis;',
    'uniform float uTan,uSpin,uCloudSpin,uFade;',
    'const float PI=3.14159265;',
    'float hash(vec3 p){p=fract(p*.3183099+.1);p*=17.;return fract(p.x*p.y*p.z*(p.x+p.y+p.z));}',
    'vec3 lin(vec3 c){return c*c;}',
    // Longitude is measured from +z toward +x; the texture starts at -180 deg.
    'vec2 geo(vec3 n,float spin){float lon=atan(n.x,n.z)-spin;return vec2(lon,asin(clamp(n.y,-1.,1.)));}',
    'vec4 tex(sampler2D t,vec2 g){',
    '  vec2 uv=vec2(fract((g.x+PI)/(2.*PI)),.5-g.y/PI);',
    // Take gradients from a longitude that is continuous across the seam.
    '  vec2 uv2=vec2(fract((g.x)/(2.*PI)),uv.y);',
    '  vec2 dx=dFdx(uv),dy=dFdy(uv),dx2=dFdx(uv2),dy2=dFdy(uv2);',
    '  if(dot(dx2,dx2)+dot(dy2,dy2)<dot(dx,dx)+dot(dy,dy)){dx=dx2;dy=dy2;}',
    '  return textureGrad(t,uv,dx,dy);}',
    'void main(){',
    '  float asp=uRes.x/uRes.y;',
    '  vec2 q=(v-uOff)*vec2(asp,1.)*uTan;',
    '  vec3 rd=normalize(uBasis*vec3(q,-1.));',
    '  vec3 ro=uCam;',
    '  float b=dot(ro,rd),c=dot(ro,ro)-1.,h=b*b-c;',
    '  vec3 col=vec3(0.);',
    '  float closest=length(ro-rd*b);',
    '  vec3 cp=normalize(ro-rd*b);',
    '  if(h>0.&&-b-sqrt(h)>0.){',
    '    vec3 n=normalize(ro+rd*(-b-sqrt(h)));',
    '    float d=dot(n,uSun);',
    '    vec2 g=geo(n,uSpin);',
    '    vec3 day=lin(tex(uDay,g).rgb);',
    '    vec3 night=lin(tex(uNight,g).rgb);',
    '    float cl=tex(uClouds,geo(n,uSpin+uCloudSpin)).r;cl=smoothstep(.15,.95,cl);',
    '    float lit=smoothstep(-.08,.22,d);',
    '    float ocean=smoothstep(.02,.1,day.b-day.r);',
    '    vec3 hv=normalize(uSun-rd);',
    '    float spec=pow(max(dot(n,hv),0.),60.)*ocean*(1.-cl)*.9;',
    '    vec3 dcol=mix(day,vec3(.95),cl*.9)*(.06+1.15*max(d,0.))+vec3(1.,.9,.75)*spec*lit;',
    // Warm the terminator.
    '    dcol*=mix(vec3(1.),vec3(1.25,.8,.55),smoothstep(.35,0.,d)*lit);',
    '    vec3 ncol=night*vec3(1.6,1.25,.85)*(1.-cl*.85)*smoothstep(.12,-.12,d);',
    '    col=dcol*lit+ncol;',
    '    float fr=pow(1.-max(dot(n,-rd),0.),3.);',
    '    col+=vec3(.25,.5,1.)*fr*smoothstep(-.3,.5,d)*.9;',
    '  }else{',
    '    vec3 s=rd*420.;vec3 cell=floor(s);float hs=hash(cell);',
    '    if(hs>.994){vec3 off=vec3(hash(cell+1.7),hash(cell+3.1),hash(cell+5.3))-.5;',
    '      float st=smoothstep(.22,0.,length(fract(s)-.5-off*.5));col+=vec3(.8,.85,1.)*st*(hs-.994)*140.;}',
    '    col+=vec3(1.,.92,.8)*(pow(max(dot(rd,uSun),0.),3000.)*40.+pow(max(dot(rd,uSun),0.),90.)*.14);',
    '  }',
    // Atmosphere around the limb, brighter on the lit side and when looking toward the Sun.
    '  float alt=closest-1.;',
    '  if(alt>-.02){',
    '    float glow=exp(-max(alt,0.)*55.)*smoothstep(-.02,.004,alt);',
    '    float day=smoothstep(-.35,.4,dot(cp,uSun));',
    '    float fwd=pow(max(dot(rd,uSun),0.),6.);',
    '    col+=glow*(vec3(.3,.55,1.)*day*.9+vec3(1.,.55,.25)*fwd*2.2);',
    '  }',
    '  col=1.-exp(-col*1.1);',
    '  o=vec4(sqrt(col)*uFade,1.);',
    '}'
  ].join('\n');

  function shader(type, src) {
    var s = gl.createShader(type);
    gl.shaderSource(s, src);
    gl.compileShader(s);
    if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s));
    return s;
  }
  var prog = gl.createProgram();
  try {
    gl.attachShader(prog, shader(gl.VERTEX_SHADER, VERT));
    gl.attachShader(prog, shader(gl.FRAGMENT_SHADER, FRAG));
    gl.linkProgram(prog);
    if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(prog));
  } catch (err) {
    window.SDN_EARTH = false;
    document.documentElement.classList.remove('has-earth');
    console.warn('earth: shader failed', err);
    return;
  }
  gl.useProgram(prog);
  var quadVao = gl.createVertexArray();
  gl.bindVertexArray(quadVao);
  var buf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
  var loc = gl.getAttribLocation(prog, 'p');
  gl.enableVertexAttribArray(loc);
  gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
  gl.bindVertexArray(null);
  var U = {};
  ['uDay', 'uNight', 'uClouds', 'uRes', 'uOff', 'uCam', 'uSun', 'uBasis', 'uTan', 'uSpin', 'uCloudSpin', 'uFade'].forEach(function (n) { U[n] = gl.getUniformLocation(prog, n); });

  // Satellites. Each vertex is one orbit: radius (Earth radii), inclination,
  // node and starting angle. kind 0 = dot, 1 = a sample of the highlighted
  // orbit's arc, 2 = the highlighted satellite. Positions are computed on the GPU.
  var ORB_VERT = [
    '#version 300 es',
    'in vec4 aOrb;in float aKind;',
    'uniform vec3 uCam,uSun;uniform mat3 uBasis;uniform vec2 uRes,uOff;',
    'uniform float uTan,uTime,uRate,uPx,uHiAngle,uFade;',
    'out float vA;out float vKind;',
    'void main(){',
    '  float r=aOrb.x,inc=aOrb.y,node=aOrb.z;',
    '  float ang=aKind==1.?aOrb.w:aKind==2.?uHiAngle:aOrb.w+uRate*pow(r,-1.5)*uTime;',
    // Angle runs the same way as the planet's spin, so prograde orbits look prograde.
    '  vec3 p=vec3(sin(ang),0.,cos(ang));',
    '  p=vec3(p.x,-p.z*sin(inc),p.z*cos(inc));',
    '  p=vec3(p.x*cos(node)+p.z*sin(node),p.y,-p.x*sin(node)+p.z*cos(node))*r;',
    '  vec3 c=transpose(uBasis)*(p-uCam);',
    '  vKind=aKind;',
    '  if(c.z>-.02){gl_Position=vec4(2.,2.,0.,1.);gl_PointSize=0.;vA=0.;return;}',
    '  float asp=uRes.x/uRes.y;',
    '  gl_Position=vec4((c.xy/-c.z)/uTan/vec2(asp,1.)+uOff,0.,1.);',
    '  vec3 d=p-uCam;float L=length(d);d/=L;',
    '  float b=dot(uCam,d),h=b*b-dot(uCam,uCam)+1.,vis=1.;',
    '  if(h>0.){float t=-b-sqrt(h);if(t>0.)vis=smoothstep(-.03,.01,t-L);}',
    '  float s=dot(p,uSun);float shade=(s<0.&&length(p-s*uSun)<1.)?.28:1.;',
    '  float near=clamp(1.7/-c.z,.55,2.);',
    '  if(aKind==0.){gl_PointSize=2.6*uPx*near;vA=.85*shade;}',
    '  else if(aKind==1.){float lag=mod(uHiAngle-ang,6.2831853);gl_PointSize=3.4*uPx*near;vA=.5+.5*exp(-lag*.7);}',
    '  else{gl_PointSize=9.*uPx*near;vA=1.;}',
    '  vA*=vis*uFade;',
    '}'
  ].join('\n');
  var ORB_FRAG = [
    '#version 300 es',
    'precision highp float;',
    'in float vA;in float vKind;out vec4 o;',
    'void main(){',
    '  float d=length(gl_PointCoord-.5);',
    '  if(vKind==2.){float core=smoothstep(.22,.12,d),halo=smoothstep(.5,.1,d)*.45;o=vec4(mix(vec3(1.,.64,.14),vec3(1.,.95,.85),core),(core+halo)*vA);return;}',
    '  float a=smoothstep(.5,.15,d)*vA;',
    '  o=vec4(vKind==1.?vec3(1.,.64,.14):vec3(.7,.88,1.),vKind==1.?a*.8:a);',
    '}'
  ].join('\n');
  var orbProg = gl.createProgram();
  var orbOk = false;
  try {
    gl.attachShader(orbProg, shader(gl.VERTEX_SHADER, ORB_VERT));
    gl.attachShader(orbProg, shader(gl.FRAGMENT_SHADER, ORB_FRAG));
    gl.linkProgram(orbProg);
    orbOk = gl.getProgramParameter(orbProg, gl.LINK_STATUS);
    if (!orbOk) console.warn('earth: satellite shader failed', gl.getProgramInfoLog(orbProg));
  } catch (err) {
    console.warn('earth: satellite shader failed', err);
  }

  // A fixed seed keeps the sky the same on every visit.
  var seed = 7;
  function rnd() { seed = (seed * 16807) % 2147483647; return (seed - 1) / 2147483646; }
  var D0 = Math.PI / 180;
  // Placed so its arc rises clear of the hero text and the satellite crosses it just after load.
  var HI = { r: 1.2, inc: 60 * D0, node: 300 * D0, phase: 2.4 };
  var orbs = [];
  function shellInc() {
    var x = rnd();
    if (x < 0.38) return 53;
    if (x < 0.62) return 97.5;
    if (x < 0.72) return 43;
    if (x < 0.8) return 70;
    if (x < 0.87) return 87.9;
    return rnd() * 100;
  }
  for (var n = 0; n < 1500; n++) orbs.push(1.06 + rnd() * rnd() * 0.16, (shellInc() + (rnd() - 0.5)) * D0, rnd() * 6.2832, rnd() * 6.2832, 0);
  for (n = 0; n < 220; n++) orbs.push(1.35 + rnd() * 0.9, rnd() * 65 * D0, rnd() * 6.2832, rnd() * 6.2832, 0);
  for (n = 0; n < 110; n++) orbs.push(6.62, rnd() * 1.5 * D0, rnd() * 6.2832, rnd() * 6.2832, 0);
  var ARC = 4000;
  for (n = 0; n < ARC; n++) orbs.push(HI.r, HI.inc, HI.node, n / ARC * 6.2832, 1);
  orbs.push(HI.r, HI.inc, HI.node, 0, 2);
  var orbCount = orbs.length / 5;
  var orbVao = gl.createVertexArray();
  gl.bindVertexArray(orbVao);
  var orbBuf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, orbBuf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(orbs), gl.STATIC_DRAW);
  var aOrb = gl.getAttribLocation(orbProg, 'aOrb'), aKind = gl.getAttribLocation(orbProg, 'aKind');
  gl.enableVertexAttribArray(aOrb);
  gl.vertexAttribPointer(aOrb, 4, gl.FLOAT, false, 20, 0);
  gl.enableVertexAttribArray(aKind);
  gl.vertexAttribPointer(aKind, 1, gl.FLOAT, false, 20, 16);
  gl.bindVertexArray(null);
  var OU = {};
  ['uCam', 'uSun', 'uBasis', 'uRes', 'uOff', 'uTan', 'uTime', 'uRate', 'uPx', 'uHiAngle', 'uFade'].forEach(function (k) { OU[k] = gl.getUniformLocation(orbProg, k); });
  // Kepler's period-radius ratio, scaled so the geostationary ring turns with the planet.
  var SPIN = 0.008;
  var RATE = SPIN * Math.pow(6.62, 1.5);

  var pending = 3;
  function texture(unit, url) {
    var t = gl.createTexture();
    gl.activeTexture(gl.TEXTURE0 + unit);
    gl.bindTexture(gl.TEXTURE_2D, t);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, 1, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, new Uint8Array([0, 0, 0, 255]));
    var img = new Image();
    img.onload = function () {
      gl.activeTexture(gl.TEXTURE0 + unit);
      gl.bindTexture(gl.TEXTURE_2D, t);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, img);
      gl.generateMipmap(gl.TEXTURE_2D);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR_MIPMAP_LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.REPEAT);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
      var ext = gl.getExtension('EXT_texture_filter_anisotropic');
      if (ext) gl.texParameterf(gl.TEXTURE_2D, ext.TEXTURE_MAX_ANISOTROPY_EXT, 8);
      if (--pending === 0) { ready = performance.now(); canvas.classList.add('loaded'); }
    };
    img.src = url;
  }
  texture(0, 'img/earth/day.webp');
  texture(1, 'img/earth/night.webp');
  texture(2, 'img/earth/clouds.webp');
  gl.uniform1i(U.uDay, 0);
  gl.uniform1i(U.uNight, 1);
  gl.uniform1i(U.uClouds, 2);

  var keys = sections.map(function (el) {
    return { el: el, v: el.getAttribute('data-earth').trim().split(/\s+/).map(Number) };
  });
  function target() {
    var mid = window.scrollY + window.innerHeight * 0.5;
    var anchors = keys.map(function (k) {
      var r = k.el.getBoundingClientRect();
      return window.scrollY + r.top + Math.min(r.height, window.innerHeight) * 0.35;
    });
    if (mid <= anchors[0]) return keys[0].v.slice();
    for (var i = 0; i < keys.length - 1; i++) {
      if (mid < anchors[i + 1]) {
        var t = (mid - anchors[i]) / Math.max(1, anchors[i + 1] - anchors[i]);
        t = t * t * (3 - 2 * t);
        return keys[i].v.map(function (a, j) { return a + (keys[i + 1].v[j] - a) * t; });
      }
    }
    return keys[keys.length - 1].v.slice();
  }

  var state = target();
  var ready = 0;
  var last = performance.now();
  var spin = 0;
  var clock = 0;
  var visible = true;
  document.addEventListener('visibilitychange', function () {
    visible = !document.hidden;
    if (visible) { last = performance.now(); requestAnimationFrame(frame); }
  });

  function resize() {
    var w = window.innerWidth, h = window.innerHeight;
    var scale = Math.min(window.devicePixelRatio || 1, 2, Math.sqrt(3.2e6 / (w * h)));
    var cw = Math.round(w * scale), ch = Math.round(h * scale);
    if (canvas.width !== cw || canvas.height !== ch) { canvas.width = cw; canvas.height = ch; }
    gl.viewport(0, 0, cw, ch);
  }

  var D = Math.PI / 180;
  function frame(now) {
    if (!visible) return;
    var dt = Math.min(0.05, (now - last) / 1000);
    last = now;
    spin += dt * SPIN;
    clock += dt;
    var goal = target();
    var k = 1 - Math.exp(-dt * 3.2);
    for (var i = 0; i < state.length; i++) state[i] += (goal[i] - state[i]) * k;
    resize();

    var dist = state[0], lon = state[1] * D, lat = state[2] * D;
    var cam = [dist * Math.cos(lat) * Math.sin(lon), dist * Math.sin(lat), dist * Math.cos(lat) * Math.cos(lon)];
    var f = [-cam[0] / dist, -cam[1] / dist, -cam[2] / dist];
    var r = [f[1] * 0 - f[2] * 1, f[2] * 0 - f[0] * 0, f[0] * 1 - f[1] * 0];
    var rl = Math.hypot(r[0], r[1], r[2]) || 1;
    r = [r[0] / rl, r[1] / rl, r[2] / rl];
    var u = [r[1] * f[2] - r[2] * f[1], r[2] * f[0] - r[0] * f[2], r[0] * f[1] - r[1] * f[0]];
    // Basis columns: right, up, back (the shaders look down -z).
    var az = state[5] * D, el = state[6] * D;
    var sv = [Math.sin(az) * Math.cos(el), Math.sin(el), Math.cos(az) * Math.cos(el)];
    var sun = [
      r[0] * sv[0] + u[0] * sv[1] - f[0] * sv[2],
      r[1] * sv[0] + u[1] * sv[1] - f[1] * sv[2],
      r[2] * sv[0] + u[2] * sv[1] - f[2] * sv[2]
    ];
    var basis = [r[0], r[1], r[2], u[0], u[1], u[2], -f[0], -f[1], -f[2]];
    var fade = ready ? Math.min(1, (now - ready) / 1200) : 0;
    gl.useProgram(prog);
    gl.bindVertexArray(quadVao);
    gl.uniformMatrix3fv(U.uBasis, false, basis);
    gl.uniform3fv(U.uCam, cam);
    gl.uniform3fv(U.uSun, sun);
    gl.uniform2f(U.uRes, canvas.width, canvas.height);
    gl.uniform2f(U.uOff, state[3], state[4]);
    gl.uniform1f(U.uTan, Math.tan(22 * D));
    gl.uniform1f(U.uSpin, spin);
    gl.uniform1f(U.uCloudSpin, spin * 0.35);
    gl.uniform1f(U.uFade, fade);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
    if (orbOk) {
      gl.useProgram(orbProg);
      gl.bindVertexArray(orbVao);
      gl.uniformMatrix3fv(OU.uBasis, false, basis);
      gl.uniform3fv(OU.uCam, cam);
      gl.uniform3fv(OU.uSun, sun);
      gl.uniform2f(OU.uRes, canvas.width, canvas.height);
      gl.uniform2f(OU.uOff, state[3], state[4]);
      gl.uniform1f(OU.uTan, Math.tan(22 * D));
      gl.uniform1f(OU.uTime, clock);
      gl.uniform1f(OU.uRate, RATE);
      gl.uniform1f(OU.uPx, canvas.width / window.innerWidth);
      gl.uniform1f(OU.uHiAngle, HI.phase + RATE * Math.pow(HI.r, -1.5) * clock);
      gl.uniform1f(OU.uFade, fade);
      gl.enable(gl.BLEND);
      gl.blendFunc(gl.SRC_ALPHA, gl.ONE);
      gl.drawArrays(gl.POINTS, 0, orbCount);
      gl.disable(gl.BLEND);
    }
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);
})();
