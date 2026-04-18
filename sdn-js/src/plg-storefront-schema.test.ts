import { describe, expect, it } from 'vitest';
import { PLG } from 'spacedatastandards.org/lib/js/REC/PLG.js';

describe('canonical PLG storefront fields', () => {
  it('exposes storefront and commerce accessors', () => {
    expect(typeof PLG.prototype.TAGLINE).toBe('function');
    expect(typeof PLG.prototype.PUBLISHER_NAME).toBe('function');
    expect(typeof PLG.prototype.TAGS).toBe('function');
    expect(typeof PLG.prototype.SCREENSHOT_URLS).toBe('function');
    expect(typeof PLG.prototype.PAYMENT_MODEL).toBe('function');
    expect(typeof PLG.prototype.PRICE_USD_CENTS).toBe('function');
    expect(typeof PLG.prototype.LISTING_STATUS).toBe('function');
  });
});
