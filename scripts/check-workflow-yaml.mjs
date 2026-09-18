#!/usr/bin/env node
/**
 * Parse every GitHub Actions workflow and composite action in the repo.
 *
 * A workflow file GitHub cannot parse does not fail loudly: the run appears
 * with conclusion `failure`, ZERO jobs, and the file PATH where the workflow
 * name should be. Nothing in the logs says why, because no job ever started.
 * Every push to main between 2026-09-18 15:18 and 19:56 died that way on a
 * duplicated `with:` key introduced while adding an input to one step — seven
 * red runs that read as test failures and were not.
 *
 * So the local gate parses the files before the push does. A duplicate mapping
 * key is an error here (js-yaml `json: false`), matching GitHub's parser.
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import yaml from "js-yaml";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const ROOTS = [".github/workflows", ".github/actions"];

function* yamlFiles(dir) {
  if (!fs.existsSync(dir)) return;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) yield* yamlFiles(full);
    else if (/\.ya?ml$/.test(entry.name)) yield full;
  }
}

let checked = 0;
const failures = [];

for (const rel of ROOTS) {
  for (const file of yamlFiles(path.join(ROOT, rel))) {
    const display = path.relative(ROOT, file);
    checked += 1;
    let doc;
    try {
      doc = yaml.load(fs.readFileSync(file, "utf8"), { filename: display });
    } catch (error) {
      failures.push(`${display}: ${error.message.split("\n")[0]}`);
      continue;
    }
    if (doc === null || typeof doc !== "object") {
      failures.push(`${display}: parsed to ${doc === null ? "null" : typeof doc}, not a mapping`);
      continue;
    }
    // A workflow with no jobs and an action with no runs produce the same
    // silent zero-job failure as a parse error, so they are the same defect.
    const isAction = path.basename(file).startsWith("action.");
    if (isAction) {
      if (!doc.runs) failures.push(`${display}: composite action has no 'runs:'`);
    } else if (!doc.jobs || Object.keys(doc.jobs).length === 0) {
      failures.push(`${display}: workflow declares no jobs`);
    }
  }
}

if (failures.length > 0) {
  for (const failure of failures) console.error(`FAIL ${failure}`);
  console.error(`\n${failures.length} of ${checked} workflow/action file(s) would not start on GitHub.`);
  process.exit(1);
}

console.log(`OK: ${checked} workflow/action file(s) parse and declare jobs/runs.`);
