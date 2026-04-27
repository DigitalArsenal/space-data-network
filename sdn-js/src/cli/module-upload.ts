import path from 'node:path';

import {
  packageModule,
  readModulePackage,
  type PackageModuleOptions,
  type PackagedModule,
} from './module-package';

export { packageModule };
export type { PackageModuleOptions, PackagedModule };

export interface UploadModuleOptions {
  nodeUrl: string;
  packagePath: string;
  sessionCookie: string;
  fetchImpl?: typeof fetch;
}

export interface ListModulesOptions {
  nodeUrl: string;
  sessionCookie: string;
  fetchImpl?: typeof fetch;
}

export async function uploadModule(options: UploadModuleOptions): Promise<unknown> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const nodeOrigin = normalizeNodeOrigin(options.nodeUrl);
  const { packageFile, encryptedBundlePath, encryptedBundleBytes } = await readModulePackage(options.packagePath);
  const form = new FormData();
  form.set(
    'bundle',
    new Blob([toArrayBuffer(encryptedBundleBytes)], { type: packageFile.metadata.content_type || 'application/wasm' }),
    path.basename(encryptedBundlePath),
  );
  form.set('metadata', JSON.stringify(packageFile.metadata));
  form.set('content_key_hex', packageFile.content_key_hex);
  form.set('signature_hex', packageFile.signature_hex);

  const response = await fetchImpl(`${nodeOrigin}/api/v1/plugin-modules/upload`, {
    method: 'POST',
    headers: authHeaders(options.sessionCookie),
    body: form,
  });
  if (!response.ok) {
    throw new Error(`module upload failed: ${response.status} ${await response.text()}`);
  }
  return response.json();
}

export async function listModules(options: ListModulesOptions): Promise<unknown> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const nodeOrigin = normalizeNodeOrigin(options.nodeUrl);
  const response = await fetchImpl(`${nodeOrigin}/api/v1/plugin-modules`, {
    method: 'GET',
    headers: authHeaders(options.sessionCookie),
  });
  if (!response.ok) {
    throw new Error(`module list failed: ${response.status} ${await response.text()}`);
  }
  return response.json();
}

function authHeaders(sessionCookie: string): Record<string, string> {
  return {
    Accept: 'application/json',
    Cookie: sessionCookie,
    'X-Requested-With': 'sdn-cli',
  };
}

function normalizeNodeOrigin(nodeUrl: string): string {
  return new URL(nodeUrl).origin;
}

function toArrayBuffer(bytes: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(bytes.byteLength);
  copy.set(bytes);
  return copy.buffer;
}
