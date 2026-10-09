#!/usr/bin/env node
/**
 * Render the whitepapers into readable site pages.
 *
 * The markdown under whitepapers/ at the repository root is the authoritative
 * copy. This writes one page per paper to docs/whitepapers/ and copies the
 * figures beside them, so the published site never holds a second, divergent
 * copy of the text. `marked` comes from the sdn-js workspace; zero external
 * origin. TeX math ($…$ inline, $$…$$ on its own
 * lines) is typeset here with KaTeX, and its stylesheet and fonts are copied
 * beside the pages, so readers get finished math with no script and no CDN.
 *
 * The typeset PDFs and the figures come from docs/typeset-whitepapers.mjs.
 *
 * Usage: node docs/build-whitepapers.mjs
 */
import { cpSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..');
const require = createRequire(join(REPO, 'sdn-js', 'package.json'));
const { marked } = require('marked');
const katex = require('katex');

const KATEX = dirname(require.resolve('katex/package.json'));
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

// Math, as GitHub reads it: $$ on its own lines for a display block, $…$
// inline. An inline opener must be followed by a non-space and a closer
// preceded by one and not followed by a digit, so prose like "$10 million"
// stays prose. A malformed formula fails the build rather than shipping raw TeX.
let hasMath = false;
function tex(source, displayMode) {
  hasMath = true;
  return katex.renderToString(source, { displayMode, throwOnError: true, strict: 'error', output: 'htmlAndMathml' });
}
marked.use({
  extensions: [
    {
      name: 'displayMath',
      level: 'block',
      start: (src) => src.match(/^\$\$/m)?.index,
      tokenizer(src) {
        const match = /^\$\$[ \t]*\n([\s\S]+?)\n\$\$[ \t]*(?:\n+|$)/.exec(src);
        if (match) return { type: 'displayMath', raw: match[0], text: match[1].trim() };
      },
      renderer: (token) => `<div class="math-display">${tex(token.text, true)}</div>\n`,
    },
    {
      name: 'inlineMath',
      level: 'inline',
      start: (src) => src.indexOf('$'),
      tokenizer(src) {
        const match = /^\$(?!\s)((?:\\.|[^$\\\n])+?)(?<!\s)\$(?!\d)/.exec(src);
        if (match) return { type: 'inlineMath', raw: match[0], text: match[1] };
      },
      renderer: (token) => tex(token.text, false),
    },
  ],
});

/** Keep in step with the cards on whitepapers.html. */
const PAPERS = [
  {
    src: 'evidence-supported-aso-catalog.md',
    title: 'Evidence-Supported ASO Catalog',
    description: 'An attributed orbital catalog for Space Data Network: evidence, provenance, uncertainty and reproducible selection for anthropogenic space objects.',
  },
  {
    src: 'fast-conjunction-assessment.md',
    title: 'Fast All-vs-All Conjunction Screening',
    description: 'Screening every catalog object against every other in minutes, for any propagator, with or without a GPU.',
  },
  {
    src: 'adversarial-security.md',
    title: 'Persistent Adversarial Security',
    description: 'Cryptocurrency balances at deterministically derived addresses as continuous, publicly verifiable proof of key integrity.',
  },
];

// A paper's link to another paper's markdown is right on GitHub; here it opens
// that paper's page.
const PAGE = new Map(PAPERS.map(({ src }) => [src, src.replace(/\.md$/, '.html')]));
marked.use({
  walkTokens(token) {
    if (token.type !== 'link') return;
    const [file, ...hash] = token.href.split('#');
    if (PAGE.has(file)) token.href = [PAGE.get(file), ...hash].join('#');
  },
});

const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

function page({ title, description, body, base, mdName, math }) {
  const u = (p) => base + p;
  const nav = [
    ['onboarding.html', 'Get started'],
    ['catalog.html', 'Catalog'],
    ['collision-avoidance.html', 'CA'],
    ['whitepapers.html', 'Whitepapers'],
    ['index.html#download', 'Download'],
  ];
  const links = (indent) => nav.map(([h, t]) => `${indent}<a href="${u(h)}"${h === 'whitepapers.html' ? ' aria-current="page"' : ''}>${t}</a>`).join('\n')
    + `\n${indent}<a href="#stack">Stack</a>`
    + `\n${indent}<a href="https://github.com/DigitalArsenal/space-data-network" target="_blank" rel="noopener">GitHub</a>`;
  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <link rel="stylesheet" href="${u('assets/sdn-chrome/sdn-chrome.css')}">
  <script src="${u('assets/sdn-chrome/sdn-chrome.js')}"></script>
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta name="description" content="${esc(description)}">
  <meta name="theme-color" content="#000000">
  <title>${esc(title)} &middot; Space Data Network</title>
  <link rel="icon" href="data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32' fill='none'><defs><linearGradient id='t' gradientUnits='userSpaceOnUse' x1='16' y1='29' x2='16' y2='3'><stop offset='0' stop-color='%23f5a524' stop-opacity='0'/><stop offset='.55' stop-color='%23f5a524' stop-opacity='.55'/><stop offset='1' stop-color='%23f5a524'/></linearGradient></defs><circle cx='16' cy='16' r='13' stroke='%23f5f5f7' stroke-width='1.6' opacity='.3'/><path d='M16 29A13 13 0 0 1 16 3' stroke='url(%23t)' stroke-width='2.2' stroke-linecap='round'/><path d='M16 10.2L10.4 20.1H21.6Z' stroke='%23f5f5f7' stroke-width='1.6' stroke-linejoin='round'/><circle cx='16' cy='10.2' r='2.6' fill='%23f5f5f7'/><circle cx='10.4' cy='20.1' r='2.6' fill='%23f5f5f7'/><circle cx='21.6' cy='20.1' r='2.6' fill='%23f5f5f7'/><circle cx='16' cy='3' r='2.5' fill='%23f5a524'/></svg>">
  <link rel="stylesheet" href="${u('site.css')}">${math ? `\n  <link rel="stylesheet" href="katex/katex.min.css">` : ''}
  <style>
    :root { --sdn-stack-footer-height: 0px; --sdn-stack-header-height: 52px; }
  </style>
</head>
<body class="paper-page">
  <header class="sdn-header">
    <div class="sdn-header-inner">
      <a class="sdn-header-brand" href="${u('index.html')}"><span>Space Data Network</span></a>
      <nav class="sdn-header-links" aria-label="Site">
${links('        ')}
      </nav>
      <div class="sdn-header-actions">
        <button data-sdn-theme-toggle aria-label="Switch between light and dark theme"></button>
        <button data-sdn-menu-toggle aria-label="Menu"></button>
      </div>
    </div>
  </header>
  <main>
    <article class="paper wrap">
      <p class="paper-back"><a href="${u('whitepapers.html')}">&larr; All whitepapers</a> &middot; <a href="${mdName.replace(/\.md$/, '.pdf')}">PDF</a> &middot; <a href="${GITHUB}${mdName}">Markdown source</a></p>
${body}
    </article>
    <section id="stack" data-sdn-stack="sdn"></section>
  </main>
  <footer class="sdn-footer">
    <div class="sdn-footer-inner">
      <a class="sdn-footer-brand" href="${u('index.html')}">Space Data Network</a>
      <nav class="sdn-footer-links" aria-label="Footer">
        <a href="${u('index.html')}">Home</a>
        <a href="${u('onboarding.html')}">Get started</a>
        <a href="${u('catalog.html')}">Catalog</a>
        <a href="${u('collision-avoidance.html')}">Collision avoidance</a>
        <a href="${u('whitepapers.html')}">Whitepapers</a>
        <a href="${u('style-guide.html')}">Style guide</a>
        <a href="${u('onboarding.html#security-review')}">IT spec sheet</a>
        <a href="https://github.com/DigitalArsenal/space-data-network">GitHub</a>
      </nav>
      <p class="sdn-footer-legal">MIT License &middot; &copy; Edgesource Corporation &middot; <a href="mailto:tj@edgesource.com">tj@edgesource.com</a></p>
    </div>
  </footer>
  <script src="${u('site.js')}"></script>
</body>
</html>
`;
}

mkdirSync(OUT, { recursive: true });
cpSync(join(SRC, 'assets'), join(OUT, 'assets'), { recursive: true });
// KaTeX's stylesheet and the woff2 faces it names first (every current browser
// takes woff2, so the woff/ttf fallbacks are not shipped).
mkdirSync(join(OUT, 'katex', 'fonts'), { recursive: true });
cpSync(join(KATEX, 'dist', 'katex.min.css'), join(OUT, 'katex', 'katex.min.css'));
for (const font of readdirSync(join(KATEX, 'dist', 'fonts')).filter((f) => f.endsWith('.woff2'))) {
  cpSync(join(KATEX, 'dist', 'fonts', font), join(OUT, 'katex', 'fonts', font));
}
for (const paper of PAPERS) {
  const out = join(OUT, paper.src.replace(/\.md$/, '.html'));
  seen = new Map();
  hasMath = false;
  const body = marked.parse(readFileSync(join(SRC, paper.src), 'utf8'), { gfm: true });
  writeFileSync(out, page({ ...paper, body, base: '../', mdName: paper.src, math: hasMath }));
  console.log('wrote', relative(REPO, out));
}
