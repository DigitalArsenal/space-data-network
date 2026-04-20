import type {
  StoreAuthorResult,
  StoreDataResult,
  StoreFeedEntry,
  StorePluginResult,
  StoreSearchResults,
} from '../../../src/ui/runtime/store-search';
import { escapeHtml } from '../dom/escape';
import type { StoreSelection } from '../state/types';

export function renderStoreSpotlight(
  results: StoreSearchResults,
  selection: StoreSelection | null,
  searchQuery: string,
): string {
  if (searchQuery.trim()) {
    return '';
  }
  const featured = [
    ...results.plugins.slice(0, 2),
    ...results.data.slice(0, 2),
  ];
  if (featured.length === 0) {
    return '';
  }
  return `
    <section class="sdn-store-spotlight">
      <div class="sdn-store-spotlight__header">
        <h3>Popular now</h3>
      </div>
      <div class="sdn-store-feed">
        ${featured.map((entry) => renderStoreFeedEntry(entry, selection)).join('')}
      </div>
    </section>
  `;
}

export function renderStoreFeed(
  entries: StoreFeedEntry[],
  selection: StoreSelection | null,
  searchQuery: string,
): string {
  if (entries.length === 0) {
    return searchQuery.trim()
      ? '<div class="sdn-empty">No live store results matched this search.</div>'
      : '';
  }

  return `<div class="sdn-store-feed">${entries.map((entry) => renderStoreFeedEntry(entry, selection)).join('')}</div>`;
}

export function renderStoreFeedEntry(
  entry: StoreFeedEntry,
  selection: StoreSelection | null,
): string {
  switch (entry.kind) {
    case 'author':
      return `
        <button
          type="button"
          class="sdn-store-card${selection?.kind === 'author' && selection.key === entry.key ? ' sdn-store-card--active' : ''}"
          data-store-result-kind="author"
          data-store-result-key="${escapeHtml(entry.key)}"
        >
          <span class="sdn-store-card__meta">Author</span>
          <strong>${escapeHtml(entry.name)}</strong>
          <span>${escapeHtml(entry.handle ?? entry.peerId ?? `${entry.moduleCount} linked modules`)}</span>
          ${renderStoreChipRow(entry.standardsUsed)}
        </button>
      `;
    case 'plugin':
      return `
        <button
          type="button"
          class="sdn-store-card${selection?.kind === 'plugin' && selection.key === entry.key ? ' sdn-store-card--active' : ''}"
          data-store-result-kind="plugin"
          data-store-result-key="${escapeHtml(entry.key)}"
        >
          <span class="sdn-store-card__meta">Plugin</span>
          <strong>${escapeHtml(entry.listing.name ?? entry.listing.pluginId)}</strong>
          <span>${escapeHtml(entry.publisherLabel)} · v${escapeHtml(entry.listing.version)}</span>
          <span>${escapeHtml(entry.listing.tagline ?? entry.listing.description ?? 'Signed marketplace listing')}</span>
          ${renderStoreChipRow(entry.standardsUsed)}
        </button>
      `;
    case 'data':
      return `
        <button
          type="button"
          class="sdn-store-card${selection?.kind === 'data' && selection.key === entry.key ? ' sdn-store-card--active' : ''}"
          data-store-result-kind="data"
          data-store-result-key="${escapeHtml(entry.key)}"
        >
          <span class="sdn-store-card__meta">Data</span>
          <strong>${escapeHtml(entry.standard)}</strong>
          <span>${escapeHtml(entry.description)}</span>
          <span>${escapeHtml(`${entry.moduleCount} linked modules · ${entry.publisherNames.length} publishers`)}</span>
          ${renderStoreChipRow([entry.standard])}
        </button>
      `;
  }
}

