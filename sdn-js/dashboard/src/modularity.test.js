/*
 * THE PARALLELISM LAW — sdn-dashboard-modularize-for-parallelism.
 *
 * The task's acceptance property is "two agents building two different widgets
 * must touch ZERO common files". That was demonstrated once, by intersecting two
 * feature branches. This suite is what stops it decaying: it asserts the two
 * structural facts the property rests on.
 *
 *   1. A widget/route is a DIRECTORY that declares itself. Metadata and
 *      component are discovered, so adding one edits no existing file.
 *   2. The SHELLS never name an individual widget or route. The moment
 *      NodeConsole.svelte contains `w.id === 'health'` again, or App.svelte
 *      contains `route === 'peers'`, the shared lane is back and every widget
 *      task queues behind every other one.
 *
 * Checked on the filesystem rather than by importing the route registry: a
 * route's `boot` can pull in Vite-only imports (PEERS' search engine imports a
 * `?worker&inline` module), and this suite runs in vitest's `node` environment.
 */
import { describe, expect, it } from 'vitest';
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { WIDGETS, DEFAULT_LAYOUT, PUBLIC_LAYOUT } from './node-layout.js';

const dir = (rel) => fileURLToPath(new URL(rel, import.meta.url));
const read = (rel) => readFileSync(dir(rel), 'utf8');
/**
 * The shell files are heavily commented, and their comments legitimately QUOTE
 * the structure that was removed ("the eight-branch `{#if w.id === 'health'}`
 * chain"). A law about what the code does must be read against the code — so
 * `/* *\/` blocks and `<!-- -->` markup comments come out before matching.
 */
const code = (rel) =>
  read(rel)
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/<!--[\s\S]*?-->/g, '');
const dirsIn = (rel) =>
  readdirSync(dir(rel), { withFileTypes: true })
    .filter((e) => e.isDirectory())
    .map((e) => e.name)
    .sort();

describe('a widget is a directory', () => {
  const ids = dirsIn('./widgets/');

  it('declares its metadata and ships its component, for every one', () => {
    expect(ids.length).toBeGreaterThan(0);
    for (const id of ids) {
      expect(existsSync(dir(`./widgets/${id}/widget.js`)), `widgets/${id}/widget.js`).toBe(true);
      expect(existsSync(dir(`./widgets/${id}/Widget.svelte`)), `widgets/${id}/Widget.svelte`).toBe(true);
    }
  });

  it('is discovered — the registry holds exactly the directories on disk', () => {
    expect(Object.keys(WIDGETS).sort()).toEqual(ids);
  });

  it('names itself, so a stored layout id and a directory cannot drift apart', () => {
    for (const id of ids) {
      expect(read(`./widgets/${id}/widget.js`)).toMatch(
        new RegExp(`export const id\\s*=\\s*['"]${id}['"]`)
      );
    }
  });
});

describe('a route is a directory', () => {
  const ids = dirsIn('./routes/');

  it('declares its metadata and ships its component, for every one', () => {
    expect(ids.length).toBeGreaterThan(0);
    for (const id of ids) {
      expect(existsSync(dir(`./routes/${id}/route.js`)), `routes/${id}/route.js`).toBe(true);
      expect(existsSync(dir(`./routes/${id}/Route.svelte`)), `routes/${id}/Route.svelte`).toBe(true);
    }
  });

  it('names itself', () => {
    for (const id of ids) {
      expect(read(`./routes/${id}/route.js`)).toMatch(
        new RegExp(`export const id\\s*=\\s*['"]${id}['"]`)
      );
    }
  });
});

describe('the shells stay blind to what they render', () => {
  it('NodeConsole.svelte names no individual widget', () => {
    const shell = code('./NodeConsole.svelte');
    // The eight-branch `{#if w.id === 'health'} … {:else if …}` chain was the
    // whole bottleneck. Any comparison against a widget id is that chain coming
    // back, whatever shape it wears.
    expect(shell).not.toMatch(/w\.id\s*===/);
    for (const id of Object.keys(WIDGETS)) {
      expect(shell, `NodeConsole must not name the "${id}" widget`).not.toContain(`'${id}'`);
    }
  });

  it('App.svelte names no individual route', () => {
    const shell = code('./App.svelte');
    expect(shell).not.toMatch(/route\s*===/);
    for (const id of dirsIn('./routes/')) {
      expect(shell, `App must not name the "${id}" route`).not.toContain(`'${id}'`);
    }
  });
});

describe('the derived layouts are still the design\'s own', () => {
  it('assembles the template DEFAULT_LAYOUT (`:873`) from the widgets\' own declarations', () => {
    // 4+4+4 then 8+4 — the layout in the owner's screenshot, tiling exactly.
    expect(DEFAULT_LAYOUT.map((w) => [w.id, w.span])).toEqual([
      ['health', 4],
      ['identity', 4],
      ['service', 4],
      ['netmap', 8],
      ['throughput', 4],
    ]);
  });

  it('assembles the anonymous layout with no hole in it', () => {
    expect(PUBLIC_LAYOUT.map((w) => [w.id, w.span])).toEqual([
      ['health', 6],
      ['identity', 6],
      ['netmap', 12],
    ]);
  });
});
