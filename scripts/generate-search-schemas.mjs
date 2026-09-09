// Generate reflection metadata from the embedded, published SDS schema copies.
// Usage: node scripts/generate-search-schemas.mjs PATH_TO_FLATC_WASM_JS SDS_PACKAGE
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createHash } from 'node:crypto';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const compilerPath = process.argv[2];
const sdsPackage = process.argv[3];
if (!compilerPath || !sdsPackage) throw new Error('Supply the FlatBuffers WASM CLI and the published SDS package');
const dependency = JSON.parse(await fs.readFile(path.join(sdsPackage, 'package.json')));
const goMod = await fs.readFile(path.join(root, 'sdn-server/go.mod'), 'utf8');
const version = goMod.match(/github\.com\/DigitalArsenal\/spacedatastandards\.org\/lib\/go v([^\s]+)/)?.[1];
if (dependency.name !== 'spacedatastandards.org' || dependency.version !== version)
  throw new Error('Schema includes must come from the SDS version pinned by the Go runtime');
const schemaRoot = path.join(root, 'sdn-server/internal/sds/schemas');
const destination = path.join(root, 'sdn-server/internal/sds/search-schemas');
const generated = await fs.readFile(path.join(root, 'sdn-server/internal/storage/engine_standard_catalog.go'), 'utf8');
const names = [...generated.matchAll(/"([A-Z0-9_]+\.fbs)":\s*\{Table:/g)].map(m => m[1]);
names.push('OMM.fbs', 'TBS.fbs');
names.sort();
if (names.length < 200 || new Set(names).size !== names.length) throw new Error('Unexpected routed schema catalog');
const { default: factory } = await import(pathToFileURL(path.resolve(compilerPath)));
const errors = [];
const compiler = await factory({ noInitialRun: true, print: () => {}, printErr: line => {
  if (line.includes('error:')) errors.push(line);
} });
compiler.FS.mkdir('/schemas'); compiler.FS.mkdir('/output');
const inputs = {};
const directories = new Set(['/schemas']);
const stage = (relative, bytes) => {
  const directory = path.posix.dirname(`/schemas/${relative}`);
  let current = '';
  for (const part of directory.split('/').filter(Boolean)) {
    current += '/' + part;
    if (!directories.has(current)) { compiler.FS.mkdir(current); directories.add(current); }
  }
  compiler.FS.writeFile(`/schemas/${relative}`, bytes);
};
const includeRoot = path.join(sdsPackage, 'schema');
async function includes(directory = '') {
  for (const entry of await fs.readdir(path.join(includeRoot, directory), { withFileTypes: true })) {
    const relative = path.posix.join(directory, entry.name);
    if (entry.isDirectory()) await includes(relative);
    else if (entry.isFile() && entry.name.endsWith('.fbs')) {
      const bytes = await fs.readFile(path.join(includeRoot, relative));
      stage(relative, bytes);
      inputs['published/' + relative] = createHash('sha256').update(bytes).digest('hex');
    }
  }
}
await includes();
for (const file of (await fs.readdir(schemaRoot)).filter(n => n.endsWith('.fbs')).sort()) {
  const bytes = await fs.readFile(path.join(schemaRoot, file));
  stage(`${file.slice(0, -4)}/main.fbs`, bytes);
  inputs['embedded/' + file] = createHash('sha256').update(bytes).digest('hex');
}
const outputs = new Map();
for (const name of names) {
  const priorErrors = errors.length;
  const status = compiler.callMain(['-b', '--schema', '--bfbs-builtins', '--bfbs-filenames', '/schemas',
    '-o', '/output', `/schemas/${name.slice(0, -4)}/main.fbs`]);
  if (status !== 0 || errors.length !== priorErrors) throw new Error(`${name}: ${errors.slice(priorErrors).join('; ') || `compiler status ${status}`}`);
  const bytes = compiler.FS.readFile('/output/main.bfbs').slice();
  if (Buffer.from(bytes.subarray(4, 8)).toString() !== 'BFBS') throw new Error(`${name}: expected canonical binary schema`);
  outputs.set(name.slice(0, -4) + '.bfbs', bytes);
  compiler.FS.unlink('/output/main.bfbs');
}
await fs.mkdir(destination, { recursive: true });
// No partial generation is written if any required schema fails compilation.
for (const [name, bytes] of outputs) await fs.writeFile(path.join(destination, name), bytes);
const manifest = { sdsVersion: version, compilerSha256: createHash('sha256').update(await fs.readFile(compilerPath)).digest('hex'), inputs,
  outputs: Object.fromEntries([...outputs].map(([name, bytes]) => [name, createHash('sha256').update(bytes).digest('hex')])) };
await fs.writeFile(path.join(destination, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
console.log(JSON.stringify({ schemas: outputs.size, bytes: [...outputs.values()].reduce((n, bytes) => n + bytes.length, 0) }));
