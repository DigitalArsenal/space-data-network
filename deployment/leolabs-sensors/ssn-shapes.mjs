// Empirical U.S. Space Surveillance Network (ussf-ssn) sensor coverage shapes, boresights, elevation envelopes, and
// slant-range bounds derived from 1,175,205 unique (NORAD_CAT_ID, EPOCH) Space-Track GP history records (2026 archive)
// by `com.digitalarsenal.analysis.ssn-sensor-inference@0.1.0` (piped from `com.digitalarsenal.analysis.epoch-state@0.6.0`),
// implementing the Tom Johnson / ExaResearch TLE/OMM epoch-position methodology:
//   Thomas Johnson (Exa Research, LLC), "Reverse Engineering Space Surveillance Network Practices and Performance
//   from Two Line Element Sets", 3rd International Conference on Space Situational Awareness (ICSSA), Madrid, 2022
//   (https://www.researchgate.net/publication/359774033).
//
// Same SENSOR_LOCAL axis contract as `leolabs-shapes.mjs`:
//   +Z is the boresight: azimuth A clockwise from north, elevation E above the horizontal;
//   +X is in the boresight's vertical plane, 90 degrees below it;
//   +Y is horizontal, toward azimuth A - 90 degrees (+X x +Y = +Z).
//   In east, north, up:
//     Z = (cos E sin A, cos E cos A, sin E)
//     X = (sin E sin A, sin E cos A, -cos E)
//     Y = (-cos A, sin A, 0)

export const SSN_INFERENCE_SOURCE = {
  url: "https://www.researchgate.net/publication/359774033",
  paper:
    'Thomas Johnson (Exa Research, LLC), "Reverse Engineering Space Surveillance Network Practices and Performance from Two Line Element Sets", 3rd ICSSA, 2022',
  module: "com.digitalarsenal.analysis.ssn-sensor-inference@0.1.0",
  propagator: "com.digitalarsenal.analysis.epoch-state@0.6.0",
  retrievedAt: "2026-11-27T00:00:00.000Z",
  uniqueEpochs: 1175205,
};

const rad = Math.PI / 180;
const dot = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];

/**
 * Project a closed sequence of topocentric [azDeg, elDeg] boundary rays at slant range `rangeM`
 * into the sensor's `SENSOR_LOCAL` axes `(X, Y, Z)` for boresight `(boreAzDeg, boreElDeg)`.
 */
function buildSensorLocalOutline(boreAzDeg, boreElDeg, rangeM, azElLoop) {
  const A = boreAzDeg * rad;
  const E = boreElDeg * rad;
  const Z = [Math.cos(E) * Math.sin(A), Math.cos(E) * Math.cos(A), Math.sin(E)];
  const X = [Math.sin(E) * Math.sin(A), Math.sin(E) * Math.cos(A), -Math.cos(E)];
  const Y = [-Math.cos(A), Math.sin(A), 0];

  let maxRangeM = 0;
  let minElevationDeg = 90;
  let maxElevationDeg = -90;
  const vertices = azElLoop.map(([azDeg, elDeg]) => {
    const az = azDeg * rad;
    const el = elDeg * rad;
    const enu = [
      rangeM * Math.cos(el) * Math.sin(az),
      rangeM * Math.cos(el) * Math.cos(az),
      rangeM * Math.sin(el),
    ];
    const v = [dot(enu, X), dot(enu, Y), dot(enu, Z)];
    const r = Math.hypot(v[0], v[1], v[2]);
    if (r > maxRangeM) maxRangeM = r;
    if (elDeg < minElevationDeg) minElevationDeg = elDeg;
    if (elDeg > maxElevationDeg) maxElevationDeg = elDeg;
    return v;
  });

  return {
    vertices,
    maxRangeM,
    minElevationDeg,
    maxElevationDeg,
    azDeg: boreAzDeg,
    elDeg: boreElDeg,
    count: vertices.length,
  };
}

