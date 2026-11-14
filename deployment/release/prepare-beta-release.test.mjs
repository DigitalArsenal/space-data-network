import test from 'node:test';
import assert from 'node:assert/strict';

import { computeBetaRelease, formatGithubOutput } from './prepare-beta-release.mjs';

test('defaults to package version plus beta run number', () => {
  assert.deepEqual(computeBetaRelease({ packageVersion: '1.0.3', runNumber: '42' }), {
    releaseTag: 'v1.0.3-beta.42',
    packageVersion: '1.0.3-beta.42',
    nativePackageVersion: '1.0.3~beta.42',
    releaseName: 'Space Data Network v1.0.3-beta.42 Beta',
    channel: 'beta',
    npmTag: 'beta'
  });
});

test('normalizes explicit beta version to a v-prefixed release tag', () => {
  assert.equal(
    computeBetaRelease({ packageVersion: '1.0.3', runNumber: '42', inputVersion: '1.2.0-beta.7' }).releaseTag,
    'v1.2.0-beta.7'
  );
});

test('an explicit stable version is a stable release on the latest dist-tag', () => {
  const expected = {
    releaseTag: 'v1.1.0',
    packageVersion: '1.1.0',
    nativePackageVersion: '1.1.0',
    releaseName: 'Space Data Network v1.1.0',
    channel: 'stable',
    npmTag: 'latest'
  };
  assert.deepEqual(computeBetaRelease({ packageVersion: '1.1.0', runNumber: '42', inputVersion: 'v1.1.0' }), expected);
  assert.deepEqual(computeBetaRelease({ packageVersion: '1.1.0', runNumber: '42', inputVersion: ' 1.1.0 ' }), expected);
  assert.doesNotMatch(expected.releaseName, /Beta/);
});

test('a blank version is still the next beta, never a stable release', () => {
  const release = computeBetaRelease({ packageVersion: '1.1.0', runNumber: '7', inputVersion: '' });
  assert.equal(release.releaseTag, 'v1.1.0-beta.7');
  assert.equal(release.releaseName, 'Space Data Network v1.1.0-beta.7 Beta');
  assert.equal(release.channel, 'beta');
  assert.equal(release.npmTag, 'beta');
});

test('rejects versions that are neither beta nor stable', () => {
  for (const inputVersion of ['v1.2.0-rc.1', 'v1.2.0-alpha.1', 'v1.2', '1.2.0.1', 'latest']) {
    assert.throws(
      () => computeBetaRelease({ packageVersion: '1.0.3', runNumber: '42', inputVersion }),
      /must be a beta version like v1\.0\.3-beta\.42 or a stable version like v1\.1\.0/,
      inputVersion
    );
  }
});

test('formats GitHub output lines', () => {
  assert.equal(
    formatGithubOutput({
      releaseTag: 'v1.0.3-beta.42',
      packageVersion: '1.0.3-beta.42',
      nativePackageVersion: '1.0.3~beta.42',
      releaseName: 'Space Data Network v1.0.3-beta.42 Beta',
      channel: 'beta',
      npmTag: 'beta'
    }),
    [
      'release_tag=v1.0.3-beta.42',
      'package_version=1.0.3-beta.42',
      'native_package_version=1.0.3~beta.42',
      'release_name=Space Data Network v1.0.3-beta.42 Beta',
      'channel=beta',
      'npm_tag=beta'
    ].join('\n')
  );
});
