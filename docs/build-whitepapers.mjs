#!/usr/bin/env node
/**
 * Render the whitepapers into readable site pages.
 *
 * The markdown under whitepapers/ at the repository root is the authoritative
 * copy. This writes one page per paper to docs/whitepapers/ and copies the
 * figures beside them, so the published site never holds a second, divergent
 * copy of the text. Same rules as build-docs.mjs: `marked` from the sdn-js
 * workspace, no new dependency, zero external origin.
 *
 * Usage: node docs/build-whitepapers.mjs
 */
import { cpSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..');
const require = createRequire(join(REPO, 'sdn-js', 'package.json'));
const { marked } = require('marked');

const SRC = join(REPO, 'whitepapers');
const OUT = join(HERE, 'whitepapers');
const GITHUB = 'https://github.com/DigitalArsenal/space-data-network/blob/main/whitepapers/';

// GitHub-style heading ids, so the papers' own contents and reference links
// resolve on the site exactly as they do on GitHub.
let seen = new Map();
function slug(text) {
  const base = text.toLowerCase().trim().replace(/<[^>]+>/g, '').replace(/[^\p{L}\p{N}\s_-]/gu, '').replace(/\s/g, '-');
  const n = seen.get(base) || 0;
  seen.set(base, n + 1);
  return n ? `${base}-${n}` : base;
}
marked.use({
  renderer: {
    heading({ tokens, depth, text }) {
      return `<h${depth} id="${slug(text)}">${this.parser.parseInline(tokens)}</h${depth}>\n`;
    },
  },
});

/** Keep in step with the cards on whitepapers.html. */
const PAPERS = [
  {
    src: 'evidence-supported-aso-catalog.md',
    title: 'Evidence-Supported ASO Catalog',
    description: 'An attributed orbital catalog for Space Data Network: evidence, provenance, uncertainty and reproducible selection for anthropogenic space objects.',
  },
  {
    src: 'adversarial-security.md',
    title: 'Persistent Adversarial Security',
    description: 'Cryptocurrency balances at deterministically derived addresses as continuous, publicly verifiable proof of key integrity.',
  },
];

const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

function page({ title, description, body, base, mdName }) {
  const u = (p) => base + p;
  const nav = [
    ['onboarding.html', 'Get started'],
    ['catalog.html', 'Catalog'],
    ['whitepapers.html', 'Whitepapers'],
    ['index.html#download', 'Download'],
    ['server-overview.html', 'Docs'],
    ['index.html#stack', 'Stack'],
  ];
  const links = (indent) => nav.map(([h, t]) => `${indent}<a href="${u(h)}"${h === 'whitepapers.html' ? ' aria-current="page"' : ''}>${t}</a>`).join('\n')
    + `\n${indent}<a href="https://github.com/DigitalArsenal/space-data-network" target="_blank" rel="noopener">GitHub</a>`;
  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta name="description" content="${esc(description)}">
  <meta name="theme-color" content="#000000">
  <title>${esc(title)} &middot; Space Data Network</title>
  <link rel="icon" href="data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32' fill='none'><circle cx='16' cy='16' r='13' stroke='%23f5f5f7' stroke-width='1.6' opacity='.3'/><path d='M5.8 7.9A13 13 0 0 1 28.6 12.6' stroke='%23f5a524' stroke-width='2.2' stroke-linecap='round'/><path d='M16 10.2L10.4 20.1H21.6Z' stroke='%23f5f5f7' stroke-width='1.6' stroke-linejoin='round'/><circle cx='16' cy='10.2' r='2.6' fill='%23f5f5f7'/><circle cx='10.4' cy='20.1' r='2.6' fill='%23f5f5f7'/><circle cx='21.6' cy='20.1' r='2.6' fill='%23f5f5f7'/><circle cx='28.6' cy='12.6' r='2.5' fill='%23f5a524'/></svg>">
  <link rel="stylesheet" href="${u('site.css')}">
  <style>
    :root { --sdn-stack-footer-height: 0px; --sdn-stack-header-height: 52px; }
    .site-nav { height: var(--sdn-stack-header-height, 52px); }
    .nav-links a { font-size: var(--sdn-stack-header-link-size, 14px); }
  </style>
</head>
<body class="paper-page">
  <header class="site-nav">
    <div class="inner">
      <a href="${u('index.html')}" class="brand">
        <svg viewBox="0 0 32 32" fill="none" aria-hidden="true"><circle cx="16" cy="16" r="13" stroke="#f5f5f7" stroke-width="1.6" opacity=".3"/><path d="M5.8 7.9A13 13 0 0 1 28.6 12.6" stroke="#f5a524" stroke-width="2.2" stroke-linecap="round"/><path d="M16 10.2L10.4 20.1H21.6Z" stroke="#f5f5f7" stroke-width="1.6" stroke-linejoin="round"/><circle cx="16" cy="10.2" r="2.6" fill="#f5f5f7"/><circle cx="10.4" cy="20.1" r="2.6" fill="#f5f5f7"/><circle cx="21.6" cy="20.1" r="2.6" fill="#f5f5f7"/><circle cx="28.6" cy="12.6" r="2.5" fill="#f5a524"/></svg>
        Space Data Network
      </a>
      <nav class="nav-links" aria-label="Site">
${links('        ')}
      </nav>
      <details class="nav-menu">
        <summary>Menu</summary>
        <div class="menu-panel">
${links('          ')}
        </div>
      </details>
    </div>
  </header>
  <main>
    <article class="paper wrap">
      <p class="paper-back"><a href="${u('whitepapers.html')}">&larr; All whitepapers</a> &middot; <a href="${GITHUB}${mdName}">Markdown source</a></p>
${body}
    </article>
  </main>
  <footer>
    <div class="wrap">
      <nav>
        <a href="${u('onboarding.html')}">Get started</a>
        <a href="${u('catalog.html')}">Catalog</a>
        <a href="${u('collision-avoidance.html')}">Collision avoidance</a>
        <a href="${u('whitepapers.html')}">Whitepapers</a>
        <a href="${u('style-guide.html')}">Style guide</a>
        <a href="${u('server-overview.html')}">Docs</a>
        <a href="https://github.com/DigitalArsenal/space-data-network" target="_blank" rel="noopener">GitHub</a>
      </nav>
      <span>MIT License &middot; &copy; Edgesource Corporation</span>
    </div>
  </footer>
  <script src="${u('site.js')}"></script>
</body>
</html>
`;
}

mkdirSync(OUT, { recursive: true });
cpSync(join(SRC, 'assets'), join(OUT, 'assets'), { recursive: true });
for (const paper of PAPERS) {
  const out = join(OUT, paper.src.replace(/\.md$/, '.html'));
  seen = new Map();
  const body = marked.parse(readFileSync(join(SRC, paper.src), 'utf8'), { gfm: true });
  writeFileSync(out, page({ ...paper, body, base: '../', mdName: paper.src }));
  console.log('wrote', relative(REPO, out));
}
