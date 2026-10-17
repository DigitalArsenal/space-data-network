import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const server = join(root, 'sdn-server');
const installed = join(root, 'sdn-js/node_modules');
const sds = join(installed, 'spacedatastandards.org');
assert.equal(JSON.parse(readFileSync(join(sds, 'package.json'))).version.split('+')[0], '1.253.0');
const metadata = join(server, 'internal/sds');
const manifest = JSON.parse(readFileSync(join(metadata, 'search-schemas/manifest.json')));
assert.equal(manifest.sdsVersion, '1.253.0', 'search metadata matches the published dependency');
const sha256 = file => createHash('sha256').update(readFileSync(file)).digest('hex');
for (const [input, digest] of Object.entries(manifest.inputs)) {
  const [source, ...parts] = input.split('/');
  assert.ok(['published', 'embedded'].includes(source), input);
  const base = source === 'published' ? join(sds, 'schema') : join(metadata, 'schemas');
  assert.equal(sha256(join(base, ...parts)), digest, input);
}
assert.ok(Object.keys(manifest.outputs).length >= 200, 'full routed search catalog');
for (const [output, digest] of Object.entries(manifest.outputs)) {
  assert.equal(sha256(join(metadata, 'search-schemas', output)), digest, output);
}
console.log(`PASS SDS 1.253.0 search metadata: ${Object.keys(manifest.inputs).length} input hashes, ${Object.keys(manifest.outputs).length} output hashes`);
const load = path => import(pathToFileURL(path));
const flatbuffers = await load(join(installed, 'flatbuffers/js/flatbuffers.js'));
const rfm = await load(join(sds, 'lib/js/RFM/main.js'));
const glx = await load(join(sds, 'lib/js/GLX/main.js'));
const exo = await load(join(sds, 'lib/js/EXO/main.js'));
function run(args) {
  const result = spawnSync('go', args, { cwd: server, encoding: 'utf8', timeout: 180000 });
  assert.ifError(result.error);
  assert.equal(result.status, 0, result.stdout + result.stderr);
  return result.stdout;
}
const module = JSON.parse(run(['list', '-m', '-json', 'github.com/DigitalArsenal/spacedatastandards.org/lib/go']));
assert.equal(module.Version, 'v1.253.0');
assert.equal(module.Replace, undefined, 'consume the published Go module, without a local replacement');

// Synthetic wire fixtures: this exercises installed public packages, not astronomy.
const origin = Object.assign(new rfm.RFMOriginT(), { KIND: rfm.rfmOriginKind.GALACTIC_CENTER });
const frame = Object.assign(new rfm.RFMCoordinateSystemT(), {
  AXIS_TYPE: rfm.rfmAxisType.GALACTIC, ORIGIN: origin, GALCEN_DISTANCE_PC: 8178,
});
const records = [
  ['RFM', rfm.RFM, Object.assign(new rfm.RFMT(), {
    REFERENCE_FRAME_type: rfm.RFMUnion.RFMCoordinateSystemWrapper,
    REFERENCE_FRAME: Object.assign(new rfm.RFMCoordinateSystemWrapperT(), { COORDINATE_SYSTEM: frame }),
  })],
  ['GLX', glx.GLX, Object.assign(new glx.GLXT(), {
    ID: 'fixture:published-galaxy', REDSHIFT: 0.1,
    REDSHIFT_KIND: glx.glxRedshiftTechnique.SPECTROSCOPIC,
    REDSHIFT_FRAME: glx.glxSpectralRestFrame.CMB, COSMOLOGY_ID: 'Planck18',
  })],
  ['EXO', exo.EXO, Object.assign(new exo.EXOT(), {
    PL_NAME: 'fixture:published-planet', RETRIEVED_AT: '2026-10-17T00:00:00Z',
    ARCHIVE_STATUS: exo.exoDisposition.DISPUTED,
    PL_ORBPER: Object.assign(new exo.EXOParameterT(), {
      VALUE: 3.5, UNIT: 'd', REFERENCE_BIBCODE: 'fixture:parameter-reference',
      RETRIEVED_AT: '2026-10-17T00:00:00Z',
    }),
  })],
];
const scratch = mkdtempSync(join(server, '.sds-published-e2e-'));
try {
  for (const [name, type, record] of records) {
    const builder = new flatbuffers.Builder(1024);
    type[`finish${name}Buffer`](builder, record.pack(builder));
    writeFileSync(join(scratch, name), builder.asUint8Array());
  }
  const source = String.raw`
package main
import (
  "fmt"
  "math"
  "os"
  "path/filepath"
  fb "github.com/google/flatbuffers/go"
  rfm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/RFM"
  glx "github.com/DigitalArsenal/spacedatastandards.org/lib/go/GLX"
  exo "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EXO"
)
func check(ok bool, label string) { if !ok { panic(label) } }
func bytes(name string) []byte {
  b, err := os.ReadFile(filepath.Join(os.Args[1],name)); if err != nil { panic(err) }
  check(len(b)>8 && string(b[4:8]) == "$"+name, name+" identifier")
  return b
}
func main() {
  r := rfm.GetRootAsRFM(bytes("RFM"),0)
  var table fb.Table
  check(r.REFERENCE_FRAME(&table), "RFM union")
  var wrapper rfm.RFMCoordinateSystemWrapper
  wrapper.Init(table.Bytes,table.Pos)
  f := wrapper.COORDINATE_SYSTEM(nil)
  check(f != nil && int(f.AXIS_TYPE())==33, "Galactic axes")
  check(int(f.ORIGIN(nil).KIND())==6, "Galactic center")
  check(f.GALCEN_DISTANCE_PC()==8178 && math.IsNaN(f.Z_SUN_PC()), "RFM value and missing value")
  g := glx.GetRootAsGLX(bytes("GLX"),0)
  check(string(g.ID())=="fixture:published-galaxy" && g.REDSHIFT()==0.1, "GLX identity and z")
  check(int(g.REDSHIFT_KIND())==1 && int(g.REDSHIFT_FRAME())==2, "GLX method and frame")
  check(string(g.COSMOLOGY_ID())=="Planck18" && math.IsNaN(g.DISTANCE_MPC()), "GLX cosmology and missing distance")
  e := exo.GetRootAsEXO(bytes("EXO"),0)
  check(string(e.PL_NAME())=="fixture:published-planet" && int(e.ARCHIVE_STATUS())==3, "EXO identity and status")
  p := e.PL_ORBPER(nil)
  check(p.VALUE()==3.5 && string(p.UNIT())=="d", "EXO parameter")
  check(string(p.REFERENCE_BIBCODE())=="fixture:parameter-reference", "EXO per-parameter reference")
  check(string(p.RETRIEVED_AT())==string(e.RETRIEVED_AT()) && e.LONG_ASC_NODE_UNKNOWN(), "EXO retrieval and unknown node")
  fmt.Println("PASS published SDS 1.253.0: 3 JS -> Go records; identity, provenance, enums, and missing values preserved")
}
`;
  const program = join(scratch, 'main.go');
  writeFileSync(program, source);
  process.stdout.write(run(['run', resolve(program), scratch]));
} finally {
  rmSync(scratch, { recursive: true, force: true });
}
