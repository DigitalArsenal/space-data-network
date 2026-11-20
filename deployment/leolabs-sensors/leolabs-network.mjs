// The LeoLabs network, in the same row shape as every other network (networks.mjs). The values are roster.mjs' (the page's, the AMOS
// paper's and the FCC license's); this file only gives each row its network, operator, kind, status and its own citation.
import { LEOLABS_URL, PAGE_EPOCH, PAGE_URL, PAPER, PAPER_URL, RADARS, SITES, VIDEO } from "./roster.mjs";
import { SHAPE_EPOCH, SHAPE_SOURCE, outlineOf } from "./leolabs-shapes.mjs";

const CREDIT = `LeoLabs, ${LEOLABS_URL}`;
const PAGE_CITE = { url: PAGE_URL, text: `LeoLabs public visualization, data epoch ${PAGE_EPOCH}` };
const mhz = (value) => `${value} MHz`;
const km = (meters) => `${Math.round(meters / 1000).toLocaleString("en-US")} km`;

/** A radar's field of view: LeoLabs' own outline (leolabs-shapes.mjs), with the citation of the data file it was read from. */
function shapeOf(radar) {
  const outline = outlineOf(radar.id);
  if (outline.azDeg !== radar.az || outline.elDeg !== radar.el) throw new Error(`${radar.id}: the data file's boresight ${outline.azDeg}/${outline.elDeg} is not the roster's ${radar.az}/${radar.el}`);
  return {
    ...outline,
    cite: {
      url: SHAPE_SOURCE.url,
      text: `LeoLabs public visualization data file, epoch ${SHAPE_EPOCH}`,
      source: "LeoLabs public visualization (https://platform.leolabs.space/visualizations/leo), its data file static.json.gz",
      recordId: `instruments/${radar.id}/fov`,
      retrievedAt: SHAPE_SOURCE.retrievedAt,
      version: `data epoch ${SHAPE_EPOCH}, sha256 ${SHAPE_SOURCE.sha256}`,
      notes:
        `The outline's ${outline.count} points, in LeoLabs' order, each as the vector from the radar to the point in the sensor's axes (meters). ` +
        "The data file gives them in an Earth-fixed frame on a sphere of 6378.16 km on which the radar's latitude and longitude are used, so its local up is the geodetic one; " +
        "each point is 2000 km above that sphere. RANGE_BOUNDARY ALTITUDE_SHELL, MAX_ALTITUDE_M 2000 km above the Earth's standard ellipsoid: the coverage ends there. " +
        "MAX_RANGE_M is the longest distance from the radar to the outline.",
    },
  };
}

