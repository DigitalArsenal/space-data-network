import { describe, it, expect } from 'vitest';
import { readFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';

import {
  resolveClosure,
  installClosure,
  MapModuleCatalog,
} from './module-dependency-resolver';
import {
  InstalledModuleRegistry,
  MemoryRegistryStore,
  createModuleInstaller,
} from './installed-module-registry';
import {
  createModuleRuntimeManager,
  MemoryModuleBytesStore,
} from './module-lifecycle';
import { createModuleHostCapabilityAdapters } from './module-host-adapters';
import { FlatSQLStorage, MemorySnapshotPersistence } from './flatsql-storage';
import { signAndPublishPnm, verifySignedPnm, PNM_TOPIC } from './pnm-publisher';
import { initHDWallet, ed25519PublicKey, sign } from './crypto/hd-wallet';

// WS6.7 integration: install spacex-starlink-source with its dependency
// closure auto-installed from PLG.DEPENDENCIES, start it under the worker
// harness, let its manifest TIMER drive a pull that stores into the FlatSQL
// store-of-record, and announce the stored batch with a signed PNM.
//
// Requires the built closed-modules artifacts; set SDN_CLOSED_MODULES_DIR to
// the space-data-network-closed-modules checkout (dist/ is gitignored there,
// so artifacts exist only in built checkouts). Skipped otherwise. The same
// arc runs for real in the browser (WS67_E2E page, chrome-devtools verified).
const SOURCE_ID = 'com.orbpro.spacex-starlink-source';
const PARSER_ID = 'org.digitalarsenal.ephem.starlink-parser';
const VALIDATOR_ID = 'org.digitalarsenal.ephem.validator';

const closedModulesDir = process.env.SDN_CLOSED_MODULES_DIR ?? '';

function artifactPath(packageName: string): string {
  return join(closedModulesDir, 'packages', packageName, 'dist', 'isomorphic', 'module.wasm');
}

const artifactsPresent =
  closedModulesDir.length > 0 &&
  ['spacex-starlink-source', 'starlink-parser', 'validator'].every((p) =>
    existsSync(artifactPath(p)),
  );

const MODULE_FILES: Record<string, string> = {
  [SOURCE_ID]: artifactPath('spacex-starlink-source'),
  [PARSER_ID]: artifactPath('starlink-parser'),
  [VALIDATOR_ID]: artifactPath('validator'),
};

describe.skipIf(!artifactsPresent)('Helia module install/run E2E (WS6.7)', () => {
  it(
    'installs the dependency closure, pulls on the timer, stores, and PNM-publishes',
    { timeout: 120_000 },
    async () => {
      const { createWorkerModuleHarness } = (await import(
        'space-data-module-sdk'
      )) as unknown as {
        createWorkerModuleHarness(options: Record<string, unknown>): Promise<{
          invokeRaw(bytes: Uint8Array): Promise<unknown>;
          readManifest(): Promise<Uint8Array | null>;
          destroy(): Promise<void>;
        }>;
      };
      const { encodePluginInvokeRequest, decodePluginManifest } = (await import(
        'space-data-module-sdk'
      )) as unknown as {
        encodePluginInvokeRequest(request: Record<string, unknown>): Uint8Array;
        decodePluginManifest(bytes: Uint8Array): {
          pluginId: string;
          version: string;
          dependencies?: Array<{ pluginId: string; minVersion?: string }>;
        };
      };

      await initHDWallet();
      const signingKey = Uint8Array.from({ length: 32 }, (_, i) => i + 33);
      const publicKey = await ed25519PublicKey(signingKey);

      const storage = await FlatSQLStorage.open({
        persistence: new MemorySnapshotPersistence(),
      });
      const adapters = createModuleHostCapabilityAdapters({
        storage,
        peerId: 'ws67-test',
        keySlots: { 'node-signing': signingKey },
      });

      const published: Array<{ topic: string; data: Uint8Array }> = [];
      const rawPublisher = {
        publish: async (topic: string, data: Uint8Array) => {
          published.push({ topic, data: new Uint8Array(data) });
        },
      };

      const ops: string[] = [];
      const storedCids: string[] = [];
      const host = {
        async invoke(operation: string, params: Record<string, unknown>) {
          ops.push(operation);
          switch (operation) {
            case 'http.request':
              await new Promise((r) => setTimeout(r, 2));
              return {
                status: 200,
                body: 'MEME_58095_STARLINK-30570_x.txt\nMEME_57408_STARLINK-30205_y.txt\n',
                body_encoding: 'utf8',
              };
            case 'storage.write': {
              const result = (await adapters.storage!.write(params)) as { cid: string };
              storedCids.push(result.cid);
              await signAndPublishPnm(rawPublisher, {
                cid: result.cid,
                fileId: 'EPHEMERIS',
                signingKey,
              });
              return result;
            }
            case 'keyslot.get':
              return adapters.walletSign!.get(params);
            case 'crypto.sign': {
              const data = Uint8Array.from(Buffer.from(String(params.data), 'base64'));
              const sig = await sign(signingKey, data);
              return { signature: Buffer.from(sig).toString('base64') };
            }
            case 'pubsub.publish':
              return true;
            default:
              throw new Error(`unexpected op ${operation}`);
          }
        },
      };

      const registry = new InstalledModuleRegistry(new MemoryRegistryStore());
      const bytesStore = new MemoryModuleBytesStore();
      const manager = createModuleRuntimeManager({
        registry,
        bytesStore,
        loadModule: async (wasmBytes) => {
          const harness = await createWorkerModuleHarness({ wasmSource: wasmBytes, host });
          return {
            invoke: (request: unknown) =>
              harness.invokeRaw(encodePluginInvokeRequest(request as Record<string, unknown>)),
            readManifest: () => harness.readManifest(),
            destroy: () => harness.destroy(),
          };
        },
        schedules: { [SOURCE_ID]: { 'starlink-pull': { intervalMs: 1000 } } },
      });

      // Root manifest — the real dependency graph embedded in the artifact.
      const sourceBytes = new Uint8Array(readFileSync(MODULE_FILES[SOURCE_ID]));
      const probeHarness = await createWorkerModuleHarness({ wasmSource: sourceBytes, host });
      const manifestBytes = new Uint8Array((await probeHarness.readManifest())!);
      await probeHarness.destroy();
      const sourceManifest = decodePluginManifest(manifestBytes);
      expect(sourceManifest.pluginId).toBe(SOURCE_ID);
      expect((sourceManifest.dependencies ?? []).map((d) => d.pluginId).sort()).toEqual([
        PARSER_ID,
        VALIDATOR_ID,
      ]);

      const catalog = new MapModuleCatalog([
        { pluginId: PARSER_ID, version: '0.1.0' },
        { pluginId: VALIDATOR_ID, version: '0.1.0' },
      ]);
      const installOrder: string[] = [];
      const installer = createModuleInstaller({
        fetchAndDecrypt: async (step) => new Uint8Array(readFileSync(MODULE_FILES[step.pluginId])),
        register: async (step, wasmBytes) => {
          installOrder.push(step.pluginId);
          await manager.install({ pluginId: step.pluginId, version: step.version, wasmBytes });
        },
        registry,
      });
      const plan = await resolveClosure(sourceManifest, registry, catalog);
      expect(plan.map((s) => s.pluginId).sort()).toEqual([PARSER_ID, VALIDATOR_ID]);
      await installClosure(sourceManifest, registry, catalog, installer);
      expect(installOrder[installOrder.length - 1]).toBe(SOURCE_ID);
      expect(installOrder.slice(0, 2).sort()).toEqual([PARSER_ID, VALIDATOR_ID]);

      const handle = await manager.start(SOURCE_ID);
      expect(handle.timerIds).toContain('starlink-pull');
      await new Promise((r) => setTimeout(r, 2600));
      await manager.stop(SOURCE_ID);

      for (const op of [
        'http.request',
        'storage.write',
        'keyslot.get',
        'crypto.sign',
        'pubsub.publish',
      ]) {
        expect(ops, `op ${op} fired`).toContain(op);
      }
      expect(storedCids.length).toBeGreaterThanOrEqual(1);
      expect(await storage.count()).toBeGreaterThanOrEqual(1);

      const pnmMessages = published.filter((p) => p.topic === PNM_TOPIC);
      expect(pnmMessages.length).toBeGreaterThanOrEqual(1);
      const evidence = await verifySignedPnm(pnmMessages[0].data, publicKey);
      expect(evidence.cid).toBe(storedCids[0]);
      expect(evidence.fileId).toBe('EPHEMERIS');
    },
  );
});
