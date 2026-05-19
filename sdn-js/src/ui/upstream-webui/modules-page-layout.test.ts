import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../../..');
const modulesPagePath = path.join(
  repoRoot,
  'ui/src/upstream-webui/overrides/modules/ModulesPage.js',
);

describe('modules page responsive table layout', () => {
  it('keeps the row status pill visible when module names are long', async () => {
    const source = await readFile(modulesPagePath, 'utf8');

    expect(source).toContain("style={modulesTableStyle}");
    expect(source).toContain("<col style={moduleNameColumnStyle} />");
    expect(source).toContain("<col style={moduleStatusColumnStyle} />");
    expect(source).toContain("<col style={moduleMemoryColumnStyle} />");
    expect(source).toContain("style={moduleNameCellStyle}");
    expect(source).toContain("style={moduleStatusCellStyle}");
    expect(source).toContain("style={moduleMemoryCellStyle}");
    expect(source).toMatch(/tableLayout:\s*'fixed'/);
    expect(source).toMatch(/moduleNameCellStyle[\s\S]*?minWidth:\s*0/);
    expect(source).toMatch(/moduleStatusCellStyle[\s\S]*?whiteSpace:\s*'nowrap'/);
  });

  it('filters modules with an explicit table search field', async () => {
    const source = await readFile(modulesPagePath, 'utf8');

    expect(source).toContain('moduleSearch');
    expect(source).toContain('filteredModules');
    expect(source).toContain("aria-label='Search modules'");
    expect(source).toContain("placeholder='Search modules'");
    expect(source).toMatch(/filterModuleBySearch\(module,\s*moduleSearch\)/);
    expect(source).toContain('No modules match the current search.');
  });

  it('keeps lifecycle actions on one row with stronger IPFS-toned colors', async () => {
    const source = await readFile(modulesPagePath, 'utf8');

    expect(source).toContain("style={lifecycleActionBarStyle}");
    expect(source).toMatch(/lifecycleActionBarStyle[\s\S]*?flexWrap:\s*'nowrap'/);
    expect(source).toMatch(/lifecycleActionButtonBaseStyle[\s\S]*?whiteSpace:\s*'nowrap'/);
    expect(source).toContain("background: '#0b6b70'");
    expect(source).toContain("background: '#d9480f'");
    expect(source).toContain("background: '#c92a2a'");
  });

  it('uses compact status pills and replaces methods with a configure affordance', async () => {
    const source = await readFile(modulesPagePath, 'utf8');

    expect(source).toContain("style={statusPillStyle}");
    expect(source).toMatch(/statusPillStyle[\s\S]*?fontSize:\s*'0\.7rem'/);
    expect(source).not.toContain("<DetailSection title='Methods'>");
    expect(source).not.toContain('function MethodList');
    expect(source).toContain('ConfigureModuleButton');
    expect(source).toContain('Configure module');
    expect(source).toContain('moduleConfigureUrl(module)');
  });
});
