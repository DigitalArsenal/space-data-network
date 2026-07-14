import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { PNM_TOPIC } from './pnm-publisher';

const publicExampleFiles = [
  '../../docs/docs.html',
  './stress/streaming.stress.test.ts',
] as const;

describe('public SDN channel naming examples', () => {
  it('use record codes rather than schema filenames in public API arguments', () => {
    const forbidden = [
      /\b(?:subscribe|publish)\(['"][A-Z]{3}\.fbs['"]/,
      /\bdataTypes:\s*\[[^\]]*['"][A-Z]{3}\.fbs['"]/,
      /\bSchemaType\b[^<\n]*[A-Z]{3}\.fbs/,
      /\bschema type\b[^<\n]*[A-Z]{3}\.fbs/i,
    ];

    for (const relativePath of publicExampleFiles) {
      const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8');
      for (const pattern of forbidden) {
        expect(source, `${relativePath} must not match ${pattern}`).not.toMatch(pattern);
      }
    }
  });

  it('documents the wire topic used by the shipped PNM publisher', () => {
    const docs = readFileSync(new URL('../../docs/docs.html', import.meta.url), 'utf8');
    expect(docs).toContain(PNM_TOPIC);
  });
});
