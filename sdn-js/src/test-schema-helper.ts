import { inject } from 'vitest';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import { FlatcRunner } from 'flatc-wasm/runner';

const require = createRequire(import.meta.url);

/** Complete published sources; record fixtures and expected outputs stay intact. */
export function publishedSchema(code: string): { schema: string; includes: Record<string, string> } {
  const main = require.resolve(`spacedatastandards.org/schema/${code}/main.fbs`);
  const directory = path.dirname(main);
  const includes: Record<string, string> = {};
  const seen = new Set<string>();
  function visit(file: string): string {
    const source = readFileSync(file, 'utf8');
    if (seen.has(file)) return source;
    seen.add(file);
    for (const match of source.matchAll(/^include\s+"([^"]+)"\s*;/gm)) {
      const target = path.resolve(path.dirname(file), match[1]);
      includes[path.relative(directory, target).split(path.sep).join('/')] = visit(target);
    }
    return source;
  }
  return { schema: visit(main), includes };
}

/** Compile in memory; no schema or record files are generated. */
export async function compileTestSchema(source: string): Promise<Uint8Array> {
  const compiler = await FlatcRunner.init();
  compiler.mountFile('/main.fbs', source);
  compiler.mountFile('/out/.keep', '');
  const result = compiler.runCommand(['--binary', '--schema', '-o', '/out', '/main.fbs']);
  if (result.code) throw new Error(result.stderr);
  return (compiler.readFile('/out/main.bfbs') as Uint8Array).slice();
}

/** Small registered binary record for storage and module-host contracts. */
export function recordTestSchema(standardId = 'TST') {
  return { standardId, tableName: standardId, fileId: `$${standardId}`,
    schema: `table ${standardId} { VALUE:int; } root_type ${standardId}; file_identifier "$${standardId}";` };
}

export async function recordTestBytes(value: number, standardId = 'TST'): Promise<Uint8Array> {
  const { Builder } = await import('flatbuffers');
  const builder = new Builder(64);
  builder.startObject(1);
  builder.addFieldInt32(0, value, 0);
  const root = builder.endObject();
  builder.finish(root, `$${standardId}`);
  return builder.asUint8Array().slice();
}

export interface ServerStandardSchema {
  code: string;
  text: string;
  table: string;
  fileId: string;
}

declare module 'vitest' {
  export interface ProvidedContext { serverStandardSchemas: ServerStandardSchema[] }
}

/** Responses produced by the Go HTTP handler during this test run. */
export function serverStandardSchemas(): ServerStandardSchema[] {
  const responses = inject('serverStandardSchemas');
  if (!responses?.length) throw new Error('Server-produced standard schemas are absent; run scripts/ci-local.sh js with Go and WASMEDGE_DIR configured');
  return responses;
}

export function serverStandardSchema(code: string): string {
  const response = serverStandardSchemas().find(item => item.code === code);
  if (!response) throw new Error(`No served schema for ${code}`);
  return response.text;
}
