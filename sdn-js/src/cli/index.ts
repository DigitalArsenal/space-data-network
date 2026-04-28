#!/usr/bin/env node
import {
  addUploadUser,
  loginToNode,
  readSessionCookie,
} from './auth';
import {
  listModules,
  packageModule,
  uploadModule,
} from './module-upload';
import { queryModuleDelivery } from './module-query';
import {
  createWallet,
  loadWallet,
  resolveCliHome,
} from './wallet';

type CommandHandler = (args: string[]) => Promise<void>;

const handlers: Record<string, CommandHandler> = {
  'auth:add-current-wallet': authAddCurrentWallet,
  'auth:login': authLogin,
  'module:list': moduleList,
  'module:package': modulePackage,
  'module:publish': modulePublish,
  'module:query': moduleQuery,
  'module:upload': moduleUpload,
  'wallet:init': walletInit,
  'wallet:info': walletInfo,
};

async function main(argv: string[]): Promise<void> {
  const [group, command, ...args] = argv;
  const key = `${group ?? ''}:${command ?? ''}`;
  const handler = handlers[key];
  if (!handler) {
    printUsage();
    process.exitCode = group || command ? 1 : 0;
    return;
  }
  await handler(args);
}

async function walletInit(args: string[]): Promise<void> {
  const options = parseOptions(args);
  const wallet = await createWallet({
    password: readPassword(options),
    name: options.name,
  });
  printJSON({
    wallet_home: resolveCliHome(),
    name: wallet.name,
    xpub: wallet.xpub,
    peer_id: wallet.peerId,
    signing_public_key_hex: wallet.signingPublicKeyHex,
    encryption_public_key_hex: wallet.encryptionPublicKeyHex,
  });
}

async function walletInfo(args: string[]): Promise<void> {
  const options = parseOptions(args);
  const wallet = await loadWallet({
    password: readPassword(options),
  });
  printJSON({
    wallet_home: resolveCliHome(),
    name: wallet.name,
    xpub: wallet.xpub,
    peer_id: wallet.peerId,
    signing_public_key_hex: wallet.signingPublicKeyHex,
    encryption_public_key_hex: wallet.encryptionPublicKeyHex,
  });
}

async function authLogin(args: string[]): Promise<void> {
  const options = parseOptions(args);
  const nodeUrl = requiredOption(options, 'node');
  const wallet = await loadWallet({
    password: readPassword(options),
  });
  const result = await loginToNode({
    nodeUrl,
    wallet,
  });
  printJSON({
    node_url: result.nodeUrl,
    cookie_stored: true,
    expires_at: result.expiresAt,
    user: result.user,
  });
}

async function authAddCurrentWallet(args: string[]): Promise<void> {
  const options = parseOptions(args);
  const nodeUrl = requiredOption(options, 'node');
  const trust = (options.trust || 'admin').trim();
  if (trust !== 'admin' && trust !== 'trusted' && trust !== 'standard') {
    throw new Error('--trust must be admin, trusted, or standard');
  }
  const wallet = await loadWallet({
    password: readPassword(options),
  });
  const sessionCookie = await readSessionCookie(nodeUrl);
  if (!sessionCookie) {
    throw new Error(`no session for ${nodeUrl}; run sdn auth login first`);
  }
  const result = await addUploadUser({
    nodeUrl,
    sessionCookie,
    walletInfo: wallet,
    trustLevel: trust,
  });
  printJSON(result);
}

async function modulePackage(args: string[]): Promise<void> {
  const options = parseOptions(args);
  const wallet = await loadWallet({
    password: readPassword(options),
  });
  const result = await packageModule({
    wasmPath: requiredOption(options, 'wasm'),
    outDir: options.out || 'dist',
    moduleId: requiredOption(options, 'module-id'),
    version: requiredOption(options, 'version'),
    allowedDomains: collectOptionList(options, 'allow-domain'),
    requiredScope: options['required-scope'],
    maxGrantTimeoutMs: parseOptionalInteger(options['max-grant-timeout-ms']),
    wallet,
  });
  printJSON({
    package_path: result.packagePath,
    encrypted_bundle_path: result.encryptedBundlePath,
    module_id: result.moduleId,
    version: result.version,
    bundle_sha256: result.bundleSHA256,
    signature_hex: result.signatureHex,
  });
}

async function moduleUpload(args: string[]): Promise<void> {
  const options = parseOptions(args);
  const nodeUrl = requiredOption(options, 'node');
  const sessionCookie = await readSessionCookie(nodeUrl);
  if (!sessionCookie) {
    throw new Error(`no session for ${nodeUrl}; run sdn auth login first`);
  }
  const result = await uploadModule({
    nodeUrl,
    packagePath: requiredOption(options, 'package'),
    sessionCookie,
  });
  printJSON(result);
}

