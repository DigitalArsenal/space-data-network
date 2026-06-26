import fs from 'node:fs/promises';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const ROOT_PACKAGE_JSON_PATH = path.resolve(__dirname, '../../package.json');

type PackageJson = {
  scripts?: Record<string, string>;
  dependencies?: Record<string, string>;
  devDependencies?: Record<string, string>;
};

describe('root package CI install contract', () => {
  it('installs the binary used by the root prepare script', async () => {
    const packageJson = JSON.parse(await fs.readFile(ROOT_PACKAGE_JSON_PATH, 'utf8')) as PackageJson;
    const prepareCommand = packageJson.scripts?.prepare?.trim().split(/\s+/)[0];

    expect(prepareCommand).toBeTruthy();
    expect({
      ...packageJson.dependencies,
      ...packageJson.devDependencies,
    }).toHaveProperty(prepareCommand ?? '');
  });

  it('exposes the near-wire-speed stream acceptance check from the root package', async () => {
    const packageJson = JSON.parse(await fs.readFile(ROOT_PACKAGE_JSON_PATH, 'utf8')) as PackageJson;
    const command = packageJson.scripts?.['test:wire-speed'];

    expect(command).toBeTruthy();
    expect(command).toContain('TestLiveFlatSQLReplicationBenchmarkMeetsWireSpeedGate');
    expect(command).toContain('vitest.stress.config.mts');
  });

  it('exposes separate opt-in 256 GiB stream and fetch stress checks', async () => {
    const packageJson = JSON.parse(await fs.readFile(ROOT_PACKAGE_JSON_PATH, 'utf8')) as PackageJson;
    const streamCommand = packageJson.scripts?.['test:stream-256gb'];
    const fetchCommand = packageJson.scripts?.['test:fetch-256gb'];

    expect(streamCommand).toBeTruthy();
    expect(fetchCommand).toBeTruthy();
    expect(streamCommand).not.toBe(fetchCommand);
    expect(streamCommand).toContain('STRESS_LIVE_FLATSQL_256GB=1');
    expect(streamCommand).toContain('TestFlatSQLSyncProtocolStreamsPublishedShard256GB');
    expect(fetchCommand).toContain('STRESS_LIVE_FLATSQL_256GB=1');
    expect(fetchCommand).toContain('TestLiveFlatSQLFetchesPublishedShard256GB');
  });
});
