import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import type { TestProject } from 'vitest/node';

/** The JS gate requires server-produced schemas, including in focused runs. */
export default async function setup(project: TestProject) {
  const cwd = fileURLToPath(new URL('../../sdn-server/', import.meta.url));
  const { stdout } = await promisify(execFile)('bash', [
    '../scripts/go-with-wasmedge.sh', 'test', './internal/api',
    '-run', '^TestStandardSchemaTextBrowserInterop$', '-count=1', '-v', '-timeout=5m',
  ], { cwd, env: { ...process.env, SDN_EXPORT_STANDARD_SCHEMAS: '1' }, maxBuffer: 8 * 1024 * 1024, timeout: 300_000 });
  const marker = 'SDN_STANDARD_RESPONSES=';
  const line = stdout.split('\n').find(line => line.includes(marker));
  if (!line) throw new Error('Go test did not export standard HTTP responses');
  const responses = JSON.parse(line.slice(line.indexOf(marker) + marker.length));
  project.provide('serverStandardSchemas', responses);
}