async function modulePublish(args: string[]): Promise<void> {
  const options = parseOptions(args);
  const nodeUrl = requiredOption(options, 'node');
  const wallet = await loadWallet({
    password: readPassword(options),
  });
  const packaged = await packageModule({
    wasmPath: requiredOption(options, 'wasm'),
    outDir: options.out || 'dist',
    moduleId: requiredOption(options, 'module-id'),
    version: requiredOption(options, 'version'),
    allowedDomains: collectOptionList(options, 'allow-domain'),
    requiredScope: options['required-scope'],
    maxGrantTimeoutMs: parseOptionalInteger(options['max-grant-timeout-ms']),
    wallet,
  });
  const sessionCookie = await readSessionCookie(nodeUrl);
  if (!sessionCookie) {
    throw new Error(`no session for ${nodeUrl}; run sdn auth login first`);
  }
  const uploadResult = await uploadModule({
    nodeUrl,
    packagePath: packaged.packagePath,
    sessionCookie,
  });
  printJSON({
    package_path: packaged.packagePath,
    upload: uploadResult,
  });
}

async function moduleList(args: string[]): Promise<void> {
  const options = parseOptions(args);
  const nodeUrl = requiredOption(options, 'node');
  const sessionCookie = await readSessionCookie(nodeUrl);
  if (!sessionCookie) {
    throw new Error(`no session for ${nodeUrl}; run sdn auth login first`);
  }
  printJSON(await listModules({ nodeUrl, sessionCookie }));
}

async function moduleQuery(args: string[]): Promise<void> {
  const options = parseOptions(args);
  const wallet = await loadWallet({
    password: readPassword(options),
  });
  const result = await queryModuleDelivery({
    nodeUrl: requiredOption(options, 'node'),
    moduleId: requiredOption(options, 'module-id'),
    moduleVersion: options.version,
    requesterDomain: requiredOption(options, 'requester-domain'),
    requestedTimeoutMs: parseOptionalInteger(options['requested-timeout-ms']),
    wallet,
  });
  printJSON(result);
}

function parseOptions(args: string[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (!arg.startsWith('--')) {
      throw new Error(`unexpected positional argument: ${arg}`);
    }
    const eq = arg.indexOf('=');
    if (eq > 2) {
      out[arg.slice(2, eq)] = arg.slice(eq + 1);
      continue;
    }
    const key = arg.slice(2);
    const value = args[i + 1];
    if (!value || value.startsWith('--')) {
      throw new Error(`missing value for --${key}`);
    }
    if (out[key]) {
      out[key] = `${out[key]}\n${value}`;
    } else {
      out[key] = value;
    }
    i += 1;
  }
  return out;
}

function readPassword(options: Record<string, string>): string {
  const envName = options['password-env'] || 'SDN_WALLET_PASSWORD';
  const password = process.env[envName];
  if (!password) {
    throw new Error(`set ${envName} or pass --password-env NAME`);
  }
  return password;
}

function requiredOption(options: Record<string, string>, key: string): string {
  const value = options[key]?.trim();
  if (!value) {
    throw new Error(`missing --${key}`);
  }
  return value;
}

function collectOptionList(options: Record<string, string>, key: string): string[] {
  return options[key]?.split('\n').map((value) => value.trim()).filter(Boolean) ?? [];
}

function parseOptionalInteger(value: string | undefined): number | undefined {
  if (value === undefined || value.trim() === '') {
    return undefined;
  }
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) {
    throw new Error(`invalid integer: ${value}`);
  }
  return parsed;
}

function printJSON(value: unknown): void {
  process.stdout.write(`${JSON.stringify(value, null, 2)}\n`);
}

function printUsage(): void {
  process.stdout.write(`sdn wallet init --password-env SDN_WALLET_PASSWORD [--name "SDN Upload Test"]
sdn wallet info --password-env SDN_WALLET_PASSWORD
sdn auth login --node https://sdn.spaceaware.io
sdn auth add-current-wallet --node https://sdn.spaceaware.io --trust admin
sdn module package --wasm ./module.wasm --module-id com.example.test --version 0.0.1
sdn module upload --node https://sdn.spaceaware.io --package ./dist/com.example.test-0.0.1.sdn-module.json
sdn module list --node https://sdn.spaceaware.io
sdn module query --node https://sdn.spaceaware.io --module-id com.example.test --requester-domain spaceaware.io
`);
}

main(process.argv.slice(2))
  .then(() => {
    process.exit(process.exitCode ?? 0);
  })
  .catch((error: unknown) => {
    const message = error instanceof Error ? error.message : String(error);
    process.stderr.write(`sdn: ${message}\n`);
    process.exit(1);
  });
