#!/usr/bin/env node

import { existsSync, readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const forbidden = [
  /supplemental[\s_-]*omm/i,
  /omm[\s_-]*supplemental/i,
  /od[\s_-]*supplemental/i,
  /ommCompat/i,
  /ommRunControl/i,
  /ommRole/i,
  /operatorOMMFlow/i,
  /github\.com\/ipfs\/kubo\/sdn\/(?:sdnruns|sdnodresults)/i,
  /(?:^|\/)(?:sdnruns|sdnodresults)(?:\/|$)/i,
  /(?:^|\/)(?:omm_(?:compat|runcontrol)|operator_omm_flow)\.go$/i,
  /^kubo\/plugin\/plugins\/sdnruntime\/celestrak_set\.go$/i,
  /^kubo\/sdn\/sdnflows\/celestrak_set\.go$/i,
  /maybeInstallCelestrakReferenceSet/i,
  /InstallCelestrakFlows/i,
  /(?:^|\/)kubo\/sdn\/flowrt\/firehistory(?:_test)?\.go$/i,
  /(?:^|\/)kubo\/sdn\/wasmrt\/(?:od_thread_proof_test|zz_od_aot_bench_test)\.go$/i,
  /triggerFirePortID/i,
  /SourceProviderPluginIDs/i,
  /ErrFireInFlight/i,
  /\bFireNow\b/i,
  /\bAbortFire\b/i,
  /\bClearBatch\b/i,
  /\bSetNodeConfig\b/i,
  /\bSetConfigLive\b/i,
  /\bSetFlowNodeConfig\b/i,
  /\bStoredConfig\b/i,
  /SDN_DEV_SEED_OMM/i,
  /SDN_OD_(?:MODULE_WASM_PATH|OEM_FB_DIR)/i,
  /(?:^|["'`\/])analysis\/od(?:[\/"'`]|$)/i,
  /\bod\.fit\b/i,
  /spacex[-_]?starlink[-_]?source/i,
  /constellation[-_]?od/i,
  /sdn[-_]?od/i,
  /syncCelestrak/i,
  /defaultCelestrak/i,
  /Celestrak(?:Catalog|Satcat|SpaceWeather)(?:CSV)?URL/i,
  /CelestrakInterval/i,
  /minCelestrakFetchInterval/i,
  /celestrakProviderID/i,
  /CombinedCelesTrak/i,
  /celesTrakDatasetSchemas/i,
  /strings\.Contains\([^\n]*["']celestrak["']/i,
  /\bsds_(?:omm|ocm|obd)\b/i,
  /\bS(?:OMM|OCM|OBD)\b/i,
  /\bflatsqlStore(?:Schema|FileIDs)\b/i,
  /\b(?:BuildTestWrapperRow|IngestTestRow)\b/i,
  /^\s*(?:(?:go|defer)\s+|(?:(?:_,\s*)?_\s*=\s*)?)[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*\.Execute\(\s*["'`]_initialize["'`](?:\s*,[^)]*)?\)\s*;?\s*(?:\/\/.*)?$/i,
];

const productionForbidden = [
  /https?:\/\/(?:www\.)?celestrak\.(?:org|com)(?:[\/:?]|$)/i,
];

const legacyCronMountForbidden = [
  /\.\s*(?:SnapshotStore|Store)\s*\(/,
];

const flowRuntimeForbidden = [
  /^\s*rt\.mod\.Execute\(\s*runtimeExportEnqueueTriggerFrame\b/,
];

const excludedDirectory = /(?:^|\/)(?:vendor|third_party)\//;

export function scanTrackedGo({ cwd = process.cwd() } = {}) {
  const listed = spawnSync('git', ['ls-files', '-z', '--', '*.go'], {
    cwd,
    encoding: null,
  });
  if (listed.error) {
    throw listed.error;
  }
  if (listed.status !== 0) {
    throw new Error(listed.stderr.toString('utf8').trim() || 'git ls-files failed');
  }

  const paths = listed.stdout.toString('utf8').split('\0').filter(Boolean);
  const violations = [];

  for (const path of paths) {
    if (excludedDirectory.test(path)) {
      continue;
    }

    const absolutePath = resolve(cwd, path);
    if (!existsSync(absolutePath)) {
      continue;
    }

    collectMatches(violations, path, 0, path);

    const contents = readFileSync(absolutePath, 'utf8');
    const lines = contents.split(/\r?\n/);
    for (let index = 0; index < lines.length; index += 1) {
      collectMatches(violations, path, index + 1, lines[index]);
      if (!path.toLowerCase().endsWith('_test.go')) {
        collectMatches(violations, path, index + 1, lines[index], productionForbidden);
      }
      if (path === 'kubo/sdn/flowrt/cronmount.go') {
        collectMatches(violations, path, index + 1, lines[index], legacyCronMountForbidden);
      }
      if (path === 'kubo/sdn/flowrt/runtime.go') {
        collectMatches(violations, path, index + 1, lines[index], flowRuntimeForbidden);
      }
    }
    collectDescriptorCountErrorMasking(violations, path, contents);
  }

  return violations;
}

function collectDescriptorCountErrorMasking(violations, path, contents) {
  const signature = /func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*\([^)]*\)\s+(?:u?int(?:8|16|32|64)?|uintptr)\s*\{/gi;
  let functionMatch;
  while ((functionMatch = signature.exec(contents)) !== null) {
    const helperName = functionMatch[1].toLowerCase();
    if (helperName !== 'calluint32' && !helperName.includes('descriptorcount')) {
      continue;
    }

    const openingBrace = signature.lastIndex - 1;
    const closingBrace = findMatchingGoBrace(contents, openingBrace);
    if (closingBrace < 0) {
      continue;
    }

    const bodyStart = openingBrace + 1;
    const body = contents.slice(bodyStart, closingBrace);
    if (!/\.Execute\s*\(/i.test(body)) {
      signature.lastIndex = closingBrace + 1;
      continue;
    }

    const zeroOnError = /if\s+[^{}\n]*\b(?:err|[A-Za-z_]\w*err)\b\s*!=\s*nil\s*\{[^{}]*\breturn\s+(?:u?int(?:8|16|32|64)?\s*\(\s*)?0(?:\s*\))?\s*(?:\/\/[^\n]*)?\s*\}/gi;
    let errorMatch;
    while ((errorMatch = zeroOnError.exec(body)) !== null) {
      violations.push({
        path,
        line: lineNumberAt(contents, bodyStart + errorMatch.index),
        pattern: 'descriptorCountZeroOnExportError',
      });
    }
    signature.lastIndex = closingBrace + 1;
  }
}

function findMatchingGoBrace(contents, openingBrace) {
  let depth = 0;
  let state = 'code';

  for (let index = openingBrace; index < contents.length; index += 1) {
    const char = contents[index];
    const next = contents[index + 1];

    if (state === 'line-comment') {
      if (char === '\n') state = 'code';
      continue;
    }
    if (state === 'block-comment') {
      if (char === '*' && next === '/') {
        state = 'code';
        index += 1;
      }
      continue;
    }
    if (state === 'raw-string') {
      if (char === '`') state = 'code';
      continue;
    }
    if (state === 'quoted-string' || state === 'rune') {
      if (char === '\\') {
        index += 1;
      } else if ((state === 'quoted-string' && char === '"') || (state === 'rune' && char === "'")) {
        state = 'code';
      }
      continue;
    }

    if (char === '/' && next === '/') {
      state = 'line-comment';
      index += 1;
    } else if (char === '/' && next === '*') {
      state = 'block-comment';
      index += 1;
    } else if (char === '`') {
      state = 'raw-string';
    } else if (char === '"') {
      state = 'quoted-string';
    } else if (char === "'") {
      state = 'rune';
    } else if (char === '{') {
      depth += 1;
    } else if (char === '}') {
      depth -= 1;
      if (depth === 0) return index;
    }
  }

  return -1;
}

function lineNumberAt(contents, offset) {
  return contents.slice(0, offset).split(/\r?\n/).length;
}

function collectMatches(violations, path, line, subject, expressions = forbidden) {
  for (const expression of expressions) {
    if (expression.test(subject)) {
      violations.push({
        path,
        line,
        pattern: expression.source,
      });
    }
  }
}

function main() {
  let violations;
  try {
    violations = scanTrackedGo();
  } catch (error) {
    console.error(`[no-app-specific-go] ERROR: ${error.message}`);
    process.exitCode = 2;
    return;
  }

  if (violations.length === 0) {
    console.log('[no-app-specific-go] PASS: tracked handwritten Go is application-blind');
    return;
  }

  console.error(`[no-app-specific-go] FAIL: ${violations.length} app-specific Go violation(s)`);
  for (const violation of violations) {
    console.error(`${violation.path}:${violation.line}:${violation.pattern}`);
  }
  process.exitCode = 1;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}
