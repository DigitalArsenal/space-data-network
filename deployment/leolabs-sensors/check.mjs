// E2E check of the LeoLabs records: build, decode the BYTES (not the objects), and compare every published field with the
// roster. Prints each record field by field. Exit 1 on any difference.
//
//   SDS_PIN=<spacedatastandards.org at the pin> node check.mjs [stream-dir]
//
// With a stream-dir it also reads sit.sdn and snr.sdn back from there (the files the publisher POSTs, or a node's read-back).
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";
import * as fbMod from "node:module";
import { RADARS, SITES } from "./roster.mjs";
import { frame, sitRecords, snrRecords } from "./build-records.mjs";

const PIN = process.env.SDS_PIN;
const req = createRequire(path.join(PIN, "package.json"));
const fb = await import(pathToFileURL(req.resolve("flatbuffers")).href);
const lib = async (p) => import(pathToFileURL(path.join(PIN, "lib/js", p)).href);
const { SIT } = await lib("SIT/SIT.js");
const { SNR } = await lib("SNR/SNR.js");
const { SiteType } = await lib("SIT/SiteType.js");
const { SensorType } = await lib("SNR/SensorType.js");
const { snrBandDirection } = await lib("SNR/snrBandDirection.js");
const { snrEmissionMode } = await lib("SNR/snrEmissionMode.js");
const { snrMountingFrame } = await lib("SNR/snrMountingFrame.js");
const { snrPointingMode } = await lib("SNR/snrPointingMode.js");
void fbMod;

const frames = (bytes) => {
  const out = [];
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  for (let at = 0; at < bytes.length; ) {
    const n = view.getUint32(at, true);
    out.push(bytes.subarray(at + 4, at + 4 + n));
    at += 4 + n;
  }
  return out;
};
const streams = (() => {
  const dir = process.argv[2];
  if (!dir) return { sit: frame(sitRecords()), snr: frame(snrRecords()) };
  return { sit: new Uint8Array(readFileSync(path.join(dir, "sit.sdn"))), snr: new Uint8Array(readFileSync(path.join(dir, "snr.sdn"))) };
})();

const sites = frames(streams.sit).map((bytes) => {
  const buf = new fb.ByteBuffer(bytes);
  assert(SIT.bufferHasIdentifier(buf), "a frame of sit.sdn is not a $SIT");
  return SIT.getRootAsSIT(buf).unpack();
});
const sensors = frames(streams.snr).map((bytes) => {
  const buf = new fb.ByteBuffer(bytes);
  assert(SNR.bufferHasIdentifier(buf), "a frame of snr.sdn is not a $SNR");
  return SNR.getRootAsSNR(buf).unpack();
});
assert.equal(sites.length, 8, "8 sites");
assert.equal(sensors.length, 12, "12 radars");
const near = (a, b, eps) => assert(Math.abs(a - b) <= eps, `${a} differs from ${b}`);
const siteIds = new Set(sites.map((s) => s.ID));