/**
 * Build a 1-face directional phased-array wedge outline in SENSOR_LOCAL from its empirical
 * azimuth sweep `[azStartDeg..azEndDeg]` and lower/upper elevation bounds `[minElDeg, maxElDeg]`.
 */
function buildSingleFacePhasedArray({
  boreAzDeg,
  boreElDeg,
  rangeM,
  halfAzSpanDeg,
  minElDeg,
  maxElDeg,
  steps = 12,
}) {
  const loop = [];
  for (let i = 0; i <= steps; i++) {
    const frac = -1 + (2 * i) / steps;
    const az = (boreAzDeg + frac * halfAzSpanDeg + 360) % 360;
    loop.push([az, minElDeg]);
  }
  for (let i = steps; i >= 0; i--) {
    const frac = -1 + (2 * i) / steps;
    const az = (boreAzDeg + frac * halfAzSpanDeg + 360) % 360;
    loop.push([az, maxElDeg]);
  }
  return buildSensorLocalOutline(boreAzDeg, boreElDeg, rangeM, loop);
}

/**
 * Build a 2-face UEWR phased-array outline in SENSOR_LOCAL around zenith `(boreAzDeg, 90 deg)`
 * covering the active dual-face azimuth sweep at `minElDeg` and closing over `maxElDeg` across
 * the rear blind sector so every vertex has positive +Z depth and the rear 120 deg sector is carved out.
 */
function buildTwoFaceUewr({
  boreAzDeg,
  rangeM,
  activeAzStartDeg,
  activeAzSweepDeg,
  minElDeg,
  maxElDeg,
}) {
  const loop = [];
  const activeSteps = 24;
  for (let i = 0; i <= activeSteps; i++) {
    const az = (activeAzStartDeg + (activeAzSweepDeg * i) / activeSteps + 360) % 360;
    loop.push([az, minElDeg]);
  }
  const blindSweepDeg = 360 - activeAzSweepDeg;
  const blindStartDeg = (activeAzStartDeg + activeAzSweepDeg) % 360;
  const blindSteps = 12;
  for (let i = 1; i < blindSteps; i++) {
    const az = (blindStartDeg + (blindSweepDeg * i) / blindSteps + 360) % 360;
    loop.push([az, maxElDeg]);
  }
  return buildSensorLocalOutline(boreAzDeg, 90, rangeM, loop);
}

/**
 * Build a zenith East-West surveillance fence outline (Space Fence AN/FSY-3 at Kwajalein)
 * with wide East-West horizon reach (`ewMinElDeg`) and narrower North-South meridian thickness (`nsMinElDeg`).
 */
function buildZenithFence({ rangeM, ewMinElDeg, nsMinElDeg }) {
  const loop = [];
  for (let deg = 0; deg < 360; deg += 10) {
    const radAz = deg * rad;
    // East-West (90 / 270 deg) reaches ewMinElDeg; North-South (0 / 180 deg) pinches to nsMinElDeg:
    const ewWeight = Math.sin(radAz) ** 2;
    const el = nsMinElDeg - ewWeight * (nsMinElDeg - ewMinElDeg);
    loop.push([deg, Number(el.toFixed(2))]);
  }
  return buildSensorLocalOutline(0, 90, rangeM, loop);
}

/**
 * Build a 360-deg omnidirectional radar or optical surveillance dome outline around zenith `(0, 90 deg)`
 * using its empirical 95th-percentile low-elevation horizon mask `minElDeg`.
 */
function buildOmnidirectionalHorizonMask({ rangeM, minElDeg, maxElDeg }) {
  const loop = [];
  for (let deg = 0; deg < 360; deg += 10) {
    loop.push([deg, minElDeg]);
  }
  const outline = buildSensorLocalOutline(0, 90, rangeM, loop);
  return { ...outline, maxElevationDeg: maxElDeg };
}

