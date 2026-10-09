#!/usr/bin/env node
/**
 * Typeset the whitepapers.
 *
 * The figures are TikZ sources in whitepapers/figures/. Each compiles to a PDF
 * for the typeset papers and to whitepapers/assets/<name>.svg, the file the
 * markdown (and so GitHub and the reader pages) shows. Each paper under
 * whitepapers/ is then typeset from its markdown, through pandoc and
 * whitepapers/latex/paper.lua, to docs/whitepapers/<name>.pdf. The markdown
 * stays the only copy of the text.
 *
 * Needs tectonic, pandoc and pdftocairo (poppler) on PATH.
 * Usage: node docs/typeset-whitepapers.mjs, then node docs/build-whitepapers.mjs
 */
import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdtempSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..');
const SRC = join(REPO, 'whitepapers');
const FIGURES = join(SRC, 'figures');
const LATEX = join(SRC, 'latex');
const OUT = join(HERE, 'whitepapers');

const run = (cmd, args, cwd) => execFileSync(cmd, args, { cwd, stdio: ['ignore', 'ignore', 'inherit'] });
const work = mkdtempSync(join(tmpdir(), 'sdn-typeset-'));
try {
  for (const file of readdirSync(FIGURES).filter((f) => f.endsWith('.tex') && f !== 'style.tex')) {
    const name = file.replace(/\.tex$/, '');
    run('tectonic', ['-X', 'compile', '--outdir', work, join(FIGURES, file)], FIGURES);
    run('pdftocairo', ['-svg', join(work, `${name}.pdf`), join(SRC, 'assets', `${name}.svg`)], work);
    console.log('wrote', relative(REPO, join(SRC, 'assets', `${name}.svg`)));
  }
  for (const file of readdirSync(SRC).filter((f) => f.endsWith('.md') && f !== 'README.md')) {
    const name = file.replace(/\.md$/, '');
    const tex = join(work, `${name}.tex`);
    run('pandoc', [join(SRC, file), '-f', 'gfm+tex_math_dollars+smart', '-t', 'latex', '-s',
      '--lua-filter', join(LATEX, 'paper.lua'), '-H', join(LATEX, 'header.tex'),
      '-V', 'documentclass=article', '-V', 'fontsize=10pt', '-V', 'geometry=margin=1in', '-V', 'papersize=letter',
      '-o', tex], SRC);
    run('tectonic', ['-X', 'compile', tex], work);
    copyFileSync(join(work, `${name}.pdf`), join(OUT, `${name}.pdf`));
    console.log('wrote', relative(REPO, join(OUT, `${name}.pdf`)));
  }
} finally {
  rmSync(work, { recursive: true, force: true });
}
