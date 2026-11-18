// Build the LeoLabs publication: 8 $SIT sites and 12 $SNR radars, typed SDS records, nothing else.
//
//   SDS_PIN=<spacedatastandards.org checkout at the stack's pin> node build-records.mjs <out-dir>
//
// Writes <out-dir>/sit.sdn and <out-dir>/snr.sdn: repeated [u32 little-endian length][bare FlatBuffer], the body the node's
// POST /api/v1/data/publish/batch/{schema} takes. No JSON is inside a record. The schemas are the pin's: SIT 1.2.0, SNR 0.0.4.
import { mkdirSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { FCC_UHF, LEOLABS_URL, PAGE_EPOCH, PAGE_URL, PAPER, PAPER_URL, RADARS, SITES, VIDEO } from "./roster.mjs";

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
const { SNR, SNRT } = await lib("SNR/SNR.js");
const { SNRSpectralBandT } = await lib("SNR/SNRSpectralBand.js");
const { SNRMountingT } = await lib("SNR/SNRMounting.js");
const { SNRRfParametersT } = await lib("SNR/SNRRfParameters.js");
const { OPPProvenanceT } = await lib("SNR/OPPProvenance.js");
const { SensorType } = await lib("SNR/SensorType.js");
const { snrEmissionMode } = await lib("SNR/snrEmissionMode.js");
const { snrBandDirection } = await lib("SNR/snrBandDirection.js");
const { snrMountingFrame } = await lib("SNR/snrMountingFrame.js");
const { snrPointingMode } = await lib("SNR/snrPointingMode.js");
const { oppDeterminationMethod } = await lib("SNR/oppDeterminationMethod.js");

const CREDIT = `LeoLabs, ${LEOLABS_URL}`;
const LICENSE =
  "No license is granted by LeoLabs. LeoLabs lets its visualization be used with credit and a link to https://leolabs.space " +
  "for non-commercial educational, academic or research uses and for news, and reserves all rights " +
  "(https://platform.leolabs.space/visualizations_terms_for_sharing). Facts only (name, position, pointing, power, frequency) " +
  "are published here with that credit, on the Space Data Network owner's decision of 2026-11-18; LeoLabs can have them removed.";
const PRODUCER = "space-data-network deployment/leolabs-sensors 1";

const pvn = () =>
  Object.assign(new PVNT(), {
    SOURCE_ID: "leolabs-visualization",
    SOURCE_KIND: pvnSourceKind.Reference,
    ORIGINATOR: "LeoLabs, Inc.",
    PROVIDER_ID: "leolabs",
    PRODUCER,
    LICENSE_CLASS: pvnLicenseClass.CiteOnly,
    LICENSE_ID: "leolabs-visualization-terms",
    LINEAGE: "leolabs",
    SOURCE_URL: PAGE_URL,
    SOURCE_VERSION: `data epoch ${PAGE_EPOCH}`,
    RETRIEVED_AT: PAGE_EPOCH,
  });

const citation = (recordId, field = "register:") =>
  Object.assign(new SITCitationT(), {
    PROVENANCE: pvn(),
    SOURCE_RECORD_ID: recordId,
    SOURCE_URL: PAGE_URL,
    SOURCE_VERSION: `data epoch ${PAGE_EPOCH}`,
    SOURCE_FIELD: `${field}${recordId}`,
    RETRIEVED_AT: PAGE_EPOCH,
  });

const mhz = (value) => `${value} MHz`;
const siteId = (slug) => `leolabs:site:${slug}`;
const sensorId = (id) => `leolabs:${id}`;
const siteById = new Map(SITES.map((site) => [site.slug, site]));

export function sitRecords() {
  return SITES.map((site) => {
    const radars = RADARS.filter((radar) => site.radarIds.includes(radar.id));
    const record = new SITT();
    record.ID = siteId(site.slug);
    record.NAME = site.name;
    record.ABBREVIATION = site.abbreviation;
    record.SITE_TYPE = SiteType[site.siteType];
    record.SITE_KIND = "space surveillance radar";
    record.NETWORK = "LeoLabs";
    record.LATITUDE = site.lat;
    record.LONGITUDE = site.lon;
    record.ALTITUDE = site.alt;
    record.CTR_ID = site.country;
    record.SOURCE = `LeoLabs public visualization ${PAGE_URL}`;
    record.DESCRIPTION =
      `${site.name}: ${site.where}. ${radars.length === 1 ? "One radar" : `${radars.length} radars`} here. ` +
      `Position (height in metres) as shown on LeoLabs' public visualization (${PAGE_URL}). Credit: ${CREDIT}. ${site.note}`;
    record.DESCRIPTION_LANGUAGE = "en";
    record.POSITION_CITATION = citation(radars[0].id);
    record.PARTIES = site.parties.map((party) =>
      Object.assign(new SITPartyT(), { ROLE: sitPartyRole[party.role], NAME: party.name, CITATION: Object.assign(citation(radars[0].id), { SOURCE_URL: party.cite.match(/https?:\/\/\S+/)?.[0] ?? PAGE_URL, SOURCE_FIELD: "description" }) }),
    );
    record.PROVENANCE = pvn();
    return { id: record.ID, record, bytes: finish(SIT, record) };
  });
}

function provenance(radar, extraNotes) {
  return Object.assign(new OPPProvenanceT(), {
    SOURCE: "LeoLabs, Inc. public visualization",
    SOURCE_RECORD_ID: radar.id,
    SOURCE_URL: PAGE_URL,
    RETRIEVED_AT: PAGE_EPOCH,
    SOURCE_VERSION: `data epoch ${PAGE_EPOCH}`,
    METHOD: oppDeterminationMethod.OPERATOR_STATED,
    LICENSE,
    ATTRIBUTION: `${CREDIT}. ${PAGE_URL}`,
    NOTES: extraNotes,
  });
}

export function snrRecords() {
  return RADARS.map((radar) => {
    const site = siteById.get(radar.site);
    const peers = RADARS.filter((other) => other.site === radar.site && other.id !== radar.id);
    const limits = radar.bandLowMHz !== undefined;
    const record = new SNRT();
    record.SENSOR_ID = sensorId(radar.id);
    record.NAME = radar.name;
    record.SITE_ID = siteId(radar.site);
    record.TYPE = SensorType.UNSPECIFIED;
    record.EMISSION = snrEmissionMode.ACTIVE;
    const band = Object.assign(new SNRSpectralBandT(), {
      NAME: limits
        ? `${radar.band} band ${radar.bandLowMHz} to ${radar.bandHighMHz} MHz (${FCC_UHF.text}); the page's transmit frequency is ${mhz(radar.freqMHz)}`
        : `${radar.band} band; the page's single transmit frequency is ${mhz(radar.freqMHz)}; band limits not published`,
      DESIGNATION: radar.band,
      FREQ_LOW_HZ: limits ? radar.bandLowMHz * 1e6 : 0,
      FREQ_HIGH_HZ: limits ? radar.bandHighMHz * 1e6 : 0,
      DIRECTION: snrBandDirection.TRANSMIT,
    });
    if (limits) {
      band.PROVENANCE = Object.assign(new OPPProvenanceT(), {
        SOURCE: "United States Federal Communications Commission, experimental license WO2XME (LeoLabs, Inc.)",
        SOURCE_RECORD_ID: "WO2XME",
        SOURCE_URL: radar.bandCite.url,
        METHOD: oppDeterminationMethod.OPERATOR_STATED,
        LICENSE: "United States public record",
        ATTRIBUTION: radar.bandCite.text,
        NOTES: "The license covers the Pearce, Arizona station; it is matched to this radar by band and state.",
      });
    }
    record.SPECTRAL_BANDS = [band];
    record.MOUNTING = Object.assign(new SNRMountingT(), {
      FRAME: snrMountingFrame.LOCAL_ENU,
      BORESIGHT_AZIMUTH_DEG: radar.az,
      BORESIGHT_ELEVATION_DEG: radar.el,
      POINTING: snrPointingMode.FIXED,
    });
    record.RF = Object.assign(new SNRRfParametersT(), { PEAK_POWER_W: radar.powerW });
    record.PROVENANCE = provenance(radar, "Position, power, frequency and boresight are the page's values.");
    const shape =
      radar.kind === "planar"
        ? "The page marks it as a two-dimensional (planar) radar."
        : `${PAPER} (${PAPER_URL}) describes the fan radars as one-dimensional fields of view, UHF one fan and S-band two fans per site.`;
    record.NOTES =
      `${radar.name} at ${site.name}. As shown on LeoLabs' public visualization (${PAGE_URL}); credit ${CREDIT}. ` +
      `Transmit frequency ${mhz(radar.freqMHz)} and transmit power ${radar.powerW} W are the page's single values (the power is taken as the peak transmit power). ` +
      `${limits ? `Band limits ${radar.bandLowMHz} to ${radar.bandHighMHz} MHz are from ${FCC_UHF.text}. ` : "Band limits are not published. "}` +
      `Boresight azimuth ${radar.az}, elevation ${radar.el} degrees (local east-north-up). ` +
      `Radar type: a phased-array radar per LeoLabs; the page does not say which feed. ` +
      `Field of view and range are not set: the page publishes an outline cut at a 2000 km altitude ceiling (2100 km for PFISR), not a maximum range, beam width or minimum elevation. ` +
      `${shape} ` +
      `${peers.length ? `Paired radar at this site: ${peers.map((peer) => sensorId(peer.id)).join(", ")}. ` : ""}` +
      `${radar.id === "rasr1" ? `${VIDEO.text}: ${VIDEO.url}. ` : ""}` +
      `${radar.id === "pfisr" ? "Operated by SRI International for the National Science Foundation; LeoLabs is a user. " : ""}`.trim();
    return { id: record.SENSOR_ID, record, bytes: finish(SNR, record) };
  });
}

function finish(root, record) {
  const builder = new fb.Builder(1024);
  const finishName = `finish${root.name}Buffer`;
  root[finishName](builder, record.pack(builder));
  return builder.asUint8Array();
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

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(new URL(import.meta.url).pathname)) {
  const dir = process.argv[2];
  if (!dir) throw new Error("Usage: build-records.mjs <out-dir>");
  mkdirSync(dir, { recursive: true });
  const sites = sitRecords();
  const sensors = snrRecords();
  writeFileSync(path.join(dir, "sit.sdn"), frame(sites));
  writeFileSync(path.join(dir, "snr.sdn"), frame(sensors));
  console.log(`built ${sites.length} $SIT (${sites.reduce((n, r) => n + r.bytes.length, 0)} bytes) and ${sensors.length} $SNR (${sensors.reduce((n, r) => n + r.bytes.length, 0)} bytes) in ${dir}`);
}
