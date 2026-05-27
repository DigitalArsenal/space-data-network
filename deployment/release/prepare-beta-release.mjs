import { appendFileSync, readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const modulePath = fileURLToPath(import.meta.url);
const repoRoot = resolve(dirname(modulePath), '../..');

function normalizeBetaTag(inputVersion, packageVersion, runNumber) {
  const explicitVersion = String(inputVersion ?? '').trim();
  const rawVersion = explicitVersion || `${String(packageVersion).replace(/^v/, '')}-beta.${runNumber}`;
  const releaseTag = rawVersion.startsWith('v') ? rawVersion : `v${rawVersion}`;

  if (!/^v\d+\.\d+\.\d+-beta(?:\.[0-9A-Za-z-]+)*$/.test(releaseTag)) {
    throw new Error(`Release version must be a beta version like v1.0.3-beta.42; received ${releaseTag}`);
  }

  return releaseTag;
}

export function computeBetaRelease({ packageVersion, runNumber, inputVersion = '' }) {
  if (!packageVersion) {
    throw new Error('packageVersion is required');
  }

  if (!String(inputVersion ?? '').trim() && !String(runNumber ?? '').trim()) {
    throw new Error('runNumber is required when no explicit beta version is provided');
  }

  const releaseTag = normalizeBetaTag(inputVersion, packageVersion, runNumber);
  const versionWithoutPrefix = releaseTag.slice(1);
  const nativePackageVersion = versionWithoutPrefix.replace('-beta', '~beta');

  return {
    releaseTag,
    packageVersion: versionWithoutPrefix,
    nativePackageVersion,
    releaseName: `Space Data Network ${releaseTag} Beta`,
    channel: 'beta',
    npmTag: 'beta'
  };
}

export function formatGithubOutput(release) {
  return [
    `release_tag=${release.releaseTag}`,
    `package_version=${release.packageVersion}`,
    `native_package_version=${release.nativePackageVersion}`,
    `release_name=${release.releaseName}`,
    `channel=${release.channel}`,
    `npm_tag=${release.npmTag}`
  ].join('\n');
}

function main(env = process.env) {
  const packageJson = JSON.parse(readFileSync(resolve(repoRoot, 'package.json'), 'utf8'));
  const release = computeBetaRelease({
    packageVersion: packageJson.version,
    runNumber: env.GITHUB_RUN_NUMBER,
    inputVersion: env.INPUT_VERSION
  });

  if (env.GITHUB_OUTPUT) {
    appendFileSync(env.GITHUB_OUTPUT, `${formatGithubOutput(release)}\n`);
  }

  console.log(JSON.stringify(release, null, 2));
}

if (process.argv[1] === modulePath) {
  try {
    main();
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  }
}
