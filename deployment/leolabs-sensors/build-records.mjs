// Build the space surveillance publications: for every network (LeoLabs and the others), its $SIT sites and its $SNR sensors
// (ground radars and telescopes bound by SITE_ID, in-orbit sensors bound by NORAD_CAT_ID), typed SDS records, nothing else.
//
//   SDS_PIN=<spacedatastandards.org checkout at the stack's pin> node build-records.mjs <out-dir>
//
// Writes <out-dir>/<network>/sit.sdn and snr.sdn (where the network has any): repeated [u32 little-endian length][bare FlatBuffer], the
// body the node's POST /api/v1/data/publish/batch/{schema} takes. No JSON is inside a record. ONE PROVIDER PER NETWORK
// (provider id `sdn-ssa-<network>`), so one network is one lane pair and can be withdrawn alone. The schemas are the pin's: SIT 1.2.0, SNR 0.0.4.
import { mkdirSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { LEOLABS_NETWORK } from "./leolabs-network.mjs";
import { NETWORKS as OTHER_NETWORKS } from "./networks.mjs";
import { ADDITIONS, NEW_NETWORKS } from "./networks-2.mjs";
import { ssnShapeOf } from "./ssn-shapes.mjs";

// Round 1 is the publication of 2026-11-18 (11 networks). Round 2 (2026-11-19) adds rows to four of those networks and adds new networks.
// A row's `round` says which batch carried it to the node: a lane is appended to, never resent, so round 2 posts only round-2 rows.
const ROUND_2_RETRIEVED_AT = "2026-11-19T00:00:00.000Z";
const inRound2 = (row) => ({ ...row, round: 2, cite: { retrievedAt: ROUND_2_RETRIEVED_AT, ...row.cite } });
const withSsnShape = (row) => {
  const shape = ssnShapeOf(row.id);
  if (!shape) return row;
  return {
    ...row,
    frame: shape.frame,
    az: shape.azDeg,
    el: shape.elDeg,
    shape,
    note: `${row.note ? `${row.note} ` : ""}${shape.cite.notes}`.trim(),
  };
};
const withAdditions = (network) => {
  const more = ADDITIONS[network.slug];
  const merged = !more
    ? network
    : {
        ...network,
        sites: [...network.sites, ...(more.sites ?? []).map(inRound2)],
        sensors: [...network.sensors, ...(more.sensors ?? []).map(inRound2)],
        inorbit: [...network.inorbit, ...(more.inorbit ?? []).map(inRound2)],
        leftOut: [...network.leftOut.filter(([what]) => !(more.nowPublished ?? []).includes(what)), ...(more.leftOut ?? [])],
      };
  if (merged.slug !== "ussf-ssn") return merged;
  return {
    ...merged,
    sensors: merged.sensors.map(withSsnShape),
    inorbit: merged.inorbit.map(withSsnShape),
  };
};
const known = new Set([LEOLABS_NETWORK, ...OTHER_NETWORKS].map((network) => network.slug));
for (const slug of Object.keys(ADDITIONS)) if (!known.has(slug)) throw new Error(`additions name a network that is not published: ${slug}`);
const newNetwork = (network) => ({ ...network, retrievedAt: ROUND_2_RETRIEVED_AT, sites: network.sites.map(inRound2), sensors: network.sensors.map(inRound2), inorbit: network.inorbit.map(inRound2) });

export const NETWORKS = [...[LEOLABS_NETWORK, ...OTHER_NETWORKS].map(withAdditions), ...NEW_NETWORKS.map(newNetwork)];
export const roundOf = (row) => row.round ?? 1;
// Round 3 (2026-11-20) adds no row: it sends a lane again, whole, because its records changed. The LeoLabs radars now carry
// their coverage outline (leolabs-shapes.mjs); their twelve $SNR records, same SENSOR_IDs, go out as the lane's next batch.
// Round 4 (2026-11-27) resends ussf-ssn $SNR records enriched with empirical TLE/OMM epoch-position shapes and ranges (ssn-shapes.mjs).
export const RESENT = {
  3: [{ network: "leolabs", schema: "SNR.fbs" }],
  4: [
    { network: "ussf-ssn", schema: "SIT.fbs" },
    { network: "ussf-ssn", schema: "SNR.fbs" },
  ],
};
export const providerOf = (network) => `sdn-ssa-${network.slug}`;
// The source name carries the network, so the console's SOURCES list reads "ussf-ssn-sites · 12" and not nine lanes called "sites".
export const SITE_SOURCE = (network) => `${network.slug}-sites`;
export const SENSOR_SOURCE = (network) => `${network.slug}-sensors`;

const PIN = process.env.SDS_PIN;
if (!PIN) throw new Error("Set SDS_PIN to the spacedatastandards.org checkout at the pin (it holds lib/js and node_modules/flatbuffers).");
const req = createRequire(path.join(PIN, "package.json"));
const fb = await import(pathToFileURL(req.resolve("flatbuffers")).href);
const lib = async (p) => import(pathToFileURL(path.join(PIN, "lib/js", p)).href);
const { SIT, SITT } = await lib("SIT/SIT.js");
const { SITCitationT } = await lib("SIT/SITCitation.js");
const { SITPartyT } = await lib("SIT/SITParty.js");
const { PVNT } = await lib("SIT/PVN.js");
const { pvnSourceKind } = await lib("SIT/pvnSourceKind.js");
const { pvnLicenseClass } = await lib("SIT/pvnLicenseClass.js");
const { SiteType } = await lib("SIT/SiteType.js");
const { sitPartyRole } = await lib("SIT/sitPartyRole.js");
const { siteLifecycleStatus } = await lib("SIT/siteLifecycleStatus.js");
const { SNR, SNRT } = await lib("SNR/SNR.js");
const { SNRSpectralBandT } = await lib("SNR/SNRSpectralBand.js");
const { SNRMountingT } = await lib("SNR/SNRMounting.js");
const { SNRRfParametersT } = await lib("SNR/SNRRfParameters.js");
const { SNROpticalParametersT } = await lib("SNR/SNROpticalParameters.js");
const { OPPProvenanceT } = await lib("SNR/OPPProvenance.js");
const { SCVSensorShapeContractT } = await lib("SNR/SCVSensorShapeContract.js");
const { SCVVec3T } = await lib("SNR/SCVVec3.js");
const { scvSensorShapeKind } = await lib("SNR/scvSensorShapeKind.js");
const { scvSensorAxisConvention } = await lib("SNR/scvSensorAxisConvention.js");
const { scvSensorRangeBoundaryKind } = await lib("SNR/scvSensorRangeBoundaryKind.js");
const { scvCoordinateFrame } = await lib("SNR/scvCoordinateFrame.js");
const { SensorType } = await lib("SNR/SensorType.js");
const { snrEmissionMode } = await lib("SNR/snrEmissionMode.js");
const { snrBandDirection } = await lib("SNR/snrBandDirection.js");
const { snrMountingFrame } = await lib("SNR/snrMountingFrame.js");
const { snrPointingMode } = await lib("SNR/snrPointingMode.js");
const { oppDeterminationMethod } = await lib("SNR/oppDeterminationMethod.js");

const RETRIEVED_AT = "2026-11-18T00:00:00.000Z";
const PRODUCER = "space-data-network deployment/leolabs-sensors 2";
export const siteId = (network, slug) => `${network.slug}:site:${slug}`;
export const sensorId = (network, id) => `${network.slug}:${id}`;
const OWNER_DECISION = "Published on the Space Data Network owner's decision of 2026-11-18, facts only, with the citation of each row.";
const OWNER_DECISION_2 = "Published on the Space Data Network owner's decision of 2026-11-19, facts only, with the citation of each row.";
const OWNER_DECISION_3 =
  "Published on the Space Data Network owner's decisions of 2026-11-18 (the facts, with the citation of each row) and 2026-11-20 (the coverage outline, with LeoLabs' permission as the owner states it, open and free).";

const pvn = (network, cite) =>
  Object.assign(new PVNT(), {
    SOURCE_ID: network.slug,
    SOURCE_KIND: pvnSourceKind.Reference,
    ORIGINATOR: network.operator,
    PROVIDER_ID: providerOf(network),
    PRODUCER,
    LICENSE_CLASS: pvnLicenseClass[network.terms.class],
    LICENSE_ID: `${network.slug}-terms`,
    LINEAGE: network.slug,
    SOURCE_URL: cite.url,
    SOURCE_VERSION: network.version ?? `retrieved ${(cite.retrievedAt ?? network.retrievedAt ?? RETRIEVED_AT).slice(0, 10)}`,
    RETRIEVED_AT: cite.retrievedAt ?? network.retrievedAt ?? RETRIEVED_AT,
  });

const citation = (network, cite, recordId, field = "register:") =>
  Object.assign(new SITCitationT(), {
    PROVENANCE: pvn(network, cite),
    SOURCE_RECORD_ID: recordId,
    SOURCE_URL: cite.url,
    SOURCE_VERSION: network.version ?? `retrieved ${(cite.retrievedAt ?? network.retrievedAt ?? RETRIEVED_AT).slice(0, 10)}`,
    SOURCE_FIELD: `${field}${recordId}`,
    RETRIEVED_AT: cite.retrievedAt ?? network.retrievedAt ?? RETRIEVED_AT,
  });

const opp = (network, cite, recordId, notes) =>
  Object.assign(new OPPProvenanceT(), {
    SOURCE: cite.source ?? cite.text,
    SOURCE_RECORD_ID: recordId,
    SOURCE_URL: cite.url,
    RETRIEVED_AT: cite.retrievedAt ?? network.retrievedAt ?? RETRIEVED_AT,
    SOURCE_VERSION: cite.version ?? network.version ?? `retrieved ${(cite.retrievedAt ?? network.retrievedAt ?? RETRIEVED_AT).slice(0, 10)}`,
    METHOD: oppDeterminationMethod.OPERATOR_STATED,
    LICENSE: cite.license ?? network.terms.text,
    ATTRIBUTION: `${network.credit}. ${cite.url}`,
    NOTES: notes,
  });

function finish(root, record) {
  const builder = new fb.Builder(1024);
  const finishName = `finish${root.name}Buffer`;
  root[finishName](builder, record.pack(builder));
  return builder.asUint8Array();
}

const siteStatus = (status) => {
  if (!(status in siteLifecycleStatus)) throw new Error(`unknown site status ${status}`);
  return siteLifecycleStatus[status];
};
const STATUS_WORDS = { OPERATING: "Operating", DECOMMISSIONED: "Retired", PERMANENT_SHUTDOWN: "Retired", PLANNED: "Planned", UNDER_CONSTRUCTION: "Under construction" };

export function sitRecords(network) {
  return network.sites.map((site) => {
    const sensors = network.sensors.filter((s) => s.site === site.slug);
    const rid = site.recordId ?? site.slug;
    const record = new SITT();
    record.ID = siteId(network, site.slug);
    record.NAME = site.name;
    record.ABBREVIATION = site.abbr;
    if (!(site.type in SiteType)) throw new Error(`unknown site type ${site.type}`);
    record.SITE_TYPE = SiteType[site.type];
    record.SITE_KIND = site.kind;
    record.NETWORK = network.name;
    record.LATITUDE = site.lat;
    record.LONGITUDE = site.lon;
    if (site.alt !== undefined) record.ALTITUDE = site.alt;
    record.CTR_ID = site.country;
    record.OPERATIONAL_STATUS = STATUS_WORDS[site.status] ?? site.status;
    record.LIFECYCLE_STATUS = siteStatus(site.status);
    if (site.statusDate) record.LIFECYCLE_STATUS_DATE = site.statusDate;
    record.SOURCE = site.cite.text;
    // Short on purpose: the console shows this text above the site's sensors, and the source line (SOURCE) and the record's citations carry the rest.
    record.DESCRIPTION = `${site.where}. ${site.note ?? ""} Position: ${site.precision}. Credit: ${network.credit}.`.replace(/\s+/g, " ").trim();
    record.DESCRIPTION_LANGUAGE = "en";
    record.POSITION_CITATION = citation(network, site.cite, rid);
    record.PARTIES = (site.parties ?? []).map((party) =>
      Object.assign(new SITPartyT(), {
        ROLE: sitPartyRole[party.role],
        NAME: party.name,
        CITATION: Object.assign(citation(network, { ...site.cite, url: party.cite ?? site.cite.url }, rid), { SOURCE_FIELD: "description" }),
      }),
    );
    record.PROVENANCE = pvn(network, site.cite);
    return { id: record.ID, record, bytes: finish(SIT, record) };
  });
}

function sensorRecord(network, row, siteRow) {
  const record = new SNRT();
  record.SENSOR_ID = sensorId(network, row.id);
  record.NAME = row.name;
  if (siteRow) record.SITE_ID = siteId(network, siteRow.slug);
  if (row.norad) record.NORAD_CAT_ID = row.norad;
  if (!(row.type in SensorType)) throw new Error(`unknown sensor type ${row.type}`);
  record.TYPE = SensorType[row.type];
  record.EMISSION = snrEmissionMode[row.emission];
  const limits = row.band && row.band.lowMHz !== undefined;
  if (row.band) {
    const band = Object.assign(new SNRSpectralBandT(), {
      NAME: row.band.name,
      DESIGNATION: row.band.designation,
      FREQ_LOW_HZ: limits ? row.band.lowMHz * 1e6 : 0,
      FREQ_HIGH_HZ: limits ? row.band.highMHz * 1e6 : 0,
      DIRECTION: row.emission === "ACTIVE" ? snrBandDirection.TRANSMIT : snrBandDirection.RECEIVE,
    });
    if (row.band.cite) band.PROVENANCE = opp(network, row.band.cite, row.band.cite.recordId ?? row.id, row.band.cite.notes ?? "");
    record.SPECTRAL_BANDS = [band];
  }
  if (row.az !== undefined && row.el !== undefined) {
    record.MOUNTING = Object.assign(new SNRMountingT(), {
      FRAME: row.frame === "LVLH" ? snrMountingFrame.LVLH : snrMountingFrame.LOCAL_ENU,
      BORESIGHT_AZIMUTH_DEG: row.az,
      BORESIGHT_ELEVATION_DEG: row.el,
      POINTING: snrPointingMode.FIXED,
      ...(row.shape?.minElevationDeg !== undefined ? { ELEVATION_LIMIT_MIN_DEG: row.shape.minElevationDeg } : {}),
      ...(row.shape?.maxElevationDeg !== undefined ? { ELEVATION_LIMIT_MAX_DEG: row.shape.maxElevationDeg } : {}),
    });
  }
  if (row.powerW !== undefined || row.bandwidthHz !== undefined) {
    record.RF = Object.assign(new SNRRfParametersT(), { PEAK_POWER_W: row.powerW ?? 0, BANDWIDTH_HZ: row.bandwidthHz ?? 0 });
  }
  if (row.apertureM !== undefined || row.focalM !== undefined) {
    record.OPTICAL = Object.assign(new SNROpticalParametersT(), { APERTURE_M: row.apertureM ?? 0, FOCAL_LENGTH_M: row.focalM ?? 0 });
  }
  // A published shape contract (leolabs-shapes.mjs CUSTOM_POLYGON or ssn-shapes.mjs RECTANGULAR / CONIC).
  if (row.shape) {
    if (row.shape.kind === "RECTANGULAR") {
      record.FIELD_OF_VIEW = Object.assign(new SCVSensorShapeContractT(), {
        SHAPE_KIND: scvSensorShapeKind.RECTANGULAR,
        AXIS_CONVENTION: scvSensorAxisConvention.LOCAL_X_RIGHT_Y_UP_Z_BORESIGHT,
        RANGE_BOUNDARY: scvSensorRangeBoundaryKind.RADIAL_SPHERICAL,
        MIN_RANGE_M: row.shape.minRangeM ?? 0,
        MAX_RANGE_M: row.shape.maxRangeM,
        X_HALF_ANGLE_DEG: row.shape.xHalfAngleDeg,
        Y_HALF_ANGLE_DEG: row.shape.yHalfAngleDeg,
      });
    } else if (row.shape.kind === "CONIC") {
      record.FIELD_OF_VIEW = Object.assign(new SCVSensorShapeContractT(), {
        SHAPE_KIND: scvSensorShapeKind.CONIC,
        AXIS_CONVENTION: scvSensorAxisConvention.LOCAL_X_RIGHT_Y_UP_Z_BORESIGHT,
        RANGE_BOUNDARY: scvSensorRangeBoundaryKind.RADIAL_SPHERICAL,
        MIN_RANGE_M: row.shape.minRangeM ?? 0,
        MAX_RANGE_M: row.shape.maxRangeM,
        INNER_HALF_ANGLE_DEG: row.shape.innerHalfAngleDeg ?? 0,
        OUTER_HALF_ANGLE_DEG: row.shape.outerHalfAngleDeg,
        MIN_CLOCK_ANGLE_DEG: row.shape.minClockAngleDeg ?? 0,
        MAX_CLOCK_ANGLE_DEG: row.shape.maxClockAngleDeg ?? 360,
      });
    } else {
      record.FIELD_OF_VIEW = Object.assign(new SCVSensorShapeContractT(), {
        SHAPE_KIND: scvSensorShapeKind.CUSTOM_POLYGON,
        AXIS_CONVENTION: scvSensorAxisConvention.LOCAL_X_RIGHT_Y_UP_Z_BORESIGHT,
        RANGE_BOUNDARY: scvSensorRangeBoundaryKind.RADIAL_SPHERICAL,
        MIN_RANGE_M: 0,
        MAX_RANGE_M: row.shape.maxRangeM,
        POLYGON_VERTICES: row.shape.vertices.map(([x, y, z]) => Object.assign(new SCVVec3T(), { X: x, Y: y, Z: z })),
        POLYGON_FRAME: scvCoordinateFrame.SENSOR_LOCAL,
      });
    }
    record.FIELD_OF_VIEW_PROVENANCE = opp(network, row.shape.cite, row.shape.cite.recordId, row.shape.cite.notes);
  }
  const where = siteRow ? ` at ${siteRow.name}` : row.host ? `, carried by ${row.host} (NORAD ${row.norad})` : "";
  const status = row.status ? ` Status: ${row.status}.` : "";
  const verified = row.verified ? ` Catalog number verified: ${row.verified}.` : "";
  const limitWords = row.band ? (limits ? ` Band limits ${row.band.lowMHz} to ${row.band.highMHz} MHz are published by the cited source.` : " Band limits are not published; none are invented.") : "";
  record.PROVENANCE = opp(network, row.cite, row.id, row.note ?? "");
  record.NOTES = `${row.name}${where}. Source: ${row.cite.text} (${row.cite.url}); credit ${network.credit}.${limitWords}${status}${verified} ${row.note ?? ""} ${row.shape ? OWNER_DECISION_3 : roundOf(row) === 2 ? OWNER_DECISION_2 : OWNER_DECISION}`.replace(/\s+/g, " ").trim();
  return { id: record.SENSOR_ID, record, bytes: finish(SNR, record) };
}

/** Ground sensors (by SITE_ID) and in-orbit sensors (by NORAD_CAT_ID) of one network. */
export function snrRecords(network) {
  const bySlug = new Map(network.sites.map((site) => [site.slug, site]));
  const ground = network.sensors.map((row) => {
    const siteRow = bySlug.get(row.site);
    if (!siteRow) throw new Error(`${network.slug}/${row.id}: site ${row.site} is not a row of the network`);
    return sensorRecord(network, row, siteRow);
  });
  const orbit = network.inorbit.map((row) => {
    if (!Number.isInteger(row.norad) || row.norad <= 0) throw new Error(`${network.slug}/${row.id}: no verified NORAD catalog number`);
    if (!row.verified) throw new Error(`${network.slug}/${row.id}: NORAD ${row.norad} is not verified against a public catalog`);
    return sensorRecord(network, row, null);
  });
  return [...ground, ...orbit];
}

export function frame(records) {
  const total = records.reduce((n, r) => n + 4 + r.bytes.length, 0);
  const out = new Uint8Array(total);
  const view = new DataView(out.buffer);
  let at = 0;
  for (const { bytes } of records) {
    view.setUint32(at, bytes.length, true);
    out.set(bytes, at + 4);
    at += 4 + bytes.length;
  }
  return out;
}

/** The lanes of one network, as the publisher posts them. */
export function lanesOf(network, round) {
  if (RESENT[round]) {
    const resent = RESENT[round].filter((lane) => lane.network === network.slug).map((lane) => lane.schema);
    return lanesOf(network).filter((lane) => resent.includes(lane.schema));
  }
  const lanes = [];
  const inRound = (rows, records) => records.filter((_, i) => round === undefined || roundOf(rows[i]) === round);
  const sites = inRound(network.sites, sitRecords(network));
  const sensors = inRound([...network.sensors, ...network.inorbit], snrRecords(network));
  if (sites.length) lanes.push({ schema: "SIT.fbs", providerId: providerOf(network), sourceName: SITE_SOURCE(network), file: "sit.sdn", records: sites, count: sites.length, sourceUrl: network.sites.find((row) => round === undefined || roundOf(row) === round).cite.url });
  if (sensors.length) lanes.push({ schema: "SNR.fbs", providerId: providerOf(network), sourceName: SENSOR_SOURCE(network), file: "snr.sdn", records: sensors, count: sensors.length, sourceUrl: [...network.sensors, ...network.inorbit].find((row) => round === undefined || roundOf(row) === round).cite.url });
  return lanes;
}

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(new URL(import.meta.url).pathname)) {
  const dir = process.argv[2];
  const roundAt = process.argv.indexOf("--round");
  const round = roundAt > 0 ? Number(process.argv[roundAt + 1]) : undefined;
  if (!dir) throw new Error("Usage: build-records.mjs <out-dir> [--round 2]   (--round writes only the rows of that round, and lanes.tsv for the host step)");
  const tsv = [];
  for (const network of NETWORKS) {
    const lanes = lanesOf(network, round);
    if (!lanes.length) continue;
    for (const lane of lanes) tsv.push([lane.schema, lane.providerId, lane.sourceName, network.slug, lane.count, lane.sourceUrl, `${lane.sourceName}-${round ?? 1}`, lanesOf(network).find((l) => l.schema === lane.schema).count].join("\t"));
    mkdirSync(path.join(dir, network.slug), { recursive: true });
    for (const lane of lanes) writeFileSync(path.join(dir, network.slug, lane.file), frame(lane.records));
    console.log(`${network.slug}: ${network.sites.length} $SIT, ${network.sensors.length} ground $SNR, ${network.inorbit.length} in-orbit $SNR, ${network.leftOut.length} left out (provider ${providerOf(network)})`);
  }
  // schema, provider, source, network, records in this batch, source address, batch id, records in the lane after the batch
  writeFileSync(path.join(dir, "lanes.tsv"), `${tsv.join("\n")}\n`);
}
