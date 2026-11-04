// The pinned SDS bindings carry the AP and UNSPECIFIED mean-element sources and the OCM mean elements.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { meanElementSource } from 'spacedatastandards.org/lib/js/MET/main.js';
import * as OCM from 'spacedatastandards.org/lib/js/OCM/main.js';

const require = createRequire(import.meta.url);
const pin = JSON.parse(readFileSync(new URL('../package.json', import.meta.url))).dependencies['spacedatastandards.org'];
const installed = JSON.parse(readFileSync(require.resolve('spacedatastandards.org/package.json'))).version;
assert.equal(installed, pin);
assert.equal(meanElementSource.AP, 5);
assert.equal(meanElementSource.UNSPECIFIED, 6);
assert.equal(typeof OCM.OCMMeanElements, 'function');
console.log(`PASS spacedatastandards.org ${installed}: MET AP=${meanElementSource.AP} UNSPECIFIED=${meanElementSource.UNSPECIFIED}; OCMMeanElements exported`);
