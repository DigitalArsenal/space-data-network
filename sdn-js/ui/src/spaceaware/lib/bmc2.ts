/**
 * Pure data/logic for the BMC2 mode boards (loop task U2.1), lifted out of
 * the Svelte components so the mode-tab table and route mapping are
 * unit-testable without mounting anything.
 *
 * Ground truth: `design_handoff/bmc2/BMC2_Modes_Index.dc.html` +
 * `BMC2_F1_Surveillance.dc.html` / `BMC2_F2_Track.dc.html` /
 * `BMC2_F3_Sensors.dc.html` (F4–F6 accent values are read from their own
 * `.dc.html` files too, even though those boards stay on the
 * `ScaffoldScreen` placeholder until loop task U2.2 — the top-bar nav is
 * shared across all six boards, so its accent table is complete now).
 *
 * These are static, template-only mockups (bmc2/README.md: "no logic
 * class") — the only real behavior is active-route highlighting, so
 * everything below is a plain lookup table, not runtime state.
 */

import type { Bmc2Mode } from '../router';

export const BMC2_MODES_ORDER: readonly Bmc2Mode[] = ['f1', 'f2', 'f3', 'f4', 'f5', 'f6'];

export interface Bmc2ModeTab {
  id: Bmc2Mode;
  /** Chakra Petch tab label, e.g. "SURVEILLANCE". */
  label: string;
  /** F-key tag, e.g. "F1". */
  key: string;
}

/** Left-to-right order + labels for the shared top mode bar (all six boards). */
export const BMC2_MODE_TABS: readonly Bmc2ModeTab[] = [
  { id: 'f1', label: 'SURVEILLANCE', key: 'F1' },
  { id: 'f2', label: 'TRACK', key: 'F2' },
  { id: 'f3', label: 'SENSORS', key: 'F3' },
  { id: 'f4', label: 'CONJUNCTION', key: 'F4' },
  { id: 'f5', label: 'MANEUVER', key: 'F5' },
  { id: 'f6', label: 'COMMS', key: 'F6' },
];

export interface Bmc2Accent {
  /** Active-tab fill. */
  background: string;
  /** Active-tab border (full `border` shorthand value). */
  border: string;
  /** Active-tab label color. */
  label: string;
  /** Active-tab F-key sub-label color. */
  sub: string;
}

/**
 * Per-mode active-tab accent, read verbatim from each board's own top-bar
 * markup (F1/F2/F3 share one cyan/ice accent; F4 red, F5 amber, F6 green —
 * DESIGN_TOKENS.md's semantic accenting). Inactive tabs never take an
 * accent (see `BMC2_INACTIVE_TAB_STYLE`).
 */
export const BMC2_MODE_ACCENTS: Record<Bmc2Mode, Bmc2Accent> = {
  f1: {
    background: 'rgba(74,166,224,0.16)',
    border: '1px solid rgba(120,190,230,0.5)',
    label: '#eaf6f8',
    sub: '#7fb4d6',
  },
  f2: {
    background: 'rgba(74,166,224,0.16)',
    border: '1px solid rgba(120,190,230,0.5)',
    label: '#eaf6f8',
    sub: '#7fb4d6',
  },
  f3: {
    background: 'rgba(74,166,224,0.16)',
    border: '1px solid rgba(120,190,230,0.5)',
    label: '#eaf6f8',
    sub: '#7fb4d6',
  },
  f4: {
    background: 'rgba(255,107,107,0.16)',
    border: '1px solid rgba(255,107,107,0.5)',
    label: '#ffd2d2',
    sub: '#d68a8a',
  },
  f5: {
    background: 'rgba(255,178,77,0.15)',
    border: '1px solid rgba(255,178,77,0.5)',
    label: '#ffe6bf',
    sub: '#c9a07a',
  },
  f6: {
    background: 'rgba(90,214,160,0.14)',
    border: '1px solid rgba(90,214,160,0.45)',
    label: '#cdeede',
    sub: '#7cbf9c',
  },
};

/** Inactive top-bar tab styling — identical for every mode. */
export const BMC2_INACTIVE_TAB_STYLE = {
  background: 'transparent',
  border: '1px solid rgba(90,150,180,0.18)',
  label: '#8aa0aa',
  sub: '#4d6671',
} as const;

/** `/bmc2` for the index, `/bmc2/f1`…`/bmc2/f6` for a mode board. */
export function bmc2Route(mode: Bmc2Mode | null): string {
  return mode ? `/bmc2/${mode}` : '/bmc2';
}

/** Resolves the accent for a top-bar tab given the currently active mode. */
export function bmc2TabAccent(tabId: Bmc2Mode, activeMode: Bmc2Mode): Bmc2Accent | typeof BMC2_INACTIVE_TAB_STYLE {
  return tabId === activeMode ? BMC2_MODE_ACCENTS[tabId] : BMC2_INACTIVE_TAB_STYLE;
}

/** Per-board top-bar kicker text (next to the "ORBITAL BMC2" wordmark). */
export const BMC2_KICKERS: Record<Bmc2Mode, string> = {
  f1: 'COMMON OPERATING PICTURE',
  f2: 'TRACK · INSPECT',
  f3: 'SENSORS · COVERAGE',
  f4: 'CONJUNCTION · THREAT',
  f5: 'MANEUVER · PLANNING',
  f6: 'COMMS · GROUND',
};

