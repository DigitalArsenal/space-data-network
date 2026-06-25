import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');

function readRepoFile(relativePath: string): string {
  return readFileSync(resolve(repoRoot, relativePath), 'utf8');
}

describe('Hydra marketplace Docker demo source contract', () => {
  it('exposes a documented package script for the Hydra marketplace demo', () => {
    const pkg = JSON.parse(readRepoFile('package.json')) as { scripts?: Record<string, string> };

    expect(pkg.scripts?.['demo:hydra-marketplace']).toBe('node deployment/scripts/hydra-marketplace-demo.mjs');
    expect(pkg.scripts?.['test:hydra-marketplace-demo']).toBe(
      'node deployment/scripts/hydra-marketplace-demo.mjs --dry-run --json',
    );
  });

  it('defines a Docker compose topology with two providers, two customers, and an observer', () => {
    const compose = readRepoFile('deployment/hydra-marketplace-demo.compose.yaml');

    for (const service of [
      'hydra-provider-maneuver',
      'hydra-provider-catalog',
      'hydra-customer-alpha',
      'hydra-customer-beta',
      'hydra-observer',
    ]) {
      expect(compose).toContain(`${service}:`);
      expect(compose).toContain(`SDN_HYDRA_ROLE=${service}`);
      expect(compose).toContain(`${service}-keys:/app/keys`);
    }
    expect(compose).toContain('/dns4/hydra-provider-maneuver/tcp/8080/ws/p2p/');
    expect(compose).not.toMatch(/tailscale|tailnet|private[-_ ]shortcut/i);
  });

  it('fails fast when Docker services exit before the discovery gate completes', () => {
    const script = readRepoFile('deployment/scripts/hydra-marketplace-demo.mjs');

    expect(script).toContain('assertDockerServicesRunning(plan, options)');
    expect(script).toContain('Docker service exited before Hydra verification');
  });

  it('dry-runs the scenario with real SDN/libp2p discovery gates and no secrets', () => {
    const output = execFileSync('node', ['deployment/scripts/hydra-marketplace-demo.mjs', '--dry-run', '--json'], {
      cwd: repoRoot,
      encoding: 'utf8',
    });
    const plan = JSON.parse(output) as {
      scenario: string;
      discovery: { mechanism: string; minWaitMs: number; forbiddenShortcuts: string[] };
      nodes: Array<{ role: string; service: string }>;
      streams: Array<{ schema: string; protectedFields: string[] }>;
      module: { protected: boolean; requiresFields: string[] };
      assertions: string[];
    };

    expect(plan.scenario).toBe('Hydra field-encrypted marketplace demo');
    expect(plan.discovery.mechanism).toBe('SDN/libp2p/IPFS');
    expect(plan.discovery.minWaitMs).toBeGreaterThanOrEqual(300_000);
    expect(plan.discovery.forbiddenShortcuts).toContain('tailscale');
    expect(plan.nodes.map((node) => node.role)).toEqual([
      'provider:maneuver-ephemeris',
      'provider:catalog-support',
      'customer:alpha',
      'customer:beta',
      'observer:unauthorized',
    ]);
    expect(plan.streams[0]).toMatchObject({
      schema: 'MPE',
      protectedFields: ['position', 'covariance_detail', 'maneuver_plan'],
    });
    expect(plan.module.protected).toBe(true);
    expect(plan.module.requiresFields).toContain('position');
    expect(plan.assertions).toEqual(
      expect.arrayContaining([
        'customer-alpha-decrypts-authorized-fields',
        'customer-beta-decrypts-different-authorized-fields',
        'observer-decrypt-fails',
        'protected-module-runs-for-authorized-customer',
        'protected-module-refuses-unauthorized-observer',
      ]),
    );
  });

  it('documents the Hydra mapping and runnable demo command', () => {
    const readme = readRepoFile('docs/hydra-marketplace-demo.md');

    expect(readme).toContain('npm run demo:hydra-marketplace');
    expect(readme).toContain('two providers');
    expect(readme).toContain('Customer A');
    expect(readme).toContain('Customer B');
    expect(readme).toContain('unauthorized observer');
    expect(readme).toContain('SDN/libp2p/IPFS');
    expect(readme).toContain('maneuver ephemeris');
    expect(readme).toContain('federated data mesh');
    expect(readme).toContain('zero-trust');
    expect(readme).toContain('observability');
  });
});
