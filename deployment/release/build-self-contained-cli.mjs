import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cp, chmod, lstat, mkdir, readFile, readdir, readlink, rm, symlink, unlink, writeFile } from 'node:fs/promises';
import { basename, dirname, isAbsolute, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const executableMode = 0o755;
const defaultUpdateFeedBaseUrl = 'https://sdn.spaceaware.io/api/v1/updates';
const updaterModuleId = 'org.spacedatanetwork.updater';
const updaterWasmPath = 'runtime/modules/org.spacedatanetwork.updater.wasm';
const hdWalletWasmPath = 'runtime/modules/hd-wallet-wasi.wasm';

export async function stageBundle(options) {
  const version = safeToken(options.version, 'version');
  const osName = safeToken(options.os, 'os');
  const arch = safeToken(options.arch, 'arch');
  const channel = options.channel || 'beta';
  const signature = required(options.manifestSignature, 'manifestSignature');
  const bundleName = `spacedatanetwork-${version}-${osName}-${arch}`;
  const outputDir = resolve(required(options.outputDir, 'outputDir'));
  const root = resolve(outputDir, bundleName);
  assertUnderOutputDir(root, outputDir);
  await rm(root, { recursive: true, force: true });
  await mkdir(root, { recursive: true });
  await mkdir(join(root, 'bin'), { recursive: true });
  await mkdir(join(root, 'runtime', 'kubo'), { recursive: true });
  await mkdir(join(root, 'runtime', 'modules'), { recursive: true });
  await mkdir(join(root, 'runtime', 'sdn'), { recursive: true });
  await mkdir(join(root, 'runtime', 'ui'), { recursive: true });

  const exeName = osName === 'windows' ? 'spacedatanetwork.exe' : 'spacedatanetwork';
  const aliasName = osName === 'windows' ? 'sdn.exe' : 'sdn';
  const kuboName = osName === 'windows' ? 'ipfs.exe' : 'ipfs';
  if (osName === 'windows') {
    await cp(required(options.binaryPath, 'binaryPath'), join(root, 'bin', exeName));
    const bundledWasmEdgePath = join(root, 'runtime', 'wasmedge');
    await cp(required(options.wasmedgePath, 'wasmedgePath'), bundledWasmEdgePath, { recursive: true });
    await cp(join(bundledWasmEdgePath, 'bin', 'wasmedge.dll'), join(root, 'bin', 'wasmedge.dll'));
  } else {
    await cp(required(options.binaryPath, 'binaryPath'), join(root, 'runtime', 'sdn', exeName));
    const bundledWasmEdgePath = join(root, 'runtime', 'wasmedge');
    await cp(required(options.wasmedgePath, 'wasmedgePath'), bundledWasmEdgePath, { recursive: true });
    await makeSymlinksPortable(bundledWasmEdgePath);
    await writeFile(join(root, 'bin', exeName), unixLauncherScript(exeName));
  }
  await cp(required(options.kuboPath, 'kuboPath'), join(root, 'runtime', 'kubo', kuboName));
  await cp(required(options.sdnUIPath ?? options.sdnUiPath, 'sdnUIPath'), join(root, 'runtime', 'ui', 'sdn'), { recursive: true });
  await cp(required(options.webUIPath ?? options.webUiPath, 'webUIPath'), join(root, 'runtime', 'ui', 'webui'), { recursive: true });
  await cp(
    required(options.updaterWasmPath, 'updaterWasmPath'),
    join(root, updaterWasmPath),
  );
  await cp(
    required(options.hdWalletWasmPath, 'hdWalletWasmPath'),
    join(root, hdWalletWasmPath),
  );
  await cp(required(options.licensePath, 'licensePath'), join(root, 'LICENSE'));
  await cp(required(options.readmePath, 'readmePath'), join(root, 'README.md'));
  if (options.trustRootsPath) {
    await mkdir(join(root, 'trust'), { recursive: true });
    await cp(options.trustRootsPath, join(root, 'trust', 'update-roots.json'));
  }

  if (osName === 'windows') {
    await cp(join(root, 'bin', exeName), join(root, 'bin', aliasName));
  } else {
    await chmod(join(root, 'bin', exeName), executableMode);
    await chmod(join(root, 'runtime', 'sdn', exeName), executableMode);
    await chmod(join(root, 'runtime', 'kubo', kuboName), executableMode);
    await symlink(exeName, join(root, 'bin', aliasName));
  }

  const artifacts = await collectArtifacts(root);
  const manifest = {
    schema: 'org.spacedatanetwork.bundle.v1',
    version,
    channel,
    signature,
    os: osName,
    arch,
    update: {
      feedBaseUrl: options.updateFeedBaseUrl || defaultUpdateFeedBaseUrl,
      pubsubTopic: `/sdn/updates/v1/${channel}`,
      updaterModule: updaterModuleId,
      updaterWasm: updaterWasmPath,
    },
    artifacts,
  };
  await writeFile(join(root, 'manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`);
  const checksums = artifacts
    .map((artifact) => `${artifact.sha256}  ${artifact.path}`)
    .join('\n') + '\n';
  await writeFile(join(root, 'checksums.txt'), checksums);
  return { bundleName, root, os: osName };
}

export async function createArchive(staged) {
  const outputDir = dirname(staged.root);
  const archiveName = staged.os === 'windows' ? `${staged.bundleName}.zip` : `${staged.bundleName}.tar.gz`;
  const archivePath = join(outputDir, archiveName);
  await rm(archivePath, { force: true });
  if (staged.os === 'windows') {
    await run('zip', ['-qr', archivePath, staged.bundleName], { cwd: outputDir });
  } else {
    await run('tar', ['-czf', archivePath, staged.bundleName], { cwd: outputDir });
  }
  return { archiveName, path: archivePath };
}

async function collectArtifacts(root) {
  // trust/ holds the update trust roots, which (like manifest.json and
  // checksums.txt) are bundle metadata rather than swapped payload artifacts.
  const paths = (await listRelativeFiles(root, ''))
    .filter((path) => path !== 'manifest.json' && path !== 'checksums.txt' && !path.startsWith('trust/'))
    .sort();
  const artifacts = [];
  for (const path of paths) {
    const bytes = await readFile(join(root, path));
    artifacts.push({
      path,
      sha256: createHash('sha256').update(bytes).digest('hex'),
      size: bytes.length,
    });
  }
  return artifacts;
}

async function listRelativeFiles(root, prefix) {
  const entries = await readdir(join(root, prefix), { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const relativePath = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) {
      files.push(...(await listRelativeFiles(root, relativePath)));
      continue;
    }
    if (entry.isFile() || entry.isSymbolicLink()) {
      files.push(relativePath);
    }
  }
  return files;
}

async function makeSymlinksPortable(root) {
  const entries = await readdir(root, { withFileTypes: true });
  for (const entry of entries) {
    const entryPath = join(root, entry.name);
    if (entry.isDirectory()) {
      await makeSymlinksPortable(entryPath);
      continue;
    }
    if (!entry.isSymbolicLink()) {
      continue;
    }
    const target = await readlink(entryPath);
    if (!isAbsolute(target)) {
      continue;
    }
    const localTarget = basename(target);
    if (!localTarget) {
      continue;
    }
    const localTargetPath = join(dirname(entryPath), localTarget);
    if (localTargetPath === entryPath) {
      continue;
    }
    try {
      await lstat(localTargetPath);
    } catch {
      continue;
    }
    await unlink(entryPath);
    await symlink(localTarget, entryPath);
  }
}

function unixLauncherScript(exeName) {
  return `#!/bin/sh
set -eu

SCRIPT_PATH="$0"
while [ -L "$SCRIPT_PATH" ]; do
  SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$SCRIPT_PATH")" && pwd)"
  LINK_TARGET="$(readlink "$SCRIPT_PATH")"
  case "$LINK_TARGET" in
    /*) SCRIPT_PATH="$LINK_TARGET" ;;
    *) SCRIPT_PATH="$SCRIPT_DIR/$LINK_TARGET" ;;
  esac
done

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$SCRIPT_PATH")" && pwd)"
BUNDLE_ROOT="$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)"
export WASMEDGE_DIR="\${WASMEDGE_DIR:-$BUNDLE_ROOT/runtime/wasmedge}"

if [ -d "$WASMEDGE_DIR/lib" ]; then
  if [ -n "\${LD_LIBRARY_PATH:-}" ]; then
    export LD_LIBRARY_PATH="$WASMEDGE_DIR/lib:$LD_LIBRARY_PATH"
  else
    export LD_LIBRARY_PATH="$WASMEDGE_DIR/lib"
  fi
  if [ -n "\${DYLD_LIBRARY_PATH:-}" ]; then
    export DYLD_LIBRARY_PATH="$WASMEDGE_DIR/lib:$DYLD_LIBRARY_PATH"
  else
    export DYLD_LIBRARY_PATH="$WASMEDGE_DIR/lib"
  fi
fi

exec "$BUNDLE_ROOT/runtime/sdn/${exeName}" "$@"
`;
}

function required(value, name) {
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function safeToken(value, name) {
  const token = required(value, name);
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(token)) {
    throw new Error(`${name} contains unsupported characters`);
  }
  return token;
}

function assertUnderOutputDir(root, outputDir) {
  const normalizedOutputDir = outputDir.endsWith(sep) ? outputDir : `${outputDir}${sep}`;
  if (!root.startsWith(normalizedOutputDir)) {
    throw new Error(`bundle root escapes output directory: ${root}`);
  }
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!key.startsWith('--') || !value) {
      throw new Error(`Invalid argument near ${key}`);
    }
    options[key.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = value;
    index += 1;
  }
  return options;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const staged = await stageBundle(parseArgs(process.argv.slice(2)));
  const archive = await createArchive(staged);
  console.log(archive.path);
}

function run(command, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { ...options, stdio: ['ignore', 'pipe', 'pipe'] });
    let stderr = '';
    child.stderr.on('data', (chunk) => {
      stderr += chunk;
    });
    child.on('error', reject);
    child.on('close', (code) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(new Error(`${command} exited ${code}: ${stderr.trim()}`));
    });
  });
}
