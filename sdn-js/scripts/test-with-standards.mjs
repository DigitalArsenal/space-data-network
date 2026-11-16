import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import { startVitest } from 'vitest/node';

/** The full JS gate shares one set of handler responses, entirely in memory. */
async function serverStandardSchemas() {
  const cwd = fileURLToPath(new URL('../../sdn-server/', import.meta.url));
  const { stdout } = await promisify(execFile)('bash', [
    '../scripts/go-with-wasmedge.sh', 'test', './internal/api',
    '-run', '^TestStandardSchemaTextBrowserInterop$', '-count=1', '-v', '-timeout=5m',
  ], { cwd, env: { ...process.env, SDN_EXPORT_STANDARD_SCHEMAS: '1' }, maxBuffer: 8 * 1024 * 1024, timeout: 300_000 });
  const marker = 'SDN_STANDARD_RESPONSES=';
  const line = stdout.split('\n').find(line => line.includes(marker));
  if (!line) throw new Error('Go test did not export standard HTTP responses');
  const responses = JSON.parse(line.slice(line.indexOf(marker) + marker.length));
  if (!Array.isArray(responses) || !responses.length) throw new Error('Go test exported no standard HTTP responses');
  console.log(`Go HTTP handler supplied ${responses.length} standard schemas`);
  return responses;
}

const responses = await serverStandardSchemas();
await startVitest('test', [], {
  run: true,
  provide: { serverStandardSchemas: responses },
});
