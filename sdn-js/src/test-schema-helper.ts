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