for (const site of SITES) {
  const rec = sites.find((s) => s.ID === `leolabs:site:${site.slug}`);
  assert(rec, `site ${site.slug} missing`);
  assert.equal(rec.NAME, site.name);
  assert.equal(rec.NETWORK, "LeoLabs");
  assert.equal(rec.SITE_TYPE, SiteType[site.siteType]);
  near(rec.LATITUDE, site.lat, 2e-5);
  near(rec.LONGITUDE, site.lon, 2e-5);
  near(rec.ALTITUDE, site.alt, 1e-3);
  assert.equal(rec.CTR_ID, site.country);
  assert(rec.DESCRIPTION.includes("https://leolabs.space"), "the description credits LeoLabs with the link");
  assert(rec.PROVENANCE.SOURCE_URL.startsWith("https://platform.leolabs.space/visualizations/leo"));
  assert.equal(rec.PROVENANCE.ORIGINATOR, "LeoLabs, Inc.");
  assert.equal(rec.POSITION_CITATION.SOURCE_URL, "https://platform.leolabs.space/visualizations/leo");
  assert.equal(rec.PARTIES.length, site.parties.length);
  rec.PARTIES.forEach((party, i) => assert.equal(party.NAME, site.parties[i].name));
  console.log(`SIT ${rec.ID}\n  NAME=${rec.NAME} TYPE=${SiteType[rec.SITE_TYPE]} NETWORK=${rec.NETWORK} CTR_ID=${rec.CTR_ID}\n  LAT=${rec.LATITUDE} LON=${rec.LONGITUDE} ALT=${rec.ALTITUDE} m\n  PARTIES=${rec.PARTIES.map((p) => `${p.ROLE}:${p.NAME}`).join(" | ")}\n  PROVENANCE=${rec.PROVENANCE.ORIGINATOR} ${rec.PROVENANCE.SOURCE_URL} class=${rec.PROVENANCE.LICENSE_CLASS}`);
}
assert.equal(sites.find((s) => s.ID === "leolabs:site:pfisr").PARTIES[0].NAME, "SRI International");
assert(!/LeoLabs' radar|LeoLabs radar/.test(sites.find((s) => s.ID === "leolabs:site:pfisr").DESCRIPTION.replace("LeoLabs is a user of it, not its operator", "")), "Poker Flat is not called LeoLabs' radar");

for (const radar of RADARS) {
  const rec = sensors.find((s) => s.SENSOR_ID === `leolabs:${radar.id}`);
  assert(rec, `radar ${radar.id} missing`);
  assert.equal(rec.SITE_ID, `leolabs:site:${radar.site}`);
  assert(siteIds.has(rec.SITE_ID), "SITE_ID names a published $SIT");
  assert.equal(rec.NORAD_CAT_ID, 0, "a ground radar names no spacecraft");
  assert.equal(rec.NAME, radar.name);
  assert.equal(rec.TYPE, SensorType.UNSPECIFIED);
  assert.equal(rec.EMISSION, snrEmissionMode.ACTIVE);
  assert.equal(rec.SPECTRAL_BANDS.length, 1);
  const band = rec.SPECTRAL_BANDS[0];
  assert.equal(band.DESIGNATION, radar.band);
  assert.equal(band.DIRECTION, snrBandDirection.TRANSMIT);
  if (radar.bandLowMHz !== undefined) {
    assert.equal(band.FREQ_LOW_HZ, radar.bandLowMHz * 1e6);
    assert.equal(band.FREQ_HIGH_HZ, radar.bandHighMHz * 1e6);
    assert(band.PROVENANCE?.SOURCE_URL.includes("fcc.gov"), "limits cite the FCC");
  } else {
    assert.equal(band.FREQ_LOW_HZ, 0, `${radar.id}: no limits are published, none are invented`);
    assert.equal(band.FREQ_HIGH_HZ, 0);
  }
  assert(band.NAME.includes(`${radar.freqMHz} MHz`), "the page's transmit frequency is in the band's name");
  assert.equal(rec.MOUNTING.FRAME, snrMountingFrame.LOCAL_ENU);
  assert.equal(rec.MOUNTING.POINTING, snrPointingMode.FIXED);
  assert.equal(rec.MOUNTING.BORESIGHT_AZIMUTH_DEG, radar.az);
  assert.equal(rec.MOUNTING.BORESIGHT_ELEVATION_DEG, radar.el);
  assert.equal(rec.RF.PEAK_POWER_W, radar.powerW);
  assert.equal(rec.FIELD_OF_VIEW, null, "no field of view is published");
  assert(rec.PROVENANCE.ATTRIBUTION.includes("https://leolabs.space"));
  assert.equal(rec.PROVENANCE.SOURCE_URL, "https://platform.leolabs.space/visualizations/leo");
  assert(rec.NOTES.includes("https://leolabs.space"));
  console.log(`SNR ${rec.SENSOR_ID}\n  NAME=${rec.NAME} SITE_ID=${rec.SITE_ID} TYPE=UNSPECIFIED EMISSION=ACTIVE\n  BAND=${band.DESIGNATION} low=${band.FREQ_LOW_HZ} high=${band.FREQ_HIGH_HZ} dir=TRANSMIT name="${band.NAME}"\n  MOUNTING=LOCAL_ENU FIXED az=${rec.MOUNTING.BORESIGHT_AZIMUTH_DEG} el=${rec.MOUNTING.BORESIGHT_ELEVATION_DEG}\n  RF.PEAK_POWER_W=${rec.RF.PEAK_POWER_W} FIELD_OF_VIEW=none`);
}
console.log("LEOLABS-RECORDS PASS: 8 $SIT, 12 $SNR, every field matches the roster");
