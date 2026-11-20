// The LeoLabs radars' coverage shapes, ported from LeoLabs' public visualization (https://platform.leolabs.space/visualizations/leo)
// with LeoLabs' permission as the Space Data Network owner states it (2026-11-20: "we have permission it's all public"; these networks
// are the free, signed-out part of the console). Credit: LeoLabs, https://leolabs.space .
//
// THE SOURCE. The page loads one data file, source/leolabs-visualization-static-<time>.json.gz, kept here as it was served (gzip,
// sha256 below). For each radar it gives `pos` (the radar, km) and `fov` (the outline of its coverage, km), both in an Earth-fixed
// frame on a SPHERE of radius 6378.16 km on which the radar's geodetic latitude is used as the latitude: pos =
// (6378.16 km + height) (cos lat cos lon, cos lat sin lon, sin lat) for all twelve radars. The local up at a radar is therefore the
// WGS84 geodetic normal at its latitude and longitude. The outline points lie 2000 km above that sphere (within 1 to 7 km): the
// edge of the radar's directions where they reach a 2000 km altitude ceiling.
//
// WHAT A RECORD CARRIES. FIELD_OF_VIEW CUSTOM_POLYGON, POLYGON_FRAME SENSOR_LOCAL: every outline point, in order, as the vector
// from the radar to the point in the sensor's own axes, in meters (its direction is the outline's direction; its length is the
// distance to the 2000 km ceiling in that direction). RANGE_BOUNDARY ALTITUDE_SHELL with MAX_ALTITUDE_M 2000 km and no
// ALTITUDE_REFERENCE: the volume ends where each direction reaches 2000 km above the Earth's standard ellipsoid (LeoLabs' sphere
// is their Earth; the radar stands on ours). MAX_RANGE_M is the longest of the outline's distances, LeoLabs' own number, which
// still bounds every direction. Nothing is smoothed, resampled or dropped.
//
// THE SENSOR'S AXES (MOUNTING FRAME LOCAL_ENU, POINTING FIXED, no roll), the axes the console points a site's sensor with
// (east-north-up turned by heading A - 90 degrees and pitch E - 90 degrees, no roll):
//   +Z the boresight: azimuth A clockwise from north, elevation E above the horizontal;
//   +X in the boresight's vertical plane, 90 degrees below it (at the zenith: horizontal toward azimuth A);
//   +Y horizontal, toward azimuth A - 90 degrees (+X x +Y = +Z).
// In east, north, up: Z = (cos E sin A, cos E cos A, sin E), X = (sin E sin A, sin E cos A, -cos E), Y = (-cos A, sin A, 0).
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { gunzipSync } from "node:zlib";

const here = path.dirname(fileURLToPath(import.meta.url));

export const SHAPE_SOURCE = {
  url: "https://platform-assets.leolabs.space/visualization/static.json.gz",
  page: "https://platform.leolabs.space/visualizations/leo",
  file: "source/leolabs-visualization-static-20261120T112828Z.json.gz",
  retrievedAt: "2026-11-20T11:28:28.000Z",
  sha256: "728664ee9a907dd7f06e06e0a82e3c27fc9bd82e0c69ac3b695cb0f8218ae12a",
};

const gz = readFileSync(path.join(here, SHAPE_SOURCE.file));
const digest = createHash("sha256").update(gz).digest("hex");
if (digest !== SHAPE_SOURCE.sha256) throw new Error(`${SHAPE_SOURCE.file}: sha256 ${digest} is not the captured file's ${SHAPE_SOURCE.sha256}`);
const DATA = JSON.parse(gunzipSync(gz).toString("utf8"));
/** The data file's own epoch: "2026-11-20T09:01:02.481279+00:00". */
export const SHAPE_EPOCH = DATA.epoch;
/** The altitude every radar's outline reaches (the data file's points lie 2000 km above its 6378.16 km sphere, within 1 to 7 km). */
export const CEILING_M = 2000000;

const rad = Math.PI / 180;
const dot = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];

/** One radar's instrument row of the data file, by the page's id (pfisr, msr, ksr1 ... hisr1). */
export function instrumentOf(id) {
  const row = DATA.instruments.find((instrument) => instrument.id === id);
  if (!row) throw new Error(`the LeoLabs data file has no instrument ${id}`);
  return row;
}

/**
 * A radar's outline as FIELD_OF_VIEW vertices: { vertices: [[x, y, z] m], maxRangeM, ceilingM, minElevationDeg, maxElevationDeg, azDeg, elDeg, count }.
 * azDeg / elDeg are the boresight the data file states, which the record's MOUNTING carries; the elevations are the outline's
 * lowest and highest above the radar's horizontal.
 */
export function outlineOf(id) {
  const row = instrumentOf(id);
  const lat = row.latitude * rad, lon = row.longitude * rad;
  const east = [-Math.sin(lon), Math.cos(lon), 0];
  const north = [-Math.sin(lat) * Math.cos(lon), -Math.sin(lat) * Math.sin(lon), Math.cos(lat)];
  const up = [Math.cos(lat) * Math.cos(lon), Math.cos(lat) * Math.sin(lon), Math.sin(lat)];
  const A = row.boresight.azimuth * rad, E = row.boresight.elevation * rad;
  const Z = [Math.cos(E) * Math.sin(A), Math.cos(E) * Math.cos(A), Math.sin(E)];
  const X = [Math.sin(E) * Math.sin(A), Math.sin(E) * Math.cos(A), -Math.cos(E)];
  const Y = [-Math.cos(A), Math.sin(A), 0];
  let maxRangeM = 0;
  let minElevationDeg = 90, maxElevationDeg = -90;
  const vertices = row.fov.map((point) => {
    // Every point is on the ceiling: 2000 km above the file's 6378.16 km sphere (it holds to 1 to 7 km).
    const above = Math.hypot(...point) * 1000 - 6378160;
    if (Math.abs(above - CEILING_M) > 10000) throw new Error(`${id}: an outline point is ${(above / 1000).toFixed(1)} km above the sphere, not the 2000 km ceiling`);
    const v = [0, 1, 2].map((i) => (point[i] - row.pos[i]) * 1000);
    const enu = [dot(v, east), dot(v, north), dot(v, up)];
    const range = Math.hypot(...v);
    const elevation = Math.asin(enu[2] / range) / rad;
    maxRangeM = Math.max(maxRangeM, range);
    minElevationDeg = Math.min(minElevationDeg, elevation);
    maxElevationDeg = Math.max(maxElevationDeg, elevation);
    return [dot(enu, X), dot(enu, Y), dot(enu, Z)];
  });
  return { vertices, maxRangeM, ceilingM: CEILING_M, minElevationDeg, maxElevationDeg, azDeg: row.boresight.azimuth, elDeg: row.boresight.elevation, count: vertices.length };
}
