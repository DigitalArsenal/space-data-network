#!/usr/bin/env node

import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { DEFAULT_EXPECTED_ROLES, buildLiveDHTProofSummary, normalizeExpectedRoles } from './live-dht-client-smoke.mjs';

const PROOF_LABELS = {
  peerDiscovery: 'peer-discovery',
  identityExchange: 'identity-exchange',
  providerSearch: 'provider-search',
  dataSearch: 'data-search',
  retrievalQuery: 'retrieval-query',
  dhtRegistrationWait: 'dht-registration-wait'
};

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const key = argv[index];
    if (!key.startsWith('--')) {
      throw new Error(`Unexpected argument: ${key}`);
    }
    const value = argv[index + 1];
    if (!value || value.startsWith('--')) {
      throw new Error(`Missing value for ${key}`);
    }
    options[key.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = value;
    index += 1;
  }
  return options;
}

function walkJSON(root) {
  if (!existsSync(root)) {
    return [];
  }
  const files = [];
  const stack = [root];
  while (stack.length > 0) {
    const current = stack.pop();
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const path = join(current, entry.name);
      if (entry.isDirectory()) {
        stack.push(path);
      } else if (entry.isFile() && entry.name.endsWith('.json')) {
        files.push(path);
      }
    }
  }
  return files.sort();
}

export function summarizeReports({ reportDir, expectedRoles = DEFAULT_EXPECTED_ROLES }) {
  const reports = walkJSON(reportDir).map((file) => JSON.parse(readFileSync(file, 'utf8')));
  const roles = new Set(reports.map((report) => report.role));
  const failures = [];

  for (const role of expectedRoles) {
    if (!roles.has(role)) {
      failures.push(`missing report for ${role}`);
    }
  }
  for (const report of reports) {
    if (!report.success) {
      failures.push(`${report.role} failed: ${report.error ?? 'unknown error'}`);
      continue;
    }
    const seen = new Set(report.seenRoles ?? []);
    for (const role of expectedRoles) {
      if (!seen.has(role)) {
        failures.push(`${report.role} did not observe ${role}`);
      }
    }
    const proofs = buildLiveDHTProofSummary(report, expectedRoles);
    for (const [key, passed] of Object.entries(proofs)) {
      if (!passed) {
        failures.push(`${report.role} did not prove ${PROOF_LABELS[key] ?? key}`);
      }
    }
  }

  return { reports, failures };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const options = parseArgs(process.argv.slice(2));
  const expectedRoles = normalizeExpectedRoles(options.expectRoles);
  const { reports, failures } = summarizeReports({
    reportDir: resolve(options.reports ?? 'dist/live-dht/reports'),
    expectedRoles
  });

  console.log(JSON.stringify({ expectedRoles, reports, failures }, null, 2));
  if (failures.length > 0) {
    process.exit(1);
  }
}
