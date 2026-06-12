import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const publicExampleFiles = [
  '../../docs/docs.html',
  './stress/streaming.stress.test.ts',
] as const;

describe('public SDN channel naming examples', () => {
  it('do not expose schema-file suffixes for SDS record codes', () => {
    const forbidden = [
      /\/spacedatanetwork\/sds\/[A-Z]{3}\.fbs/,
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
});
