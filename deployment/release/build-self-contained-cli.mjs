import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cp, chmod, mkdir, readdir, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import { dirname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const executableMode = 0o755;

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
  await mkdir(join(root, 'runtime', 'ui'), { recursive: true });

  const exeName = osName === 'windows' ? 'spacedatanetwork.exe' : 'spacedatanetwork';
  const aliasName = osName === 'windows' ? 'sdn.exe' : 'sdn';
  const kuboName = osName === 'windows' ? 'ipfs.exe' : 'ipfs';
  await cp(required(options.binaryPath, 'binaryPath'), join(root, 'bin', exeName));
  await cp(required(options.kuboPath, 'kuboPath'), join(root, 'runtime', 'kubo', kuboName));
  await cp(required(options.sdnUIPath, 'sdnUIPath'), join(root, 'runtime', 'ui', 'sdn'), { recursive: true });
  await cp(required(options.webUIPath, 'webUIPath'), join(root, 'runtime', 'ui', 'webui'), { recursive: true });
  await cp(
    required(options.updaterWasmPath, 'updaterWasmPath'),
    join(root, 'runtime', 'modules', 'org.spacedatanetwork.updater.wasm'),
  );
  await cp(required(options.licensePath, 'licensePath'), join(root, 'LICENSE'));
  await cp(required(options.readmePath, 'readmePath'), join(root, 'README.md'));

  if (osName === 'windows') {
    await cp(join(root, 'bin', exeName), join(root, 'bin', aliasName));
  } else {
    await chmod(join(root, 'bin', exeName), executableMode);
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
  const paths = (await listRelativeFiles(root, ''))
    .filter((path) => path !== 'manifest.json' && path !== 'checksums.txt')
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