/** D3: every BMC2 board is demo-mode v1 (static mockup, no live feed). */
export const BMC2_DEMO_TAG_TITLE =
  'Static design mockup — BMC2 boards ship demo-mode per decision D3 pending live catalog/screening/comms feeds.';

// ---------------------------------------------------------------------------
// Index page (BMC2_Modes_Index.dc.html)
// ---------------------------------------------------------------------------

export type Bmc2CardVariant = 'cyan' | 'red' | 'amber' | 'green';

export interface Bmc2IndexCardMeta {
  label: string;
  text: string;
}

export interface Bmc2IndexCard {
  mode: Bmc2Mode;
  variant: Bmc2CardVariant;
  title: string;
  description: string;
  meta: readonly Bmc2IndexCardMeta[];
}

/** The six mode cards on the index grid, copy lifted verbatim from the `.dc.html`. */
export const BMC2_INDEX_CARDS: readonly Bmc2IndexCard[] = [
  {
    mode: 'f1',
    variant: 'cyan',
    title: 'SURVEILLANCE',
    description: 'Marquee multi-select, affiliation coloring, the COP catalog. Build groups & watch lists.',
    meta: [
      { label: 'PORTRAIT', text: 'selection force breakdown' },
      { label: 'CENTER', text: 'sortable catalog table + filters' },
      { label: 'CARD', text: 'group / watch / isolate / export' },
    ],
  },
  {
    mode: 'f2',
    variant: 'cyan',
    title: 'TRACK',
    description: 'Single-object deep dive. Attitude frame, orbit elements, subsystem tabs, tasking.',
    meta: [
      { label: 'PORTRAIT', text: 'attitude + ref-frame (ECI/ECEF/RIC)' },
      { label: 'CENTER', text: 'OVERVIEW / POWER / PROP / SENSORS / COMMS' },
      { label: 'CARD', text: 'satellite ability grid' },
    ],
  },
  {
    mode: 'f3',
    variant: 'cyan',
    title: 'SENSORS',
    description: 'Sensor volumes, field-of-view footprints, access & coverage analysis.',
    meta: [
      { label: 'PORTRAIT', text: 'cone geometry / boresight / range' },
      { label: 'CENTER', text: 'access timeline + look angles' },
      { label: 'CARD', text: 'add FOV / slew / footprint / tip-cue' },
    ],
  },
  {
    mode: 'f4',
    variant: 'red',
    title: 'CONJUNCTION',
    description: 'RPO & collision screening. Miss distance, Pc, TCA, threat rings.',
    meta: [
      { label: 'PORTRAIT', text: 'relative motion (RIC/Hill)' },
      { label: 'CENTER', text: 'screening list + countdowns' },
      { label: 'CARD', text: 'screen / COLA / threat fan / warn' },
    ],
  },
  {
    mode: 'f5',
    variant: 'amber',
    title: 'MANEUVER',
    description: 'Plan burns & compare courses of action. Preview before/after orbits, authorize, execute.',
    meta: [
      { label: 'PORTRAIT', text: 'ΔV budget + burn attitude' },
      { label: 'CENTER', text: 'COA compare + plan→execute' },
      { label: 'CARD', text: 'prograde / retro / preview / execute' },
    ],
  },
  {
    mode: 'f6',
    variant: 'green',
    title: 'COMMS',
    description: 'Ground stations, pass schedules, link budgets, ground tracks.',
    meta: [
      { label: 'PORTRAIT', text: 'link margin / rate / EIRP' },
      { label: 'CENTER', text: 'contact schedule + 2D ground track' },
      { label: 'CARD', text: 'downlink / uplink / schedule / band' },
    ],
  },
];

export interface Bmc2CardVariantStyle {
  title: string;
  tag: string;
  tagBorder: string;
  description: string;
  metaLabel: string;
  meta: string;
}

/** Per-variant text colors for the index cards (border/background are plain CSS classes — see Bmc2Index.svelte). */
export const BMC2_CARD_VARIANT_STYLE: Record<Bmc2CardVariant, Bmc2CardVariantStyle> = {
  cyan: {
    title: '#eaf6f8',
    tag: '#7fb4d6',
    tagBorder: 'rgba(120,190,230,0.4)',
    description: '#7d929b',
    metaLabel: '#5a7a8a',
    meta: '#6f8693',
  },
  red: {
    title: '#ffd2d2',
    tag: '#d68a8a',
    tagBorder: 'rgba(255,107,107,0.4)',
    description: '#b89a9a',
    metaLabel: '#a07a7a',
    meta: '#8a6f6f',
  },
  amber: {
    title: '#ffe6bf',
    tag: '#c9a07a',
    tagBorder: 'rgba(255,178,77,0.4)',
    description: '#b8a487',
    metaLabel: '#a08a5f',
    meta: '#8a7a5f',
  },
  green: {
    title: '#cdeede',
    tag: '#7cbf9c',
    tagBorder: 'rgba(90,214,160,0.4)',
    description: '#8fb3a0',
    metaLabel: '#7c9c8a',
    meta: '#6f8f7c',
  },
};
