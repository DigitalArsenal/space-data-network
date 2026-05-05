import fs from 'node:fs/promises';
import path from 'node:path';

import { describe, expect, it } from 'vitest';

const repoRoot = path.resolve(__dirname, '../../../..');

async function readRepoFile(relativePath: string): Promise<string> {
  return fs.readFile(path.join(repoRoot, relativePath), 'utf8');
}

describe('upstream IPFS mirror boundaries', () => {
  it('keeps the upstream WebUI navigation free of SDN product routes', async () => {
    const source = await readRepoFile('webui/src/navigation/NavBar.js');

    expect(source).toContain("to='/diagnostics'");
    expect(source).not.toContain("href='/webui'");
    expect(source).not.toContain('Space Data Network');
    expect(source).not.toContain('sdn-logo');
  });

  it('does not require SDN-only custom schemes in Kubo CORS configuration', async () => {
    const daemonConfig = await readRepoFile('desktop/src/daemon/config.js');
    const launchE2E = await readRepoFile('desktop/test/e2e/launch.e2e.test.js');
    const notConnectedHelp = await readRepoFile('webui/src/components/is-not-connected/is-not-connected.tsx');

    expect(daemonConfig).not.toContain("'sdn://-'");
    expect(daemonConfig).not.toContain("'webui://-'");
    expect(daemonConfig).not.toContain('"sdn://-"');
    expect(daemonConfig).not.toContain('"webui://-"');
    expect(launchE2E).not.toContain('sdn://-');
    expect(launchE2E).not.toContain('webui://-');
    expect(notConnectedHelp).not.toContain('sdn://-');
    expect(notConnectedHelp).not.toContain('webui://-');
  });
});