/**
 * Empirical SSN sensor table keyed by `ussf-ssn` sensor row id.
 * Every row records the sample count `n`, ascending-node control count `ctrl`, circular-mean boresight,
 * elevation percentiles `[elP05, elP50, elP95]`, slant-range percentiles `[rangeP50Km, rangeP95Km, rangeP99Km]`,
 * and ascending-pass fraction `ascPct` measured by `analysis/ssn-sensor-inference`.
 */
export const SSN_SENSOR_EMPIRICAL = {
  fps85: {
    n: 94504,
    ctrl: 3284,
    ascPct: 48.5,
    elP05: 15.4,
    elP50: 50.1,
    elP95: 76.1,
    rangeP50Km: 1441,
    rangeP95Km: 2674,
    rangeP99Km: 3420,
    build: () =>
      buildSingleFacePhasedArray({
        boreAzDeg: 184.8,
        boreElDeg: 50.1,
        rangeM: 3_420_000,
        halfAzSpanDeg: 60,
        minElDeg: 15.4,
        maxElDeg: 76.1,
      }),
  },
  cobradane: {
    n: 19062,
    ctrl: 0,
    ascPct: 56.6,
    elP05: 12.6,
    elP50: 28.6,
    elP95: 57.8,
    rangeP50Km: 1871,
    rangeP95Km: 3100,
    rangeP99Km: 3324,
    build: () =>
      buildSingleFacePhasedArray({
        boreAzDeg: 318.7,
        boreElDeg: 28.6,
        rangeM: 3_324_000,
        halfAzSpanDeg: 64,
        minElDeg: 12.6,
        maxElDeg: 57.8,
      }),
  },
  parcs: {
    n: 18377,
    ctrl: 0,
    ascPct: 47.4,
    elP05: 11.1,
    elP50: 29.8,
    elP95: 59.9,
    rangeP50Km: 1825,
    rangeP95Km: 2669,
    rangeP99Km: 2891,
    build: () =>
      buildSingleFacePhasedArray({
        boreAzDeg: 355.5,
        boreElDeg: 29.8,
        rangeM: 2_891_000,
        halfAzSpanDeg: 62,
        minElDeg: 11.1,
        maxElDeg: 59.9,
      }),
  },
  beale: {
    n: 2466,
    ctrl: 27,
    ascPct: 50.2,
    elP05: 4.7,
    elP50: 19.6,
    elP95: 53.6,
    rangeP50Km: 1919,
    rangeP95Km: 3613,
    rangeP99Km: 3982,
    build: () =>
      buildTwoFaceUewr({
        boreAzDeg: 246.0,
        rangeM: 3_982_000,
        activeAzStartDeg: 126,
        activeAzSweepDeg: 240,
        minElDeg: 4.7,
        maxElDeg: 53.6,
      }),
  },
  "pave-paws-cape-cod": {
    n: 1904,
    ctrl: 0,
    ascPct: 54.0,
    elP05: 4.3,
    elP50: 24.4,
    elP95: 57.6,
    rangeP50Km: 1644,
    rangeP95Km: 2806,
    rangeP99Km: 3306,
    build: () =>
      buildTwoFaceUewr({
        boreAzDeg: 107.0,
        rangeM: 3_306_000,
        activeAzStartDeg: 347,
        activeAzSweepDeg: 240,
        minElDeg: 4.3,
        maxElDeg: 57.6,
      }),
  },
  clear: {
    n: 4259,
    ctrl: 0,
    ascPct: 37.8,
    elP05: 8.4,
    elP50: 21.8,
    elP95: 44.6,
    rangeP50Km: 1711,
    rangeP95Km: 2687,
    rangeP99Km: 2953,
    build: () =>
      buildTwoFaceUewr({
        boreAzDeg: 304.0,
        rangeM: 2_953_000,
        activeAzStartDeg: 184,
        activeAzSweepDeg: 240,
        minElDeg: 8.4,
        maxElDeg: 44.6,
      }),
  },
  pituffik: {
    n: 1808,
    ctrl: 0,
    ascPct: 48.0,
    elP05: 5.7,
    elP50: 17.5,
    elP95: 56.0,
    rangeP50Km: 1877,
    rangeP95Km: 2595,
    rangeP99Km: 2730,
    build: () =>
      buildTwoFaceUewr({
        boreAzDeg: 57.0,
        rangeM: 2_730_000,
        activeAzStartDeg: 297,
        activeAzSweepDeg: 240,
        minElDeg: 5.7,
        maxElDeg: 56.0,
      }),
  },
  fylingdales: {
    n: 4739,
    ctrl: 0,
    ascPct: 37.3,
    elP05: 7.9,
    elP50: 36.1,
    elP95: 68.0,
    rangeP50Km: 1123,
    rangeP95Km: 2549,
    rangeP99Km: 2941,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 2_941_000,
        minElDeg: 7.9,
        maxElDeg: 68.0,
      }),
  },
  "space-fence-an-fsy-3": {
    n: 15745,
    ctrl: 30078,
    ascPct: 3.2,
    elP05: 26.2,
    elP50: 48.0,
    elP95: 75.0,
    rangeP50Km: 1493,
    rangeP95Km: 2253,
    rangeP99Km: 2536,
    build: () =>
      buildZenithFence({
        rangeM: 2_536_000,
        ewMinElDeg: 22.0,
        nsMinElDeg: 42.0,
      }),
  },
  millstone: {
    n: 1638,
    ctrl: 0,
    ascPct: 50.1,
    elP05: 6.9,
    elP50: 19.6,
    elP95: 53.7,
    rangeP50Km: 1894,
    rangeP95Km: 3066,
    rangeP99Km: 3498,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 3_498_000,
        minElDeg: 6.9,
        maxElDeg: 53.7,
      }),
  },
  husir: {
    n: 1638,
    ctrl: 0,
    ascPct: 50.1,
    elP05: 6.9,
    elP50: 19.6,
    elP95: 53.7,
    rangeP50Km: 1894,
    rangeP95Km: 3066,
    rangeP99Km: 3498,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 3_498_000,
        minElDeg: 6.9,
        maxElDeg: 53.7,
      }),
  },
  hax: {
    n: 1638,
    ctrl: 0,
    ascPct: 50.1,
    elP05: 6.9,
    elP50: 19.6,
    elP95: 53.7,
    rangeP50Km: 1894,
    rangeP95Km: 3066,
    rangeP99Km: 3498,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 3_498_000,
        minElDeg: 6.9,
        maxElDeg: 53.7,
      }),
  },
  globus: {
    n: 734,
    ctrl: 0,
    ascPct: 67.3,
    elP05: 6.1,
    elP50: 27.6,
    elP95: 66.7,
    rangeP50Km: 1504,
    rangeP95Km: 2652,
    rangeP99Km: 3840,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 3_840_000,
        minElDeg: 6.1,
        maxElDeg: 66.7,
      }),
  },
  altair: {
    n: 14472,
    ctrl: 28070,
    ascPct: 6.0,
    elP05: 24.5,
    elP50: 49.4,
    elP95: 76.2,
    rangeP50Km: 1425,
    rangeP95Km: 2185,
    rangeP99Km: 2546,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 2_546_000,
        minElDeg: 24.5,
        maxElDeg: 76.2,
      }),
  },
  tradex: {
    n: 14472,
    ctrl: 28070,
    ascPct: 6.0,
    elP05: 24.5,
    elP50: 49.4,
    elP95: 76.2,
    rangeP50Km: 1425,
    rangeP95Km: 2185,
    rangeP99Km: 2546,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 2_546_000,
        minElDeg: 24.5,
        maxElDeg: 76.2,
      }),
  },
  alcor: {
    n: 14472,
    ctrl: 28070,
    ascPct: 6.0,
    elP05: 24.5,
    elP50: 49.4,
    elP95: 76.2,
    rangeP50Km: 1425,
    rangeP95Km: 2185,
    rangeP99Km: 2546,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 2_546_000,
        minElDeg: 24.5,
        maxElDeg: 76.2,
      }),
  },
  mmw: {
    n: 14472,
    ctrl: 28070,
    ascPct: 6.0,
    elP05: 24.5,
    elP50: 49.4,
    elP95: 76.2,
    rangeP50Km: 1425,
    rangeP95Km: 2185,
    rangeP99Km: 2546,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 2_546_000,
        minElDeg: 24.5,
        maxElDeg: 76.2,
      }),
  },
  "holt-c-band": {
    n: 529,
    ctrl: 19956,
    ascPct: 48.2,
    elP05: 5.7,
    elP50: 15.8,
    elP95: 42.3,
    rangeP50Km: 1397,
    rangeP95Km: 2041,
    rangeP99Km: 3652,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 3_652_000,
        minElDeg: 5.7,
        maxElDeg: 42.3,
      }),
  },
  "geodss-maui": {
    n: 16503,
    ctrl: 6431,
    ascPct: 50.8,
    elP05: 19.5,
    elP50: 43.3,
    elP95: 71.1,
    rangeP50Km: 37031,
    rangeP95Km: 39702,
    rangeP99Km: 40418,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 40_418_000,
        minElDeg: 19.5,
        maxElDeg: 71.1,
      }),
  },
  "geodss-diego-garcia": {
    n: 16476,
    ctrl: 3718,
    ascPct: 40.3,
    elP05: 19.4,
    elP50: 47.4,
    elP95: 80.0,
    rangeP50Km: 36594,
    rangeP95Km: 39724,
    rangeP99Km: 40335,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 40_335_000,
        minElDeg: 19.4,
        maxElDeg: 80.0,
      }),
  },
  sst: {
    n: 8948,
    ctrl: 3074,
    ascPct: 43.5,
    elP05: 19.4,
    elP50: 42.3,
    elP95: 65.8,
    rangeP50Km: 36936,
    rangeP95Km: 39660,
    rangeP99Km: 40398,
    build: () =>
      buildOmnidirectionalHorizonMask({
        rangeM: 40_398_000,
        minElDeg: 19.4,
        maxElDeg: 65.8,
      }),
  },
};

