// E2E check of the space surveillance records: build, decode the BYTES (not the objects), and compare every published field with the
// rows of every network. Prints each record field by field and a count per network. Exit 1 on any difference.
//
//   SDS_PIN=<spacedatastandards.org at the pin> node check.mjs [read-back dir]
//
// With a read-back dir it reads <dir>/<network>/sit.sdn and snr.sdn from there (a node's read-back of its lanes) instead of the builder's own bytes.
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";
import { NETWORKS, frame, lanesOf, providerOf, sensorId, siteId } from "./build-records.mjs";

const PIN = process.env.SDS_PIN;
const req = createRequire(path.join(PIN, "package.json"));
const fb = await import(pathToFileURL(req.resolve("flatbuffers")).href);
const lib = async (p) => import(pathToFileURL(path.join(PIN, "lib/js", p)).href);
const { SIT } = await lib("SIT/SIT.js");
const { SNR } = await lib("SNR/SNR.js");
const { SiteType } = await lib("SIT/SiteType.js");
const { siteLifecycleStatus } = await lib("SIT/siteLifecycleStatus.js");
const { pvnLicenseClass } = await lib("SIT/pvnLicenseClass.js");
const { SensorType } = await lib("SNR/SensorType.js");
const { snrBandDirection } = await lib("SNR/snrBandDirection.js");
const { snrEmissionMode } = await lib("SNR/snrEmissionMode.js");
const { snrMountingFrame } = await lib("SNR/snrMountingFrame.js");
const { snrPointingMode } = await lib("SNR/snrPointingMode.js");

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
const near = (a, b, eps) => assert(Math.abs(a - b) <= eps, `${a} differs from ${b}`);
const dir = process.argv[2];
const read = (network, file, builtLane) => {
  if (!dir) return builtLane ? frame(builtLane.records) : new Uint8Array();
  const p = path.join(dir, network.slug, file);
  return existsSync(p) ? new Uint8Array(readFileSync(p)) : new Uint8Array();
};

