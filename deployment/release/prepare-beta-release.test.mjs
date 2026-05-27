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

test('rejects non-beta versions', () => {
  assert.throws(
    () => computeBetaRelease({ packageVersion: '1.0.3', runNumber: '42', inputVersion: 'v1.2.0' }),
    /must be a beta version/
  );
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