/**
 * Return the empirical `shape` object (matching `leolabs-network.mjs` `shapeOf`) for an active `ussf-ssn` sensor row,
 * or `null` if the sensor is retired (`moss`), under construction (`darc-site-1`), or in-orbit.
 */
export function ssnShapeOf(sensorId) {
  const entry = SSN_SENSOR_EMPIRICAL[sensorId];
  if (!entry) return null;
  const outline = entry.build();
  const summary =
    `Empirical TLE/OMM epoch-position inference (${SSN_INFERENCE_SOURCE.paper}; ${SSN_INFERENCE_SOURCE.module}): ` +
    `${entry.n.toLocaleString("en-US")} exclusive last-observation epochs (ascending-node control=${entry.ctrl.toLocaleString("en-US")}) ` +
    `from ${SSN_INFERENCE_SOURCE.uniqueEpochs.toLocaleString("en-US")} unique GP history records; ` +
    `boresight az=${outline.azDeg} deg, el=${outline.elDeg} deg (LOCAL_ENU); ` +
    `El[P05,P50,P95]=[${entry.elP05}, ${entry.elP50}, ${entry.elP95}] deg; ` +
    `SlantRange[P50,P95,P99]=[${entry.rangeP50Km}, ${entry.rangeP95Km}, ${entry.rangeP99Km}] km; ` +
    `ascendingPasses=${entry.ascPct}%.`;
  return {
    ...outline,
    cite: {
      url: SSN_INFERENCE_SOURCE.url,
      text: SSN_INFERENCE_SOURCE.paper,
      source: `${SSN_INFERENCE_SOURCE.paper} via ${SSN_INFERENCE_SOURCE.module}`,
      recordId: `ussf-ssn:${sensorId}/fov`,
      retrievedAt: SSN_INFERENCE_SOURCE.retrievedAt,
      version: SSN_INFERENCE_SOURCE.module,
      notes: summary,
    },
  };
}