const table = [];
const allIds = new Set();
const providers = new Set();
for (const network of NETWORKS) {
  const lanes = lanesOf(network);
  const sites = frames(read(network, "sit.sdn", lanes.find((l) => l.schema === "SIT.fbs"))).map((bytes) => {
    const buf = new fb.ByteBuffer(bytes);
    assert(SIT.bufferHasIdentifier(buf), `${network.slug}: a frame of sit.sdn is not a $SIT`);
    return SIT.getRootAsSIT(buf).unpack();
  });
  const sensors = frames(read(network, "snr.sdn", lanes.find((l) => l.schema === "SNR.fbs"))).map((bytes) => {
    const buf = new fb.ByteBuffer(bytes);
    assert(SNR.bufferHasIdentifier(buf), `${network.slug}: a frame of snr.sdn is not a $SNR`);
    return SNR.getRootAsSNR(buf).unpack();
  });
  assert.equal(sites.length, network.sites.length, `${network.slug}: site count`);
  assert.equal(sensors.length, network.sensors.length + network.inorbit.length, `${network.slug}: sensor count`);
  providers.add(providerOf(network));
  console.log(`== NETWORK ${network.slug} (${network.name}) provider ${providerOf(network)}: ${sites.length} $SIT, ${network.sensors.length} ground $SNR, ${network.inorbit.length} in-orbit $SNR`);
  const siteIds = new Set(sites.map((s) => s.ID));

  for (const row of network.sites) {
    const rec = sites.find((s) => s.ID === siteId(network, row.slug));
    assert(rec, `site ${row.slug} missing`);
    assert(!allIds.has(rec.ID), `duplicate id ${rec.ID}`);
    allIds.add(rec.ID);
    assert.equal(rec.NAME, row.name);
    assert.equal(rec.NETWORK, network.name);
    assert.equal(rec.SITE_TYPE, SiteType[row.type]);
    // SIT.LATITUDE and LONGITUDE are single-precision floats: about 1e-5 degree (1 m) at 100 to 180 degrees of longitude.
    near(rec.LATITUDE, row.lat, 2e-5);
    near(rec.LONGITUDE, row.lon, 2e-5);
    near(rec.ALTITUDE, row.alt ?? 0, 1e-3);
    assert.equal(rec.CTR_ID, row.country);
    assert.equal(rec.LIFECYCLE_STATUS, siteLifecycleStatus[row.status], `${row.slug}: the lifecycle status is the row's (retired sensors are marked retired)`);
    if (["DECOMMISSIONED", "PERMANENT_SHUTDOWN"].includes(row.status)) assert(/RETIRED/.test(row.note), `${row.slug}: a retired site says RETIRED`);
    assert(rec.DESCRIPTION.includes(row.cite.url), "the description cites the row's own source");
    assert(rec.DESCRIPTION.includes(network.credit), "the description carries the credit");
    assert.equal(rec.PROVENANCE.PROVIDER_ID, providerOf(network));
    assert.equal(rec.PROVENANCE.LICENSE_CLASS, pvnLicenseClass[network.terms.class]);
    assert.equal(rec.PROVENANCE.ORIGINATOR, network.operator);
    assert.equal(rec.PROVENANCE.SOURCE_URL, row.cite.url);
    assert.equal(rec.POSITION_CITATION.SOURCE_URL, row.cite.url);
    assert.equal(rec.PARTIES.length, (row.parties ?? []).length);
    rec.PARTIES.forEach((party, i) => assert.equal(party.NAME, row.parties[i].name));
    console.log(`SIT ${rec.ID}\n  NAME=${rec.NAME} TYPE=${SiteType[rec.SITE_TYPE]} NETWORK=${rec.NETWORK} CTR_ID=${rec.CTR_ID} STATUS=${siteLifecycleStatus[rec.LIFECYCLE_STATUS]}\n  LAT=${rec.LATITUDE} LON=${rec.LONGITUDE} ALT=${rec.ALTITUDE} m\n  PARTIES=${rec.PARTIES.map((p) => p.NAME).join(" | ")}\n  PROVENANCE=${rec.PROVENANCE.PROVIDER_ID} ${rec.PROVENANCE.SOURCE_URL} class=${pvnLicenseClass[rec.PROVENANCE.LICENSE_CLASS]}`);
  }

  const wanted = [...network.sensors.map((row) => ({ row, ground: true })), ...network.inorbit.map((row) => ({ row, ground: false }))];
  for (const { row, ground } of wanted) {
    const rec = sensors.find((s) => s.SENSOR_ID === sensorId(network, row.id));
    assert(rec, `sensor ${row.id} missing`);
    assert(!allIds.has(rec.SENSOR_ID), `duplicate id ${rec.SENSOR_ID}`);
    allIds.add(rec.SENSOR_ID);
    assert.equal(rec.NAME, row.name);
    assert.equal(rec.TYPE, SensorType[row.type]);
    assert.equal(rec.EMISSION, snrEmissionMode[row.emission]);
    if (ground) {
      assert.equal(rec.SITE_ID, siteId(network, row.site));
      assert(siteIds.has(rec.SITE_ID), "SITE_ID names a published $SIT of the same network");
      assert.equal(rec.NORAD_CAT_ID, 0, "a ground sensor names no spacecraft");
    } else {
      assert.equal(rec.NORAD_CAT_ID, row.norad, "an in-orbit sensor is bound by its NORAD catalog number");
      assert(row.verified && /GCAT satcat|CelesTrak/.test(row.verified), `${row.id}: the catalog number is verified against a public catalog`);
      assert(!rec.SITE_ID, "an in-orbit sensor names no ground site");
    }
    if (row.band) {
      assert.equal(rec.SPECTRAL_BANDS.length, 1);
      const band = rec.SPECTRAL_BANDS[0];
      assert.equal(band.DESIGNATION, row.band.designation);
      assert.equal(band.NAME, row.band.name);
      assert.equal(band.DIRECTION, row.emission === "ACTIVE" ? snrBandDirection.TRANSMIT : snrBandDirection.RECEIVE);
      if (row.band.lowMHz !== undefined) {
        assert.equal(band.FREQ_LOW_HZ, row.band.lowMHz * 1e6);
        assert.equal(band.FREQ_HIGH_HZ, row.band.highMHz * 1e6);
        assert(!/allocation/i.test(band.NAME) || /NOT/.test(row.note ?? ""), `${row.id}: a federal allocation is never a sensor's band`);
      } else {
        assert.equal(band.FREQ_LOW_HZ, 0, `${row.id}: no limits are published, none are invented`);
        assert.equal(band.FREQ_HIGH_HZ, 0);
      }
    } else {
      assert.equal(rec.SPECTRAL_BANDS.length, 0, `${row.id}: no band is published, none is stored`);
    }
    if (row.az !== undefined) {
      assert.equal(rec.MOUNTING.FRAME, snrMountingFrame.LOCAL_ENU);
      assert.equal(rec.MOUNTING.POINTING, snrPointingMode.FIXED);
      assert.equal(rec.MOUNTING.BORESIGHT_AZIMUTH_DEG, row.az);
      assert.equal(rec.MOUNTING.BORESIGHT_ELEVATION_DEG, row.el);
    } else {
      assert.equal(rec.MOUNTING, null, `${row.id}: no pointing is published`);
    }
    if (row.powerW !== undefined) assert.equal(rec.RF.PEAK_POWER_W, row.powerW);
    if (row.bandwidthHz !== undefined) assert.equal(rec.RF.BANDWIDTH_HZ, row.bandwidthHz);
    if (row.apertureM !== undefined) assert.equal(rec.OPTICAL.APERTURE_M, row.apertureM);
    assert.equal(rec.FIELD_OF_VIEW, null, "no field of view is published (no outline can be stored)");
    if (row.status === "retired") assert(/RETIRED/.test(rec.NOTES), `${row.id}: a retired sensor says RETIRED`);
    assert(rec.PROVENANCE.ATTRIBUTION.includes(network.credit));
    assert.equal(rec.PROVENANCE.SOURCE_URL, row.cite.url);
    assert(rec.NOTES.includes(network.credit));
    const b = rec.SPECTRAL_BANDS[0];
    console.log(`SNR ${rec.SENSOR_ID}\n  NAME=${rec.NAME} ${ground ? `SITE_ID=${rec.SITE_ID}` : `NORAD_CAT_ID=${rec.NORAD_CAT_ID}`} TYPE=${SensorType[rec.TYPE]} EMISSION=${snrEmissionMode[rec.EMISSION]}\n  BAND=${b ? `${b.DESIGNATION} low=${b.FREQ_LOW_HZ} high=${b.FREQ_HIGH_HZ} name="${b.NAME}"` : "none"}\n  MOUNTING=${rec.MOUNTING ? `az=${rec.MOUNTING.BORESIGHT_AZIMUTH_DEG} el=${rec.MOUNTING.BORESIGHT_ELEVATION_DEG}` : "none"} RF.PEAK_POWER_W=${rec.RF?.PEAK_POWER_W ?? "none"} OPTICAL.APERTURE_M=${rec.OPTICAL?.APERTURE_M ?? "none"} FIELD_OF_VIEW=none`);
  }
  table.push({ network: network.slug, provider: providerOf(network), sites: sites.length, sensors: network.sensors.length, inorbit: network.inorbit.length, withLimits: wanted.filter(({ row }) => row.band?.lowMHz !== undefined).length, retired: wanted.filter(({ row }) => row.status === "retired").length + network.sites.filter((s) => ["DECOMMISSIONED", "PERMANENT_SHUTDOWN"].includes(s.status)).length, leftOut: network.leftOut.length });
}
assert.equal(providers.size, NETWORKS.length, "one provider per network");

