#!/usr/bin/env node
/**
 * BUILD THE DOCUMENTATION SITE PAGES AND THE DOWNLOADABLE PDF.
 *
 * The three guides are authored as markdown because that is what stays
 * reviewable in a pull request. This renders them into the pages the public
 * site serves, and into one combined PDF for people who want the whole thing
 * offline or in print.
 *
 * NO NEW RUNTIME DEPENDENCY, on purpose:
 *  · markdown  -> HTML   `marked`, already vendored under sdn-js
 *  · HTML      -> PDF    headless Chrome's own --print-to-pdf
 * A PDF toolchain (pandoc, LaTeX, a headless-browser wrapper) would be a large
 * install for one artifact, and every machine that can view this site already
 * has a browser that renders it identically.
 *
 * ZERO EXTERNAL ORIGIN. Fonts are the system stack, styles are inline, and the
 * pages link only to each other. Nothing here fetches a byte from a third
 * party, in the browser or during the build — the same rule the node UI keeps.
 *
 * Usage:
 *   node docs/build-docs.mjs           # pages + PDF
 *   node docs/build-docs.mjs --no-pdf  # pages only (skips Chrome)
 */
import { readFileSync, writeFileSync, existsSync, mkdirSync, rmSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createRequire } from 'node:module';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..');

// `marked` lives in the sdn-js workspace; resolve from there rather than
// duplicating the dependency at the repo root.
const require = createRequire(join(REPO, 'sdn-js', 'package.json'));
const { marked } = require('marked');

/** The guides, in reading order. Order is the PDF's chapter order too. */
const DOCS = [
  { slug: 'server-overview', src: 'SERVER-OVERVIEW.md', title: 'Server Overview' },
  { slug: 'agent-api-guide', src: 'AGENT-API-GUIDE.md', title: 'API Guide' },
  { slug: 'operator-enrollment', src: 'OPERATOR-ENROLLMENT.md', title: 'Operator Enrollment' },
];

const SITE = 'Space Data Network';

/*
 * The dashboard's design system, as plain CSS.
 *
 * Tokens are copied from the console's Tailwind theme
 * (sdn-js/spaceaware-ui/src/dashboard-tailadmin/styles/tailwind.css) so the
 * docs and the console are visibly one product: same gray ramp, same monochrome
 * brand, same shadows, same Outfit typeface. Copied rather than imported
 * because these pages are static files on the site with no build step of their
 * own — and a Tailwind pipeline for four documents would cost more than it
 * returns. If the console's ramp changes, change it here too.
 *
 * Outfit is loaded from the site's own vendored font if present and otherwise
 * falls back to the system stack; no external origin is contacted either way.
 *
 * Screen and print share the stylesheet on purpose — printing a page and
 * downloading the PDF should give the same document.
 */
