import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { PNM_TOPIC } from './pnm-publisher';

// The rendered guides live only in the node's embed tree (served at /docs/);
// docs/build-docs.mjs writes them there.
const SERVER_OVERVIEW = '../../sdn-server/cmd/spacedatanetwork/embedded/docs/server-overview.html';

describe('public SDN channel naming examples', () => {
  it('documents the wire topic used by the shipped PNM publisher', () => {
    const docs = readFileSync(new URL(SERVER_OVERVIEW, import.meta.url), 'utf8');
    expect(docs).toContain(PNM_TOPIC);
  });
});