console.log("\nCOUNTS PER NETWORK (sites | ground sensors | in-orbit sensors | sensors with published limits | retired rows | left out)");
for (const t of table) console.log(`  ${t.network.padEnd(20)} ${String(t.sites).padStart(3)} ${String(t.sensors).padStart(3)} ${String(t.inorbit).padStart(3)} ${String(t.withLimits).padStart(3)} ${String(t.retired).padStart(3)} ${String(t.leftOut).padStart(3)}   ${t.provider}`);
const sum = (key) => table.reduce((n, t) => n + t[key], 0);
console.log(`  ${"TOTAL".padEnd(20)} ${String(sum("sites")).padStart(3)} ${String(sum("sensors")).padStart(3)} ${String(sum("inorbit")).padStart(3)} ${String(sum("withLimits")).padStart(3)} ${String(sum("retired")).padStart(3)} ${String(sum("leftOut")).padStart(3)}`);
console.log("\nLEFT OUT, AND WHY");
for (const network of NETWORKS) for (const [what, why] of network.leftOut) console.log(`  [${network.slug}] ${what}: ${why}`);
console.log(`SSA-RECORDS PASS: ${sum("sites")} $SIT, ${sum("sensors")} ground $SNR, ${sum("inorbit")} in-orbit $SNR in ${NETWORKS.length} networks, every field matches the rows`);
