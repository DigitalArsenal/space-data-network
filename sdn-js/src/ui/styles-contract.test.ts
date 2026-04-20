import { readFileSync } from 'node:fs';
import path from 'node:path';

import { describe, expect, it } from 'vitest';

const stylesPath = path.resolve(
  __dirname,
  '../../ui/src/styles.css',
);
const styles = readFileSync(stylesPath, 'utf8');

describe('sdn admin layout css contract', () => {
  it('locks the main shell height and prevents browser-window scrolling', () => {
    expect(styles).toMatch(/html,\s*body,\s*#app\s*\{[^}]*overflow:\s*hidden;/s);
    expect(styles).toMatch(/\.sdn-admin-shell\s*\{[^}]*height:\s*100vh;/s);
  });

  it('keeps workspace scrolling inside the active section instead of the whole page', () => {
    expect(styles).toMatch(/\.sdn-admin-main\s*\{[^}]*overflow:\s*hidden;/s);
    expect(styles).toMatch(/\.sdn-admin-page\s*\{[^}]*overflow:\s*hidden;/s);
    expect(styles).toMatch(/\.sdn-admin-workspace--active\s*\{[^}]*overflow:\s*auto;/s);
  });

  it('uses one shared explicit topbar control height for the URL bar and right-side actions', () => {
    expect(styles).toMatch(/\.sdn-command-bar\s*\{[^}]*height:\s*var\(--sdn-control-height\);/s);
    expect(styles).toMatch(/\.sdn-admin-topbar__actions\s*>\s*\.sdn-button,\s*\.sdn-admin-topbar__actions\s*>\s*\.sdn-ghost-button,\s*\.sdn-admin-topbar__actions\s*>\s*\.sdn-account-button\s*\{[^}]*height:\s*var\(--sdn-control-height\);/s);
  });
});