export function renderStoreDetail(
  results: StoreSearchResults,
  selection: StoreSelection | null,
): string {
  if (!selection) {
    return '<div class="sdn-empty">No store result selected.</div>';
  }

  if (selection.kind === 'plugin') {
    const plugin = results.plugins.find((entry) => entry.key === selection.key);
    if (!plugin) {
      return '<div class="sdn-empty">No plugin selected.</div>';
    }
    const listing = plugin.listing;
    return `
      <div class="sdn-store-detail">
        <p class="sdn-kicker">Plugin</p>
        <h3>${escapeHtml(listing.name ?? listing.pluginId)}</h3>
        <p class="sdn-copy">${escapeHtml(listing.description ?? listing.tagline ?? 'Signed PLG marketplace listing.')}</p>
        ${renderStoreChipRow(plugin.standardsUsed)}
        <div class="sdn-store-detail__facts">
          <span>Publisher: ${escapeHtml(plugin.publisherLabel)}</span>
          <span>Plugin ID: ${escapeHtml(listing.pluginId)}</span>
          <span>Version: ${escapeHtml(listing.version)}</span>
          <span>Status: ${escapeHtml(listing.status ?? 'public')}</span>
        </div>
        <section class="sdn-control-grid">
          <label class="sdn-field">
            <span>Requester domain</span>
            <input id="sdn-requester-domain" type="text" value="app.example.com" />
          </label>
          <label class="sdn-field">
            <span>Grant timeout (ms)</span>
            <input id="sdn-request-timeout" type="number" min="1000" step="1000" value="300000" />
          </label>
          <label class="sdn-field">
            <span>Invoke method</span>
            <input id="sdn-invoke-method" type="text" value="echo" />
          </label>
          <label class="sdn-field">
            <span>Invoke payload</span>
            <textarea id="sdn-invoke-payload" rows="3">live browser request</textarea>
          </label>
        </section>
        <div class="sdn-action-row">
          <button id="sdn-run-live-flow" type="button" class="sdn-button">Run live workflow</button>
          <button type="button" class="sdn-ghost-button" data-store-open-workspace="pinning">Open pinning rules</button>
        </div>
      </div>
    `;
  }

  if (selection.kind === 'author') {
    const author = results.authors.find((entry) => entry.key === selection.key);
    if (!author) {
      return '<div class="sdn-empty">No author selected.</div>';
    }
    return `
      <div class="sdn-store-detail">
        <p class="sdn-kicker">Author</p>
        <h3>${escapeHtml(author.name)}</h3>
        <div class="sdn-store-detail__facts">
          <span>Handle: ${escapeHtml(author.handle ?? '<none>')}</span>
          <span>Peer ID: ${escapeHtml(author.peerId ?? '<unknown>')}</span>
          <span>Linked plugins: ${escapeHtml(String(author.moduleCount))}</span>
        </div>
        ${renderStoreChipRow(author.standardsUsed)}
        <pre class="sdn-code">${escapeHtml(JSON.stringify({
          pluginIds: author.pluginIds,
          standardsUsed: author.standardsUsed,
        }, null, 2))}</pre>
        <div class="sdn-action-row">
          <button type="button" class="sdn-ghost-button" data-store-open-workspace="pinning">Open pinning rules</button>
        </div>
      </div>
    `;
  }

  const dataEntry = results.data.find((entry) => entry.key === selection.key);
  if (!dataEntry) {
    return '<div class="sdn-empty">No SDS-linked data result selected.</div>';
  }
  return `
    <div class="sdn-store-detail">
      <p class="sdn-kicker">Data</p>
      <h3>${escapeHtml(dataEntry.standard)}</h3>
      <p class="sdn-copy">${escapeHtml(dataEntry.description)}</p>
      ${renderStoreChipRow([dataEntry.standard])}
      <div class="sdn-store-detail__facts">
        <span>Linked plugins: ${escapeHtml(String(dataEntry.moduleCount))}</span>
        <span>Publishers: ${escapeHtml(dataEntry.publisherNames.join(', ') || '<none>')}</span>
      </div>
      <pre class="sdn-code">${escapeHtml(JSON.stringify({
        pluginIds: dataEntry.pluginIds,
        publisherNames: dataEntry.publisherNames,
      }, null, 2))}</pre>
      <div class="sdn-action-row">
        <button type="button" class="sdn-ghost-button" data-store-open-workspace="pinning">Open pinning rules</button>
      </div>
    </div>
  `;
}

export function renderStoreChipRow(values: string[]): string {
  if (values.length === 0) {
    return '<span class="sdn-store-chip-row__empty">No SDS standards referenced</span>';
  }
  return `
    <div class="sdn-store-chip-row">
      ${values.map((value) => `<span class="sdn-chip">${escapeHtml(value)}</span>`).join('')}
    </div>
  `;
}

export function hasStoreSelection(
  results: StoreSearchResults,
  selection: StoreSelection,
): boolean {
  switch (selection.kind) {
    case 'plugin':
      return results.plugins.some((result) => result.key === selection.key);
    case 'author':
      return results.authors.some((result) => result.key === selection.key);
    case 'data':
      return results.data.some((result) => result.key === selection.key);
  }
}

export function resolveStoreSelection(
  results: StoreSearchResults,
  selection: StoreSelection | null,
): StoreSelection | null {
  if (selection && hasStoreSelection(results, selection)) {
    return selection;
  }
  return results.plugins[0]
    ? { kind: 'plugin', key: results.plugins[0].key }
    : results.authors[0]
      ? { kind: 'author', key: results.authors[0].key }
      : results.data[0]
        ? { kind: 'data', key: results.data[0].key }
        : null;
}

export function getSelectedPluginListing(
  results: StoreSearchResults,
  selection: StoreSelection | null,
): StorePluginResult | undefined {
  if (!selection || selection.kind !== 'plugin') {
    return results.plugins[0];
  }
  return results.plugins.find((result) => result.key === selection.key) ?? results.plugins[0];
}

export type StoreViewSelection =
  | StoreAuthorResult
  | StorePluginResult
  | StoreDataResult;
