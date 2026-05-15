import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');

function readRepoFile(relativePath: string): string {
  return readFileSync(resolve(repoRoot, relativePath), 'utf8');
}

describe('local cluster deployment source guardrails', () => {
  it('mounts the generated bootstrap key at the runtime path the server reads', () => {
    const compose = readRepoFile('deployment/docker-compose.yaml');

    expect(compose).toContain('./generated/node.key:/app/keys/node.key:ro');
    expect(compose).not.toContain('./keys/node.key');
    expect(compose).not.toContain('/home/sdn/.spacedatanetwork/keys/node.key');
  });

  it('does not pin local bootstrap to a stale peer id or static Docker IP', () => {
    const compose = readRepoFile('deployment/docker-compose.yaml');

    expect(compose).toContain('${SDN_LOCAL_BOOTSTRAP_PEER_ID');
    expect(compose).toContain('/dns4/full-node-1/tcp/8080/ws/p2p/');
    expect(compose).not.toContain('12D3KooWETX4rpMhLm7UKN7ejr1ZcgF6daLqGiURkngJtFGqdTkt');
    expect(compose).not.toContain('/ip4/172.18.0.2');
  });

  it('uses a generated full-node-2 config instead of relying on ignored bootstrap env vars', () => {
    const compose = readRepoFile('deployment/docker-compose.yaml');

    expect(compose).toContain('./generated/full-node-2.yaml:/app/config/full-docker.yaml:ro');
    expect(compose).toContain('./generated/full-node-1.yaml:/app/config/full-docker.yaml:ro');
  });

  it('prepares generated local cluster files before build, up, or test', () => {
    const script = readRepoFile('deployment/scripts/local-cluster.sh');

    expect(script).toContain('cmd_prepare()');
    expect(script).toContain('node "${SCRIPT_DIR}/prepare-local-cluster.mjs"');
    expect(script).toMatch(/cmd_up\(\)[\s\S]*cmd_prepare/);
    expect(script).toMatch(/cmd_build\(\)[\s\S]*cmd_prepare/);
    expect(script).toMatch(/cmd_test\(\)[\s\S]*cmd_prepare/);
  });

  it('requires datastore convergence evidence in the Docker chaos report', () => {
    const script = readRepoFile('deployment/scripts/chaos-local-cluster.mjs');

    expect(script).toContain('chaos-local-network.mjs');
    expect(script).toContain('validateDatastoreChaosReport');
    expect(script).toContain('remoteHttpFallback');
    expect(script).toContain('sshFallback');
    expect(script).toContain('allVerified');
    expect(script).toContain('targetMet');
    expect(script).toContain('wireSpeedUtilization');
  });
});