export const LEOLABS_NETWORK = {
  slug: "leolabs", name: "LeoLabs", operator: "LeoLabs, Inc.", kind: "commercial space surveillance radar network",
  status: "operating", credit: CREDIT, retrievedAt: PAGE_EPOCH, version: `data epoch ${PAGE_EPOCH}`,
  terms: {
    class: "CiteOnly",
    text:
      "No license is granted by LeoLabs. LeoLabs lets its visualization be used with credit and a link to https://leolabs.space " +
      "for non-commercial educational, academic or research uses and for news, and reserves all rights " +
      "(https://platform.leolabs.space/visualizations_terms_for_sharing). Facts (name, position, pointing, power, frequency) " +
      "are published here with that credit, on the Space Data Network owner's decision of 2026-11-18, and each radar's coverage outline " +
      "from the visualization's data file with LeoLabs' permission as the owner states it (2026-11-20), open and free; LeoLabs can have them removed.",
  },
  sites: SITES.map((site) => {
    const first = RADARS.find((r) => r.id === site.radarIds[0]);
    return {
      slug: site.slug, name: site.name, abbr: site.abbreviation, type: site.siteType, kind: "space surveillance radar", country: site.country,
      lat: site.lat, lon: site.lon, alt: site.alt, precision: "as the page shows it", where: site.where, cite: PAGE_CITE, recordId: first.id,
      parties: site.parties.map((p) => ({ role: p.role, name: p.name, cite: p.cite.match(/https?:\/\/\S+/)?.[0] ?? PAGE_URL })),
      status: "OPERATING", note: `Height in metres. ${site.note}`,
    };
  }),
  sensors: RADARS.map((radar) => {
    const limits = radar.bandLowMHz !== undefined;
    const shape = shapeOf(radar);
    const peers = RADARS.filter((other) => other.site === radar.site && other.id !== radar.id);
    const kindWords =
      radar.kind === "planar"
        ? "The page marks it as a two-dimensional (planar) radar."
        : `${PAPER} (${PAPER_URL}) describes the fan radars as one-dimensional fields of view, UHF one fan and S-band two fans per site.`;
    return {
      id: radar.id, site: radar.site, name: radar.name, type: "UNSPECIFIED", emission: "ACTIVE",
      band: {
        name: limits
          ? `${radar.band} band ${radar.bandLowMHz} to ${radar.bandHighMHz} MHz (${radar.bandCite.text}); the page's transmit frequency is ${mhz(radar.freqMHz)}`
          : `${radar.band} band; the page's single transmit frequency is ${mhz(radar.freqMHz)}; band limits not published`,
        designation: radar.band, lowMHz: radar.bandLowMHz, highMHz: radar.bandHighMHz,
        cite: limits ? { url: radar.bandCite.url, text: radar.bandCite.text, source: radar.bandCite.source, recordId: radar.bandCite.recordId, license: radar.bandCite.license, notes: radar.bandCite.notes } : undefined,
      },
      powerW: radar.powerW, az: radar.az, el: radar.el, cite: PAGE_CITE, shape,
      note:
        `Transmit frequency ${mhz(radar.freqMHz)} and transmit power ${radar.powerW} W are the page's single values (the power is taken as the peak transmit power). ` +
        `${limits ? `Band limits ${radar.bandLowMHz} to ${radar.bandHighMHz} MHz are from ${radar.bandCite.text}. ` : "Band limits are not published. "}` +
        `Boresight azimuth ${radar.az}, elevation ${radar.el} degrees (local east-north-up). ` +
        "Radar type: a phased-array radar per LeoLabs; the page does not say which feed. " +
        `Field of view: LeoLabs' outline of this radar's coverage, ${shape.count} points from the page's data file, the directions from the radar to where its coverage reaches 2000 km altitude ` +
        `(elevations ${shape.minElevationDeg.toFixed(1)} to ${shape.maxElevationDeg.toFixed(1)} degrees). ` +
        `The shape ends at that 2000 km altitude ceiling, each direction at its own distance; the range is the longest of those distances, ${km(shape.maxRangeM)}, at the lowest elevation. ` +
        `${kindWords} ` +
        `${peers.length ? `Paired radar at this site: ${peers.map((peer) => `leolabs:${peer.id}`).join(", ")}. ` : ""}` +
        `${radar.id === "rasr1" ? `${VIDEO.text}: ${VIDEO.url}. ` : ""}` +
        `${radar.id === "pfisr" ? "Operated by SRI International for the National Science Foundation; LeoLabs is a user. " : ""}`.trim(),
    };
  }),
  inorbit: [],
  leftOut: [
    ["Radar ids of the page's table with no position (hqsr, agsr, risr, dusr, rwsr, sksr, sasr, irsr, dgsr and others), the Saudi Arabia entries and the three DSR GEO entries", "no coordinates, no frequency, no field of view on the page: planned or placeholders"],
    ["Argentina (Tolhuin) radar", "the page gives \"Southern Argentina\" only; the coordinates are in an Argentine Ministry of Defense report, and its installation was suspended in 2023 (SSA-SITES, not part of this publication)"],
    ["LeoLabs FCC experimental test stations at Colorado Springs (mobile, WP2XKD), Menlo Park (laboratory, WP2XJJ, WJ2XLT, WN2XAM) and the Magdalena NM and Graham Pass Road CA points of WO2XXL", "licensed test places, not sensor sites; the licenses name no radar there"],
    ["Seeker-ROK (South Korea)", "listed in the page's script but not in the data it loads"],
  ],
};
