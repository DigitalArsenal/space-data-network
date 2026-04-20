import type { ObservedPeerRecord } from '../../../src/ui/runtime/types';
import { escapeHtml } from '../dom/escape';
import type { ModuleDeliveryEventLike, ProviderDescriptor } from '../state/types';

export function buildProviderDescriptorText(provider: ProviderDescriptor): string {
  const payload: Record<string, unknown> = {
    publicKey: provider.publicKey,
    peerId: provider.peerId,
    ipns: provider.ipns,
    relayAddresses: provider.relayAddresses,
  };
  if (provider.identity) {
    payload.identity = {
      xpub: provider.identity.xpub,
      identityPublicKey: provider.identity.identityPublicKey,
      signingPublicKey: provider.identity.signingPublicKey,
      encryptionPublicKey: provider.identity.encryptionPublicKey,
      ipnsEntries: provider.identity.ipnsEntries,
      ensNames: provider.identity.ensNames,
      addresses: provider.identity.addresses,
    };
  }
  return JSON.stringify(payload, null, 2);
}

export function buildObservedPeersMarkup(records: ObservedPeerRecord[]): string {
  const items = records.slice(0, 6);
  if (items.length === 0) {
    return 'DHT, provider, and protocol evidence will stream here.';
  }
  return items.map((item) => `
      <div class="sdn-sighting">
        <strong>${escapeHtml(item.peerId)}</strong>
        <span>${escapeHtml(item.sources.join(', '))}</span>
        <span>${escapeHtml(item.detail ?? '')}</span>
      </div>
    `).join('');
}

export function buildTimelineMarkup(events: ModuleDeliveryEventLike[]): string {
  if (events.length === 0) {
    return '<div class="sdn-empty">Challenge, grant, fetch, decrypt, load, and invoke events appear in order.</div>';
  }
  return `
    <ol class="sdn-timeline">
      ${events.map((event) => `
        <li>
          <strong>${escapeHtml(event.stage)}</strong>
          <span>${escapeHtml(event.detail ?? event.cid ?? '')}</span>
        </li>
      `).join('')}
    </ol>
  `;
}
