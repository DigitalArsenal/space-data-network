#!/usr/bin/env node
// Content-stamps the marketing pages' own script and stylesheet URLs (?v=<sha256 prefix>) so a
// republished page never pairs with a cached old asset: GitHub Pages serves them with max-age 600,
// and a reload inside that window kept the old plans.js and plans.css under the rewritten page
// (owner 2026-11-04: every data-driven section rendered empty). `--check` only reports stale stamps.
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const pages = ['docs/marketing.html', 'docs/marketing-modules.html', 'docs/marketing/deployed.html'];
const assets = path.join(root, 'docs/marketing');
const stamp = (file) => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex').slice(0, 12);

export function stampMarketingAssets({ write = false } = {}) {
  const stale = [];
  for (const page of pages) {
    const file = path.join(root, page);
    const html = fs.readFileSync(file, 'utf8');
    const next = html.replace(/\b(href|src)="([^"?#]+\.(?:css|js))(\?v=[0-9a-f]*)?"/g, (match, attr, url, v) => {
      const target = path.resolve(path.dirname(file), url);
      if (!target.startsWith(assets + path.sep) || !fs.existsSync(target)) return match;
      const want = `?v=${stamp(target)}`;
      if (v !== want) stale.push(`${page}: ${url}${v || ''} -> ${url}${want}`);
      return `${attr}="${url}${want}"`;
    });
    if (write && next !== html) fs.writeFileSync(file, next);
  }
  return stale;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const check = process.argv.includes('--check');
  const stale = stampMarketingAssets({ write: !check });
  for (const line of stale) console.log(`${check ? 'stale' : 'stamped'} ${line}`);
  if (check && stale.length) {
    console.error(`marketing assets: ${stale.length} stale reference(s); run node scripts/marketing-assets-stamp.mjs`);
    process.exit(1);
  }
}
