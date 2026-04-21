import strokeBasketSource from '../../../webui/src/icons/StrokeBasket.tsx?raw';
import strokeCodeSource from '../../../webui/src/icons/StrokeCode.tsx?raw';
import strokeCubeSource from '../../../webui/src/icons/StrokeCube.tsx?raw';
import strokeFolderSource from '../../../webui/src/icons/StrokeFolder.tsx?raw';
import strokePeersSmallSource from '../../../webui/src/icons/StrokePeersSmall.tsx?raw';
import strokePinSource from '../../../webui/src/icons/StrokePin.tsx?raw';
import strokeSearchSource from '../../../webui/src/icons/StrokeSearch.tsx?raw';
import strokeServerSource from '../../../webui/src/icons/StrokeServer.tsx?raw';
import strokeUserSource from '../../../webui/src/icons/StrokeUser.tsx?raw';
import strokeWalletSource from '../../../webui/src/icons/StrokeWallet.tsx?raw';

export const brandMarkSvg = `
  <svg viewBox="0 0 512 512" aria-hidden="true" focusable="false">
    <defs>
      <linearGradient id="sdn-brand-a" x1="84.315" x2="527.72" y1="771.51" y2="771.51" gradientUnits="userSpaceOnUse">
        <stop offset="0" stop-color="#4a9ea1" />
      </linearGradient>
      <linearGradient id="sdn-brand-b" x1="99.675" x2="512.36" y1="771.48" y2="771.48" gradientUnits="userSpaceOnUse">
        <stop offset="0" stop-color="#63d3d7" />
      </linearGradient>
    </defs>
    <path d="M84.315 899.51l221.7 128 221.7-128v-256l-221.7-127.99-221.7 128z" fill="url(#sdn-brand-a)" transform="translate(-50.017 -515.51)" />
    <path d="M283.13 546.35l-160.74 92.806a38.396 38.396 0 0 1 0 8.59l160.75 92.805c13.554-10 32.043-10 45.597 0l160.75-92.807a38.343 38.343 0 0 1-.001-8.588l-160.74-92.806c-13.554 10.001-32.044 10.001-45.599 0zm221.79 127.03L344 767.22c1.884 16.739-7.361 32.751-22.799 39.489l.18 184.58a38.386 38.386 0 0 1 7.439 4.294l160.75-92.805c-1.884-16.739 7.36-32.752 22.799-39.49v-185.61a38.397 38.397 0 0 1-7.44-4.294zm-397.81 1.032a38.387 38.387 0 0 1-7.438 4.295v185.61c15.438 6.738 24.683 22.75 22.799 39.489l160.74 92.806a38.4 38.4 0 0 1 7.44-4.295v-185.61c-15.439-6.738-24.684-22.75-22.8-39.49l-160.74-92.81z" fill="url(#sdn-brand-b)" transform="translate(-50.017 -515.51)" />
    <path d="M256 512l221.7-128V128L256 256v256z" fill-opacity=".251" />
    <path d="M256 512V256L34.3 128v256L256 512z" fill-opacity=".039" />
    <path d="M34.298 128l221.7 128 221.7-128-221.7-128-221.7 128z" fill-opacity=".13" />
  </svg>
`;

export const networkIconSvg = iconFromWebuiComponent(strokePeersSmallSource);
export const directoryIconSvg = iconFromWebuiComponent(strokeFolderSource);
export const storeIconSvg = iconFromWebuiComponent(strokeBasketSource);
export const pinningIconSvg = iconFromWebuiComponent(strokePinSource);
export const frontendIconSvg = iconFromWebuiComponent(strokeCodeSource);
export const walletIconSvg = iconFromWebuiComponent(strokeWalletSource);
export const accountIconSvg = iconFromWebuiComponent(strokeUserSource);
export const connectIconSvg = iconFromWebuiComponent(strokeServerSource);
export const ipfsDashboardIconSvg = iconFromWebuiComponent(strokeCubeSource);
export const refreshIconSvg = iconFromWebuiComponent(strokeSearchSource);
export const searchIconSvg = iconFromWebuiComponent(strokeSearchSource);
export const featureCarouselArrowSvg = `
  <svg viewBox="0 0 100 100" aria-hidden="true" focusable="false">
    <path
      d="M61 18 29 50l32 32 10-10-22-22 22-22z"
      fill="currentColor"
    />
  </svg>
`;

function iconFromWebuiComponent(source: string): string {
  const svgMatch = source.match(/<svg\b([\s\S]*?)>([\s\S]*?)<\/svg>/);
  if (!svgMatch) {
    throw new Error('expected upstream webui icon source to contain an <svg> root');
  }

  const rawAttributes = normalizeSvgMarkup(svgMatch[1]).replace(/\sxmlns="[^"]*"/g, '').trim();
  const body = normalizeSvgMarkup(svgMatch[2]).trim();

  return `
  <svg${rawAttributes ? ` ${rawAttributes}` : ''} aria-hidden="true" focusable="false">
    ${body}
  </svg>
`;
}

function normalizeSvgMarkup(markup: string): string {
  return markup
    .replace(/\{\.\.\.props\}/g, '')
    .replace(/className=/g, 'class=')
    .replace(/(\s[\w:-]+)=\{([^}]+)\}/g, (_match, attribute, value) => {
      const trimmed = value.trim();
      if (/^-?\d+(\.\d+)?$/.test(trimmed)) {
        return `${attribute}="${trimmed}"`;
      }
      if (/^['"][\s\S]*['"]$/.test(trimmed)) {
        return `${attribute}=${trimmed}`;
      }
      return '';
    })
    .replace(/\s+/g, ' ')
    .replace(/\s+\/>/g, ' />');
}
