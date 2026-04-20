import { readFileSync } from 'node:fs';
import path from 'node:path';

import { describe, expect, it } from 'vitest';

import {
  accountIconSvg,
  connectIconSvg,
  directoryIconSvg,
  frontendIconSvg,
  ipfsDashboardIconSvg,
  networkIconSvg,
  refreshIconSvg,
  storeIconSvg,
  walletIconSvg,
} from '../../ui/src/icons';

const iconsPath = path.resolve(__dirname, '../../ui/src/icons.ts');
const iconsSource = readFileSync(iconsPath, 'utf8');

describe('sdn shell icons', () => {
  it('uses the IPFS peers icon for the network rail item', () => {
    expect(networkIconSvg).toContain('viewBox="0 0 19.9 18.8"');
    expect(networkIconSvg).toContain('M14.9 10.7c-.4 0-.8.1-1.1.2');
  });

  it('uses IPFS stroke icons for the remaining shell actions', () => {
    expect(directoryIconSvg).toContain('M72.53 32.38H55.08');
    expect(storeIconSvg).toContain('M83.4 42.43h-6.89V27');
    expect(frontendIconSvg).toContain('M75 24H25.11');
    expect(walletIconSvg).toContain('M85 39.72h-3V29');
    expect(accountIconSvg).toContain('m74.68 66.44-.14-.13');
    expect(connectIconSvg).toContain('M76.84 33.65A5.16 5.16 0 0 0 82 28.5');
    expect(ipfsDashboardIconSvg).toContain('M84.173 64.865v-29.1a4.48 4.48 0 1 0-4.4-7.63');
    expect(refreshIconSvg).toContain('M81.92 74.8 65.8 58.67');
  });

  it('sources the shell icons from the upstream IPFS webui icon files', () => {
    expect(iconsSource).toContain("../../../webui/src/icons/StrokePeersSmall.tsx?raw");
    expect(iconsSource).toContain("../../../webui/src/icons/StrokeFolder.tsx?raw");
    expect(iconsSource).toContain("../../../webui/src/icons/StrokeBasket.tsx?raw");
    expect(iconsSource).toContain("../../../webui/src/icons/StrokeCode.tsx?raw");
    expect(iconsSource).toContain("../../../webui/src/icons/StrokeWallet.tsx?raw");
    expect(iconsSource).toContain("../../../webui/src/icons/StrokeUser.tsx?raw");
    expect(iconsSource).toContain("../../../webui/src/icons/StrokeServer.tsx?raw");
    expect(iconsSource).toContain("../../../webui/src/icons/StrokeCube.tsx?raw");
    expect(iconsSource).toContain("../../../webui/src/icons/StrokeSearch.tsx?raw");
  });
});
