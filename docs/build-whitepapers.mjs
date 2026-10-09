#!/usr/bin/env node
/**
 * Render the whitepapers into the site's paper pages and their index.
 *
 * The markdown under whitepapers/ at the repository root is the authoritative
 * copy. This writes one page per paper to docs/whitepapers/, copies the
 * figures beside them, and regenerates the cards on docs/whitepapers.html, so
 * the published site never holds a second, divergent copy of the text. Each
 * paper's front matter (everything before its first "##" heading: title,
 * subtitle, authors, affiliations, edition, evidence cutoff) is parsed into the
 * page header and the index card. `marked` and KaTeX come from NODE_PATH or the
 * sdn-js workspace; zero external origin. TeX math ($…$ inline, $$…$$ on its
 * own lines) is typeset here with KaTeX, and its stylesheet and fonts are
 * copied beside the pages, so readers get finished math with no script and no
 * CDN.
 *
 * Interactive models: docs/whitepapers-app/models.json is a copy of the
 * experiments site's models/index.json ([{paper, section_id, title, url}], url
 * relative to that site's root). Each entry whose section exists gets a "Run
 * it" control that opens the model in a side panel. `--sync-models` refreshes
 * the copy from the live manifest first; the build itself reads only files, so
 * two runs on one tree write identical bytes.
 *
 * Intro reels: docs/media/papers/<paper>/{reel.webm,reel.mp4,poster.webp}. A
 * paper whose reel files are present gets the reel in its header.
 *
 * The typeset PDFs and the figures come from docs/typeset-whitepapers.mjs.
 *
 * Usage: node docs/build-whitepapers.mjs [--sync-models]
 */
import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
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
const INDEX = join(HERE, 'whitepapers.html');
const MODELS = join(HERE, 'whitepapers-app', 'models.json');
const MODELS_SITE = 'https://digitalarsenal.github.io/orbit-accuracy-experiments/';
const MODELS_URL = `${MODELS_SITE}models/index.json`;
const GITHUB = 'https://github.com/DigitalArsenal/space-data-network/blob/main/whitepapers/';

/**
 * The papers, in index order. topic labels the card and header; summary is the
 * index card's blurb; earth is the header's Earth keyframe (earth.js:
 * dist lon lat ox oy sunAz sunEl); conjunction flies earth.js's close approach.
 */
const PAPERS = [
  {
    src: 'evidence-supported-aso-catalog.md',
    topic: 'Catalog',
    description: 'An attributed orbital catalog for Space Data Network: evidence, provenance, uncertainty and reproducible selection for anthropogenic space objects.',
    summary: 'An attributed orbital catalog for Space Data Network. Each solution records the evidence behind it, where it came from and how uncertain it is, and the rule that selected it. Includes the measured Vimpel refinement study, and HPOP measured against Orekit and against precise orbits, with every result reproducible in a browser.',
    earth: '1.9 -60 28 0.95 -0.55 62 18',
  },
  {
    src: 'fast-conjunction-assessment.md',
    topic: 'Conjunctions',
    description: 'Screening every catalog object against every other in minutes, for any propagator, with or without a GPU.',
    summary: 'How Space Data Network checks every tracked object against every other for close approaches: the full catalog over three days in about 20 seconds with SGP4, on a GPU or on ordinary processors, and with a numerical propagator in minutes. Includes agreement with SOCRATES, where collision probabilities are calibrated, and private screening on encrypted orbits.',
    earth: '1.9 40 24 0.95 -0.55 70 16',
    conjunction: true,
  },
  {
    src: 'adversarial-security.md',
    topic: 'Security',
    description: 'Cryptocurrency balances at deterministically derived addresses as continuous, publicly verifiable proof of key integrity.',
    summary: 'Cryptocurrency balances at addresses derived from a key act as continuous, public proof that the key is intact. An attacker who steals the key takes the funds, so funds that stay put mean a key that has not been stolen.',
    earth: '1.9 150 18 0.95 -0.55 128 10',
  },
];
const slugOf = (paper) => paper.src.replace(/\.md$/, '');