const CSS = `
:root {
  color-scheme: light;
  --white: #ffffff;
  --gray-50: #f9fafb;
  --gray-100: #f2f4f7;
  --gray-200: #e4e7ec;
  --gray-300: #d0d5dd;
  --gray-400: #98a2b3;
  --gray-500: #667085;
  --gray-600: #475467;
  --gray-700: #344054;
  --gray-800: #1d2939;
  --gray-900: #101828;
  --shadow-sm: 0px 1px 3px 0px rgba(16, 24, 40, 0.1);
  --radius: 12px;
}
* { box-sizing: border-box; }
body {
  margin: 0; padding: 2.5rem 1.5rem 6rem;
  font: 16px/1.65 Outfit, ui-sans-serif, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  color: var(--gray-800); background: var(--gray-50);
}
main {
  max-width: 48rem; margin: 0 auto; background: var(--white);
  border: 1px solid var(--gray-200); border-radius: var(--radius);
  box-shadow: var(--shadow-sm); padding: 2.5rem 2.75rem 3rem;
}
h1, h2, h3, h4 { line-height: 1.25; font-weight: 650; color: var(--gray-900); margin: 2.5rem 0 .75rem; }
h1 { font-size: 1.9rem; margin-top: 0; letter-spacing: -.02em; }
h2 { font-size: 1.35rem; padding-bottom: .35rem; border-bottom: 1px solid var(--gray-200); }
h3 { font-size: 1.05rem; }
p, ul, ol, blockquote, table { margin: 0 0 1rem; }
li { margin: .25rem 0; }
a { color: var(--gray-900); text-underline-offset: 2px; }
code {
  font: .875em/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  background: var(--gray-100); padding: .12em .35em; border-radius: 6px;
}
pre {
  background: var(--gray-900); color: var(--gray-50); padding: 1rem 1.15rem;
  border-radius: 10px; overflow-x: auto; margin: 0 0 1.25rem;
  font-size: .84rem; line-height: 1.55;
}
pre code { background: none; padding: 0; color: inherit; font-size: inherit; }
blockquote {
  margin-left: 0; padding: .6rem 1rem; border-left: 3px solid var(--gray-300);
  background: var(--gray-50); color: var(--gray-600); border-radius: 0 8px 8px 0;
}
table { width: 100%; border-collapse: collapse; font-size: .92rem; }
th, td { text-align: left; padding: .5rem .65rem; border-bottom: 1px solid var(--gray-200); vertical-align: top; }
th { font-weight: 620; background: var(--gray-50); color: var(--gray-500);
     text-transform: uppercase; font-size: .78rem; letter-spacing: .04em; }
hr { border: 0; border-top: 1px solid var(--gray-200); margin: 2.5rem 0; }
.nav {
  max-width: 48rem; margin: 0 auto 1.25rem; display: flex; flex-wrap: wrap;
  gap: .5rem; align-items: center; font-size: .875rem;
}
/* Pill nav, the console's rail vocabulary: ink-on-paper for the active item. */
.nav a {
  color: var(--gray-600); text-decoration: none; background: var(--white);
  border: 1px solid var(--gray-200); border-radius: 999px; padding: .35rem .85rem;
  transition: background .15s, color .15s;
}
.nav a:hover { background: var(--gray-100); color: var(--gray-900); }
.nav a[aria-current="page"] {
  background: var(--gray-900); border-color: var(--gray-900); color: var(--white);
}
.nav .grow { margin-left: auto; }
.foot { max-width: 48rem; margin: 1.5rem auto 0; color: var(--gray-500); font-size: .85rem; }

@media print {
  /* The site chrome is navigation, and navigation does not survive paper. */
  body { padding: 0; font-size: 11pt; }
  .nav, .foot, .no-print { display: none !important; }
  main { max-width: none; }
  pre { background: #f2f4f7; color: #101828; border: 1px solid #e4e7ec;
        white-space: pre-wrap; word-wrap: break-word; }
  h1, h2, h3 { break-after: avoid; }
  pre, table, blockquote { break-inside: avoid; }
  .chapter { break-before: page; }
  .chapter:first-of-type { break-before: auto; }
}
@page { size: A4; margin: 18mm 16mm; }
`;

/** Rewrite in-repo markdown links to their rendered page names. */
function rewriteLinks(html) {
  let out = html;
  for (const d of DOCS) {
    out = out
      .replaceAll(`href="./${d.src}"`, `href="${d.slug}.html"`)
      .replaceAll(`href="${d.src}"`, `href="${d.slug}.html"`);
  }
  return out;
}

function navFor(active) {
  const links = DOCS.map(
    (d) =>
      `<a href="${d.slug}.html"${d.slug === active ? ' aria-current="page"' : ''}>${d.title}</a>`,
  ).join('\n      ');
  // No wordmark: these pages are served inside the site that already carries
  // the brand, and repeating it above every document is chrome, not navigation.
  return `  <nav class="nav">
      ${links}
      <span class="grow"></span>
      <a href="space-data-network-docs.pdf">PDF</a>
    </nav>`;
}

