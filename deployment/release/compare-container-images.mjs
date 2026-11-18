// Compares the container image the release pushes with the image the install
// tests ran (the downloadable tarball's), before the push and again on the
// digest pulled back after it.
//
//   node deployment/release/compare-container-images.mjs \
//     --tested <image> --candidate <image> --version <version without v>
//
// Both images must be in the local Docker store. Fails unless the two have:
//   - the same platform (os, architecture, variant);
//   - the same layers, in order (RootFS diff ids);
//   - the same runtime configuration: entrypoint, command, environment, user,
//     working directory, exposed ports, volumes, stop signal, health check
//     and shell;
//   - a `version` command answering version=<version> (the release stamp,
//     SDN_RELEASE_TAG, which only shows on a running node).
// Allowed to differ, and nothing else: the labels (the pushed image carries
// the OCI source, revision and created labels the tested build does not) and
// the created time (each build stamps its own). The image ids differ for
// those two reasons only.
import { execFileSync } from 'node:child_process';
import { isDeepStrictEqual } from 'node:util';

const RUNTIME_CONFIG = [
  'Entrypoint',
  'Cmd',
  'Env',
  'User',
  'WorkingDir',
  'ExposedPorts',
  'Volumes',
  'StopSignal',
  'Healthcheck',
  'Shell',
];

function parseArgs(argv) {
  const options = {};
  for (let i = 0; i < argv.length; i += 2) {
    const key = argv[i];
    const value = argv[i + 1];
    if (!['--tested', '--candidate', '--version'].includes(key) || value === undefined) {
      throw new Error(`usage: compare-container-images.mjs --tested <image> --candidate <image> --version <version>; got ${key}`);
    }
    options[key.slice(2)] = value;
  }
  for (const name of ['tested', 'candidate', 'version']) {
    if (!options[name]) throw new Error(`--${name} is required`);
  }
  return options;
}

function inspect(image) {
  const [record] = JSON.parse(execFileSync('docker', ['image', 'inspect', image], { encoding: 'utf8' }));
  return record;
}

// The fields that decide what runs, with an absent field and an empty one
// read alike (Docker writes null, [] or {} for "none" depending on its store).
function runtimeView(record) {
  const empty = (value) => value === undefined || value === null
    || (Array.isArray(value) && value.length === 0)
    || (typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === 0)
    || value === '';
  const config = record.Config ?? {};
  const view = {
    Os: record.Os,
    Architecture: record.Architecture,
    Variant: record.Variant || '',
    Layers: record.RootFS?.Layers ?? [],
  };
  for (const field of RUNTIME_CONFIG) view[field] = empty(config[field]) ? null : config[field];
  return view;
}

function differences(tested, candidate) {
  const a = runtimeView(tested);
  const b = runtimeView(candidate);
  return Object.keys(a).filter((field) => !isDeepStrictEqual(a[field], b[field]))
    .map((field) => ({ field, tested: a[field], candidate: b[field] }));
}

function stampOf(image) {
  return execFileSync('docker', ['run', '--rm', image, 'version'], { encoding: 'utf8' }).split('\n')[0];
}

const options = parseArgs(process.argv.slice(2));
const tested = inspect(options.tested);
const candidate = inspect(options.candidate);
const found = differences(tested, candidate);
const want = `version=${options.version}`;
const stamps = { tested: stampOf(options.tested), candidate: stampOf(options.candidate) };
for (const [which, stamp] of Object.entries(stamps)) {
  if (stamp !== want) found.push({ field: `version stamp (${which})`, tested: want, candidate: stamp });
}
if (found.length > 0) {
  console.error(`::error::${options.candidate} is not the tested image ${options.tested}`);
  for (const { field, tested: a, candidate: b } of found) {
    console.error(`${field}:\n  tested:    ${JSON.stringify(a)}\n  candidate: ${JSON.stringify(b)}`);
  }
  process.exit(1);
}
console.log(`${options.candidate} is the tested image ${options.tested}: ${runtimeView(candidate).Layers.length} layers, runtime configuration and ${want} equal`);
console.log(`tested id ${tested.Id}, candidate id ${candidate.Id} (labels and created time may differ)`);