const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const unesc = (s) => s.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
const plain = (html) => unesc(html.replace(/<[^>]+>/g, '')).replace(/\s+/g, ' ').trim();

// ---------------------------------------------------------------------------
// Markdown rendering

// GitHub-style heading ids, so the papers' own contents and reference links
// resolve on the site exactly as they do on GitHub. Headings are collected for
// the contents rail, and a leading section number is set apart for styling.
let seen = new Map();
let headings = [];
function slug(text) {
  const base = text.toLowerCase().trim().replace(/<[^>]+>/g, '').replace(/[^\p{L}\p{N}\s_-]/gu, '').replace(/\s/g, '-');
  const n = seen.get(base) || 0;
  seen.set(base, n + 1);
  return n ? `${base}-${n}` : base;
}
const SECTION_NUMBER = /^((?:Appendix\s+)?[A-Z\d]+(?:\.\d+)*\.?)\s+(?=\S)/;
/** A heading's leading section number ("17.3", "1.", "Appendix A."), or null. */
function sectionNumber(text) {
  const m = SECTION_NUMBER.exec(text);
  return m && (/\d/.test(m[1]) || /^Appendix/.test(m[1])) ? { num: m[1].replace(/\.$/, ''), rest: text.slice(m[0].length) } : null;
}
marked.use({
  renderer: {
    heading({ tokens, depth, text }) {
      const id = slug(text);
      const inner = this.parser.parseInline(tokens);
      headings.push({ depth, id, text: plain(inner) });
      const num = depth <= 3 && sectionNumber(inner);
      const label = num ? `<span class="sec-num">${num.num}</span>${num.rest}` : inner;
      return `<h${depth} id="${id}">${label}</h${depth}>\n`;
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

// Tables scroll inside their own frame; a centered lone image is a figure,
// captioned with its alt text.
function dress(html) {
  return html
    .replace(/<table>/g, '<div class="table-frame"><table>')
    .replace(/<\/table>/g, '</table></div>')
    .replace(/<p align="center">\s*(<img\b[^>]*\balt="([^"]*)"[^>]*>)\s*<\/p>/g, '<figure class="paper-fig">$1<figcaption>$2</figcaption></figure>');
}

// ---------------------------------------------------------------------------
// Front matter: the blocks before the first "##" heading.

/** Split a paper into its front matter and its body. */
function split(md) {
  const at = md.search(/^## /m);
  if (at < 0) throw new Error('paper has no "##" section');
  return [md.slice(0, at), md.slice(at)];
}

/**
 * Read the front matter into fields. Markdown papers write one paragraph per
 * field; HTML papers write centered <p> elements (<strong> subtitle, <em>
 * tagline). Labelled lines (edition, cutoff, ©) are recognized by their words;
 * the rest are, in order, subtitle, authors, affiliations.
 */
function frontMatter(front) {
  const items = [];
  for (const block of front.split(/\n\s*\n/).map((b) => b.trim()).filter(Boolean)) {
    if (/^-{3,}$/.test(block)) continue;
    if (block.startsWith('<')) {
      for (const [, tag, inner] of block.matchAll(/<(h1|p)\b[^>]*>([\s\S]*?)<\/\1>/g)) {
        const img = /<img\b[^>]*\bsrc="([^"]+)"[^>]*\balt="([^"]*)"/.exec(inner);
        if (img) items.push({ kind: 'image', src: img[1], alt: img[2] });
        else if (tag === 'h1') items.push({ kind: 'title', text: plain(inner) });
        else items.push({ kind: /^<strong>/.test(inner.trim()) ? 'strong' : /^<em>/.test(inner.trim()) ? 'em' : 'text', text: plain(inner) });
      }
    } else if (block.startsWith('# ')) {
      items.push({ kind: 'title', text: plain(marked.parseInline(block.slice(2))) });
    } else {
      items.push({ kind: 'text', text: plain(marked.parseInline(block.replace(/\s*\n\s*/g, ' '))) });
    }
  }
  const fm = { title: '', subtitle: '', tagline: '', authors: [], affiliations: '', edition: null, cutoff: [], copyright: '', image: null };
  const rest = [];
  for (const item of items) {
    const { kind, text } = item;
    if (kind === 'title') fm.title = text;
    else if (kind === 'image') fm.image = item;
    else if (/^(numerical )?evidence cutoff:/i.test(text)) fm.cutoff = text.replace(/^[^:]*:\s*/, '').split(/;\s*/);
    else if (/^©/.test(text)) fm.copyright = text;
    else if (/^(technical )?whitepaper\b|^revised\b|^edition\b/i.test(text)) fm.edition = edition(text);
    else if (kind === 'strong') fm.subtitle = text;
    else if (kind === 'em') fm.tagline = text;
    else rest.push(text);
  }
  if (!fm.subtitle) fm.subtitle = rest.shift() || '';
  fm.authors = (rest.shift() || '').split(/,\s*(?:and\s+)?|\s+and\s+/).filter(Boolean);
  fm.affiliations = rest.shift() || '';
  if (!fm.title || !fm.authors.length || !fm.edition) throw new Error(`front matter needs a title, authors and an edition line (got ${JSON.stringify(fm)})`);
  if (rest.length) throw new Error(`unrecognized front matter: ${JSON.stringify(rest)}`);
  return fm;
}

/** "Technical whitepaper 1.9 | 9 October 2026 (note)" or "Revised 25 September 2026". */
function edition(text) {
  const [head, tail = ''] = text.split(/\s*\|\s*/);
  const version = /\d+(?:\.\d+)+/.exec(head)?.[0] || '';
  const dated = tail || head;
  const date = /\d{1,2} [A-Z][a-z]+ \d{4}/.exec(dated)?.[0] || '';
  const note = /\(([^)]*)\)\s*$/.exec(dated)?.[1] || '';
  const kind = tail ? head.replace(version, '').trim() : 'Whitepaper';
  return { kind, version, date, dateLabel: /^revised\b/i.test(head) ? 'Revised' : 'Published', note };
}

/** Each author with their affiliation: "Koury: X. Jah: Y." names them; otherwise one line for all. */
function byline(fm) {
  const keyed = new Map();
  for (const [, key, aff] of fm.affiliations.matchAll(/(?:^|\.\s+)([A-Z][\p{L}'-]+):\s+(.+?)(?=\.\s+[A-Z][\p{L}'-]+:\s|\.?$)/gu)) keyed.set(key, aff);
  return fm.authors.map((name) => {
    const key = [...keyed.keys()].find((k) => name.split(/\s+/).includes(k));
    const aff = keyed.size ? keyed.get(key) || '' : fm.affiliations;
    return { name, affiliation: aff.replace(/\.$/, '') };
  });
}

/** An affiliation line, its e-mail addresses as links. */
const affiliationHtml = (aff) => aff.split(/\s+·\s+/).map((part) => /^[^\s@]+@[^\s@]+$/.test(part)
  ? `<a href="mailto:${esc(part)}">${esc(part)}</a>` : esc(part)).join('<span class="dot" aria-hidden="true">·</span>');

const metaLine = (fm) => [
  fm.authors.join(' and '),
  fm.edition.version ? `Whitepaper ${fm.edition.version}` : '',
  `${fm.edition.dateLabel === 'Revised' ? 'Revised ' : ''}${fm.edition.date}`,
].filter(Boolean).join(' · ');

// ---------------------------------------------------------------------------
// Models and reels

async function syncModels() {
  const res = await fetch(MODELS_URL);
  if (!res.ok) throw new Error(`${MODELS_URL}: HTTP ${res.status}`);
  const list = await res.json();
  if (!Array.isArray(list)) throw new Error(`${MODELS_URL}: not an array`);
  writeFileSync(MODELS, `${JSON.stringify(list, null, 2)}\n`);
  console.log('synced', relative(REPO, MODELS), `(${list.length} models)`);
}

/** The models for one paper, keyed by section id; an entry naming a missing section is reported and skipped. */
function modelsFor(paper, ids) {
  const list = existsSync(MODELS) ? JSON.parse(readFileSync(MODELS, 'utf8')) : [];
  const out = new Map();
  for (const m of list) {
    if (!m || typeof m !== 'object' || String(m.paper || '').replace(/\.(md|html)$/, '') !== slugOf(paper)) continue;
    if (!m.section_id || !m.url || !m.title) { console.warn(`models: incomplete entry skipped: ${JSON.stringify(m)}`); continue; }
    if (!ids.has(m.section_id)) { console.warn(`models: ${slugOf(paper)} has no section "${m.section_id}"; skipped`); continue; }
    if (!out.has(m.section_id)) out.set(m.section_id, { title: String(m.title), url: new URL(m.url, MODELS_SITE).href });
  }
  return out;
}

const PLAY = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5.5v13l11-6.5z" fill="currentColor"/></svg>';
const runIt = (m) => `<button class="run-it" type="button" data-model-url="${esc(m.url)}" data-model-title="${esc(m.title)}"><span class="run-icon">${PLAY}</span><span class="run-text"><b>Run it</b><span>${esc(m.title)}</span></span><span class="run-go" aria-hidden="true">&rarr;</span></button>\n`;

/** Put each model's control under its section heading. */
function attachModels(html, models) {
  for (const [id, m] of models) html = html.replace(new RegExp(`(<h([23]) id="${id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}">[\\s\\S]*?</h\\2>\\n)`), `$1${runIt(m)}`);
  return html;
}

const reelDir = (paper) => join(HERE, 'media', 'papers', slugOf(paper));
const hasReel = (paper) => ['reel.webm', 'reel.mp4', 'poster.webp'].every((f) => existsSync(join(reelDir(paper), f)));

// ---------------------------------------------------------------------------
// Pages

const ICON = {
  pdf: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 4v11M7 10l5 5 5-5M5 20h14"/></svg>',
  md: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="6" width="18" height="12" rx="2"/><path d="M7 15V9l2.5 3L12 9v6M16 9v6M14 13l2 2 2-2"/></svg>',
  back: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M15 6l-6 6 6 6"/></svg>',
  list: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M9 6h11M9 12h11M9 18h11M4.5 6h.01M4.5 12h.01M4.5 18h.01"/></svg>',
  close: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>',
  open: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/></svg>',
};

/** The contents rail: sections, with their subsections (references excepted). */
function toc(models) {
  const items = [];
  for (const h of headings) {
    if (h.depth === 2) items.push({ ...h, children: [] });
    else if (h.depth === 3 && items.length && !/^references$/i.test(items.at(-1).text)) items.at(-1).children.push(h);
  }
  const link = (h) => {
    const num = sectionNumber(h.text);
    const label = num ? `<span class="toc-num">${esc(num.num)}</span><span>${esc(num.rest)}</span>` : `<span>${esc(h.text)}</span>`;
    return `<a href="#${h.id}">${label}${models.has(h.id) ? '<i class="toc-model" title="Interactive model"></i>' : ''}</a>`;
  };
  return items.map((h) => `          <li>${link(h)}${h.children.length ? `\n            <ol>\n${h.children.map((c) => `              <li>${link(c)}</li>`).join('\n')}\n            </ol>\n          ` : ''}</li>`).join('\n');
}

function header(base, current) {
  const u = (p) => base + p;
  const nav = [
    ['onboarding.html', 'Get started'],
    ['catalog.html', 'Catalog'],
    ['collision-avoidance.html', 'CA'],
    ['whitepapers.html', 'Whitepapers'],
    ['index.html#download', 'Download'],
  ];
  const links = nav.map(([h, t]) => `        <a href="${u(h)}"${h === current ? ' aria-current="page"' : ''}>${t}</a>`).join('\n');
  return `  <header class="sdn-header">
    <div class="sdn-header-inner">
      <a class="sdn-header-brand" href="${u('index.html')}"><span>Space Data Network</span></a>
      <nav class="sdn-header-links" aria-label="Site">
${links}
        <a href="#stack">Stack</a>
        <a href="https://github.com/DigitalArsenal/space-data-network" target="_blank" rel="noopener">GitHub</a>
      </nav>
      <div class="sdn-header-actions">
        <button data-sdn-theme-toggle aria-label="Switch between light and dark theme"></button>
        <button data-sdn-menu-toggle aria-label="Menu"></button>
      </div>
    </div>
  </header>`;
}

function footer(base) {
  const u = (p) => base + p;
  return `  <footer class="sdn-footer">
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
  </footer>`;
}

const FAVICON = `<link rel="icon" href="data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32' fill='none'><defs><linearGradient id='t' gradientUnits='userSpaceOnUse' x1='16' y1='29' x2='16' y2='3'><stop offset='0' stop-color='%23f5a524' stop-opacity='0'/><stop offset='.55' stop-color='%23f5a524' stop-opacity='.55'/><stop offset='1' stop-color='%23f5a524'/></linearGradient></defs><circle cx='16' cy='16' r='13' stroke='%23f5f5f7' stroke-width='1.6' opacity='.3'/><path d='M16 29A13 13 0 0 1 16 3' stroke='url(%23t)' stroke-width='2.2' stroke-linecap='round'/><path d='M16 10.2L10.4 20.1H21.6Z' stroke='%23f5f5f7' stroke-width='1.6' stroke-linejoin='round'/><circle cx='16' cy='10.2' r='2.6' fill='%23f5f5f7'/><circle cx='10.4' cy='20.1' r='2.6' fill='%23f5f5f7'/><circle cx='21.6' cy='20.1' r='2.6' fill='%23f5f5f7'/><circle cx='16' cy='3' r='2.5' fill='%23f5a524'/></svg>">`;

function page(paper, fm, body, models, words) {
  const base = '../';
  const u = (p) => base + p;
  const name = slugOf(paper);
  const reel = hasReel(paper);
  const ed = fm.edition;
  const sections = headings.filter((h) => h.depth === 2 && /^\d/.test(h.text)).length;
  const facts = [
    ed.version && ['Edition', esc(ed.version)],
    [ed.dateLabel, esc(ed.date)],
    fm.cutoff.length && ['Evidence cutoff', fm.cutoff.map((c) => `<span>${esc(c)}</span>`).join('')],
    sections && ['Sections', String(sections)],
    ['Reading time', `${Math.max(1, Math.round(words / 230))} min`],
    models.size && ['Interactive models', String(models.size)],
  ].filter(Boolean);
  const authors = byline(fm).map((a) => `          <li><b>${esc(a.name)}</b>${a.affiliation ? `<span>${affiliationHtml(a.affiliation)}</span>` : ''}</li>`).join('\n');
  const reelHtml = reel ? `
        <figure class="reel-frame paper-reel">
          <div class="reel-stage">
            <video muted playsinline preload="auto" data-poster="${u(`media/papers/${name}/poster.webp`)}" aria-label="${esc(fm.title)}: introduction">
              <source src="${u(`media/papers/${name}/reel.webm`)}" type='video/webm; codecs="av01.0.08M.08"'>
              <source src="${u(`media/papers/${name}/reel.mp4`)}" type="video/mp4">
            </video>
          </div>
        </figure>` : '';
  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <script>window.coi = { coepCredentialless: () => true, quiet: true };</script>
  <script src="coi-serviceworker.js"></script>
  <link rel="stylesheet" href="${u('assets/sdn-chrome/sdn-chrome.css')}">
  <script src="${u('assets/sdn-chrome/sdn-chrome.js')}"></script>
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta name="description" content="${esc(paper.description)}">
  <meta name="theme-color" content="#000000">
  <title>${esc(fm.title)} &middot; Space Data Network</title>
  ${FAVICON}
  <link rel="stylesheet" href="${u('site.css')}">
  <link rel="stylesheet" href="${u('whitepapers-app/paper.css')}">${reel ? `\n  <link rel="stylesheet" href="${u('media/reel/reel.css')}">` : ''}${hasMath ? `\n  <link rel="stylesheet" href="katex/katex.min.css">` : ''}
  <style>
    :root { --sdn-stack-footer-height: 0px; --sdn-stack-header-height: 52px; }
  </style>
</head>
<body class="paper-page">
${header(base, 'whitepapers.html')}
  <canvas id="earth" aria-hidden="true"${paper.conjunction ? ' data-conjunction' : ''}></canvas>
  <div class="paper-progress" aria-hidden="true"><span></span></div>
  <main>
    <header class="paper-hero" data-earth="${paper.earth}">
      <div class="wrap paper-hero-grid${reel ? ' has-reel' : ''}">
        <div class="paper-hero-text">
          <p class="paper-crumbs"><a href="${u('whitepapers.html')}">${ICON.back}All whitepapers</a></p>
          <p class="eyebrow">${esc(paper.topic)} &middot; ${esc(ed.kind)}</p>
          <h1 class="paper-title">${esc(fm.title)}</h1>
          <p class="paper-subtitle">${esc(fm.subtitle)}</p>${fm.tagline ? `\n          <p class="paper-tagline">${esc(fm.tagline)}</p>` : ''}
          <ul class="paper-authors">
${authors}
          </ul>
          <div class="actions paper-actions">
            <a class="btn btn-primary" href="${name}.pdf">${ICON.pdf}Download PDF</a>
            <a class="btn" href="${GITHUB}${paper.src}">${ICON.md}Markdown source</a>
          </div>
        </div>${reelHtml}
      </div>
      <div class="wrap">
        <dl class="paper-facts">
${facts.map(([k, v]) => `          <div><dt>${k}</dt><dd>${v}</dd></div>`).join('\n')}
        </dl>${ed.note || fm.copyright ? `\n        <p class="paper-note">${[ed.note && esc(ed.note[0].toUpperCase() + ed.note.slice(1)), fm.copyright && esc(fm.copyright)].filter(Boolean).join(' &middot; ')}</p>` : ''}
      </div>
    </header>
    <div class="paper-body">
      <div class="paper-layout">
        <nav class="paper-toc" id="contents" aria-label="Contents">
          <div class="toc-head">
            <p>Contents</p>
            <button class="toc-close" type="button" aria-label="Close contents">${ICON.close}</button>
          </div>
          <ol>
${toc(models)}
          </ol>
          <div class="toc-files">
            <a href="${name}.pdf">${ICON.pdf}PDF</a>
            <a href="${GITHUB}${paper.src}">${ICON.md}Markdown</a>
          </div>
        </nav>
        <article class="paper">
${body}
        </article>
      </div>
    </div>
    <button class="toc-fab" type="button" aria-controls="contents" aria-expanded="false">${ICON.list}<span class="toc-fab-label">Contents</span><svg class="toc-ring" viewBox="0 0 36 36" aria-hidden="true"><circle cx="18" cy="18" r="15"/><circle class="toc-ring-fill" cx="18" cy="18" r="15" pathLength="100"/></svg></button>
    <div class="paper-scrim" hidden></div>
    <aside class="paper-panel" aria-labelledby="panel-title" hidden>
      <div class="panel-head">
        <div>
          <p class="eyebrow">Interactive model</p>
          <h2 id="panel-title"></h2>
        </div>
        <a class="panel-btn panel-open" href="#" target="_blank" rel="noopener" aria-label="Open in a new tab">${ICON.open}</a>
        <button class="panel-btn panel-close" type="button" aria-label="Close">${ICON.close}</button>
      </div>
      <div class="panel-body"><iframe title="Interactive model" allow="cross-origin-isolated; fullscreen; clipboard-write" loading="lazy"></iframe></div>
    </aside>
    <section id="stack" data-sdn-stack="sdn"></section>
  </main>
${footer(base)}
  <script src="${u('site.js')}"></script>${reel ? `\n  <script src="${u('media/reel/reel.js')}" defer></script>` : ''}
  <script src="${u('earth.js')}"></script>
  <script src="${u('whitepapers-app/paper.js')}"></script>
</body>
</html>
`;
}

// Satellites on the cover art's orbits (points on the drawn ellipses).
const COVER_SATS = {
  Catalog: '<circle class="cover-sat" cx="332" cy="151" r="4"/>',
  Conjunctions: '<circle class="cover-sat" cx="282" cy="163" r="4"/><circle class="cover-sat alt" cx="296" cy="154" r="4"/>',
  Security: '<circle class="cover-sat" cx="383" cy="141" r="4"/>',
};

/** The index cards, regenerated between the markers on whitepapers.html. */
function card(paper, fm, models) {
  const name = slugOf(paper);
  const reel = hasReel(paper);
  const cover = reel
    ? `<img src="media/papers/${name}/poster.webp" alt="" loading="lazy">`
    : `<svg viewBox="0 0 400 180" aria-hidden="true"><circle class="cover-planet" cx="330" cy="250" r="190"/><ellipse class="cover-orbit" cx="330" cy="250" rx="300" ry="96" transform="rotate(-14 330 250)"/><ellipse class="cover-orbit faint" cx="330" cy="250" rx="250" ry="150" transform="rotate(22 330 250)"/>${COVER_SATS[paper.topic] || ''}</svg>`;
  return `          <article class="card paper-card">
            <a class="paper-cover cover-${esc(paper.topic.toLowerCase())}" href="whitepapers/${name}.html" tabindex="-1" aria-hidden="true">${cover}</a>
            <div class="paper-card-body">
              <p class="eyebrow">${esc(paper.topic)}${fm.edition.version ? ` &middot; Edition ${esc(fm.edition.version)}` : ''}</p>
              <h3><a href="whitepapers/${name}.html">${esc(fm.title)}</a></h3>
              <p>${esc(paper.summary)}</p>
              <p class="meta">${esc(metaLine(fm))}${models.size ? ` &middot; ${models.size} interactive model${models.size === 1 ? '' : 's'}` : ''}</p>
              <div class="formats">
                <a href="whitepapers/${name}.html">Read</a>
                <a href="whitepapers/${name}.pdf">PDF</a>
                <a href="${GITHUB}${paper.src}">Markdown</a>
              </div>
            </div>
          </article>`;
}

// ---------------------------------------------------------------------------

if (process.argv.includes('--sync-models')) await syncModels();

mkdirSync(OUT, { recursive: true });
cpSync(join(SRC, 'assets'), join(OUT, 'assets'), { recursive: true });
// KaTeX's stylesheet and the woff2 faces it names first (every current browser
// takes woff2, so the woff/ttf fallbacks are not shipped).
mkdirSync(join(OUT, 'katex', 'fonts'), { recursive: true });
cpSync(join(KATEX, 'dist', 'katex.min.css'), join(OUT, 'katex', 'katex.min.css'));
for (const font of readdirSync(join(KATEX, 'dist', 'fonts')).filter((f) => f.endsWith('.woff2'))) {
  cpSync(join(KATEX, 'dist', 'fonts', font), join(OUT, 'katex', 'fonts', font));
}
const cards = [];
for (const paper of PAPERS) {
  const out = join(OUT, paper.src.replace(/\.md$/, '.html'));
  const [front, rest] = split(readFileSync(join(SRC, paper.src), 'utf8'));
  const fm = frontMatter(front);
  seen = new Map();
  headings = [];
  hasMath = false;
  let body = dress(marked.parse(rest, { gfm: true }));
  const models = modelsFor(paper, new Set(headings.map((h) => h.id)));
  body = attachModels(body, models);
  const words = rest.replace(/\$\$[\s\S]*?\$\$|<\/?[a-zA-Z][^>\n]*>|[#*_`|>-]/g, ' ').split(/\s+/).filter(Boolean).length;
  writeFileSync(out, page(paper, fm, body, models, words));
  cards.push(card(paper, fm, models));
  console.log('wrote', relative(REPO, out), hasReel(paper) ? '' : '(no reel yet)');
}
const index = readFileSync(INDEX, 'utf8');
const START = '<!-- PAPERS_START -->';
const END = '<!-- PAPERS_END -->';
const a = index.indexOf(START);
const b = index.indexOf(END);
if (a < 0 || b < a) throw new Error(`whitepapers.html needs ${START} … ${END}`);
writeFileSync(INDEX, `${index.slice(0, a + START.length)}\n${cards.join('\n')}\n          ${index.slice(b)}`);
console.log('wrote', relative(REPO, INDEX));