function page({ title, bodyHtml, active }) {
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${title} — ${SITE}</title>
<meta name="description" content="${title} for the ${SITE}.">
<style>${CSS}</style>
</head>
<body>
${navFor(active)}
<main>
${bodyHtml}
</main>
<div class="foot">${SITE} documentation. <a href="space-data-network-docs.pdf">Download as PDF</a>.</div>
</body>
</html>
`;
}

function render(mdPath) {
  return rewriteLinks(marked.parse(readFileSync(mdPath, 'utf8')));
}

/*
 * The node SHIPS its own documentation (owner 2026-08-28): the rendered pages
 * and the PDF are copied into the Go embed tree, go:embed'ed into the binary,
 * and served same-origin at /docs/ — so the instructions a node shows are the
 * instructions for the version it runs, updated with every release.
 */
const EMBED_DOCS_DIR = join(REPO, 'sdn-server', 'cmd', 'spacedatanetwork', 'embedded', 'docs');

function emitEmbedded(pdfBuilt) {
  mkdirSync(EMBED_DOCS_DIR, { recursive: true });
  for (const d of DOCS) {
    writeFileSync(join(EMBED_DOCS_DIR, `${d.slug}.html`), readFileSync(join(HERE, `${d.slug}.html`)));
  }
  if (pdfBuilt) {
    writeFileSync(
      join(EMBED_DOCS_DIR, 'space-data-network-docs.pdf'),
      readFileSync(join(HERE, 'space-data-network-docs.pdf')),
    );
  }
  console.log(`build-docs: embedded ${DOCS.length} page(s)${pdfBuilt ? ' + PDF' : ''} -> ${EMBED_DOCS_DIR}`);
}

function main() {
  const noPdf = process.argv.includes('--no-pdf');
  const missing = DOCS.filter((d) => !existsSync(join(HERE, d.src)));
  if (missing.length) {
    console.error(`build-docs: missing source: ${missing.map((m) => m.src).join(', ')}`);
    process.exit(1);
  }

  // ── per-guide pages ──────────────────────────────────────────────────────
  const chapters = [];
  for (const d of DOCS) {
    const bodyHtml = render(join(HERE, d.src));
    writeFileSync(join(HERE, `${d.slug}.html`), page({ title: d.title, bodyHtml, active: d.slug }));
    chapters.push(`<section class="chapter">\n${bodyHtml}\n</section>`);
    console.log(`build-docs: wrote ${d.slug}.html`);
  }

  if (noPdf) { emitEmbedded(false); return; }

  // ── one combined document, then Chrome prints it ─────────────────────────
  // The print source is a temp file rather than a shipped page: a single
  // 1,400-line scroll is a bad web page and a good PDF, and shipping it would
  // just be a fourth thing to keep in sync.
  const tmpDir = join(HERE, '.pdf-build');
  mkdirSync(tmpDir, { recursive: true });
  const printSrc = join(tmpDir, 'print.html');
  writeFileSync(
    printSrc,
    page({ title: 'Documentation', bodyHtml: chapters.join('\n<hr>\n'), active: '' }),
  );

  const chrome =
    process.env.CHROME_BIN ||
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  if (!existsSync(chrome)) {
    console.error(
      `build-docs: no Chrome at ${chrome}. Set CHROME_BIN, or run with --no-pdf.`,
    );
    process.exit(1);
  }

  const out = join(HERE, 'space-data-network-docs.pdf');
  try {
    execFileSync(
      chrome,
      [
        '--headless',
        '--disable-gpu',
        '--no-sandbox',
        // Chrome resolves relative asset URLs against the file, and there are
        // none — every style is inline, so this renders with no network at all.
        `--print-to-pdf=${out}`,
        '--no-pdf-header-footer',
        pathToFileURL(printSrc).href,
      ],
      { stdio: 'pipe' },
    );
  } catch (e) {
    console.error(`build-docs: Chrome failed to render the PDF: ${e.message}`);
    process.exit(1);
  } finally {
    rmSync(tmpDir, { recursive: true, force: true });
  }

  if (!existsSync(out)) {
    console.error('build-docs: Chrome reported success but wrote no PDF.');
    process.exit(1);
  }
  const kb = Math.round(readFileSync(out).length / 1024);
  console.log(`build-docs: wrote space-data-network-docs.pdf (${kb} KB)`);
  emitEmbedded(true);
}

main();
