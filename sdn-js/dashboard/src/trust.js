/*
 * Trust framework surface for the status dashboard.
 *
 * Mirrors sdn-server/internal/peers/trust.go: the feed's TRUST_LEVEL strings
 * are the PGP-ownertrust-scale names emitted by TrustLevel.String() —
 * never / unknown / marginal / standard / full / admin / ultimate.
 * "unknown" (a.k.a. legacy Untrusted) is NO assertion; every other tier —
 * including never, which is an explicit negative veto — IS an assertion.
 */

/** Canonical tier order, most-trusted first (for sorting + the filter UI). */
export const TRUST_TIERS = ['ultimate', 'admin', 'full', 'standard', 'marginal', 'unknown', 'never'];

/** Numeric rank for sorting: higher = more trusted; never below unknown. */
const RANK = { ultimate: 6, admin: 5, full: 4, standard: 3, marginal: 2, unknown: 1, never: 0 };

/** Normalize a feed TRUST_LEVEL string to a canonical tier ('' → 'unknown'). */
export function normalizeTrust(level) {
  const t = (level ?? '').trim().toLowerCase();
  return RANK[t] !== undefined ? t : 'unknown';
}

/** @returns {number} sort rank of a (raw) trust level string */
export function trustRank(level) {
  return RANK[normalizeTrust(level)];
}

/**
 * True when the tier is an explicit operator assertion (anything but
 * unknown/empty). This is what the "hide offline unless trusted" setting
 * keys on: never counts — a veto is information the operator set.
 */
export function hasTrustAssertion(level) {
  return normalizeTrust(level) !== 'unknown';
}

/**
 * Theme-token color name per tier (resolved against theme.js by the view;
 * this module stays free of design imports for testability).
 */
export const TRUST_COLOR_TOKEN = {
  ultimate: 'cyan',
  admin: 'cyan',
  full: 'green',
  standard: 'ice',
  marginal: 'amber',
  unknown: 'textMuted',
  never: 'red',
};
