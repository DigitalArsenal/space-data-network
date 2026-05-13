import { describe, expect, it } from 'vitest';
import { loadingMetricLabel } from '../../ui/src/lib/data-loading-labels';

describe('data loading labels', () => {
  it('uses Loading for metric values while the data page is initializing', () => {
    expect(loadingMetricLabel(true, '0')).toBe('Loading');
    expect(loadingMetricLabel(true, '0 B')).toBe('Loading');
  });

  it('uses formatted metric values after initialization', () => {
    expect(loadingMetricLabel(false, '0')).toBe('0');
    expect(loadingMetricLabel(false, '2.4 MB')).toBe('2.4 MB');
  });
});
