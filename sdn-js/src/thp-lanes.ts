/**
 * Signed Threat Profile ($THP) Lane Builders, Canonical Signing & Verification.
 *
 * Threat profile lanes ($THP) encode default multi-phenomenology screening
 * parameters, companion record references (($SCHEMA_FILE_ID, RECORD_ID)),
 * parameter-level provenance (including NOT_PUBLIC power / kinematic values
 * and analyst what-if templates), and optional Ed25519 signatures.
 *
 * All 7 default lanes ship with ACCESS_TIER = OPEN.
 */

import * as flatbuffers from 'flatbuffers';
import {
  PVNT,
  THP,
  THPParameterT,
  THPProvenanceT,
  THPReferenceT,
  THPT,
  pvnLicenseClass,
  pvnSourceKind,
  thpAccessTier,
  thpCertaintyLevel,
  thpParamValueKind,
  thpThreatCategory,
} from 'spacedatastandards.org/lib/js/THP/main.js';
import { initHDWallet, sign, verify } from './crypto';

const THP_SIGNATURE_DOMAIN_PREFIX = new TextEncoder().encode('SDN-THP-v1:');

export type ThreatPhenomenologySlug =
  | 'co-orbital'
  | 'direct-ascent'
  | 'rf-jammer'
  | 'gnss-denial'
  | 'directed-energy'
  | 'space-weather'
  | 'isr-custody';

export interface ThreatReferenceInput {
  schemaFileId: string;
  recordId: string;
  noradCatId?: number;
  cid?: string | null;
  role?: string | null;
}

export interface ThreatParameterInput {
  paramKey: string;
  valueKind: thpParamValueKind;
  numericValue?: number;
  textValue?: string | null;
  boolValue?: boolean;
  units?: string | null;
  sourceUri?: string;
  publishedDate?: string | null;
  certainty?: thpCertaintyLevel;
  accessTier?: thpAccessTier;
  notes?: string | null;
}

export interface ThreatLaneSpec {
  slug: ThreatPhenomenologySlug;
  thpId: string;
  name: string;
  description: string;
  category: thpThreatCategory;
  accessTier: thpAccessTier;
  active: boolean;
  linkedSchema: string;
  linkedRecordId: string;
  linkedNoradCatId: number;
  additionalRefs: ThreatReferenceInput[];
  parameters: ThreatParameterInput[];
  sourceId: string;
  originator: string;
  providerId: string;
  producer: string;
  lineage: string;
}

export interface ThreatLaneBuildOptions {
  timestamp?: string;
  signingPrivateKey?: Uint8Array;
  linkedRefsOverride?: Partial<Record<ThreatPhenomenologySlug, {
    linkedSchema?: string;
    linkedRecordId?: string;
    linkedNoradCatId?: number;
    additionalRefs?: ThreatReferenceInput[];
  }>>;
  accessTierOverride?: thpAccessTier;
}

export interface BuiltThreatLane {
  slug: ThreatPhenomenologySlug;
  thpId: string;
  category: thpThreatCategory;
  accessTier: thpAccessTier;
  bytes: Uint8Array;
  signed: boolean;
}

export interface ThreatSignatureVerificationResult {
  valid: boolean;
  signed: boolean;
  signatureLength: number;
  thpId: string;
  category: thpThreatCategory;
  accessTier: thpAccessTier;
}

export const DEFAULT_THP_LANE_SPECS: readonly ThreatLaneSpec[] = [
  {
    slug: 'co-orbital',
    thpId: 'thp-default-co-orbital-v1',
    name: 'Co-Orbital RPO Default Lane',
    description: 'Default co-orbital rendezvous & proximity operations lane referencing $OMM/$SIT/.',
    category: thpThreatCategory.CO_ORBITAL,
    accessTier: thpAccessTier.OPEN,
    active: true,
    linkedSchema: '$OMM',
    linkedRecordId: 'omm-default-inspector-ref',
    linkedNoradCatId: 90001,
    additionalRefs: [
      { schemaFileId: '$SIT', recordId: 'sit-default-rpo-watch', role: 'SITE' },
      { schemaFileId: '$RFB', recordId: 'rfb-default-tt-c-band', role: 'EMITTER' },
    ],
    parameters: [
      {
        paramKey: 'DELTA_V_BUDGET_M_S',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 150.0,
        units: 'm/s',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/co-orbital',
        certainty: thpCertaintyLevel.KINEMATIC_BOUND,
        accessTier: thpAccessTier.OPEN,
        notes: 'Kinematic delta-v reach envelope for default RPO threat screening.',
      },
      {
        paramKey: 'CLOSE_APPROACH_STANDOFF_KM',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 10.0,
        units: 'km',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/co-orbital',
        certainty: thpCertaintyLevel.PUBLISHED,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'WHAT_IF_TEMPLATE',
        valueKind: thpParamValueKind.TEXT,
        textValue: 'DELTA_V_BUDGET_M_S:50-500:m/s',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/co-orbital',
        certainty: thpCertaintyLevel.STATED_WHATIF,
        accessTier: thpAccessTier.OPEN,
      },
    ],
    sourceId: 'sdn-thp-defaults',
    originator: 'Space Data Network',
    providerId: 'sdn-default-thp',
    producer: 'sdn-js/thp-lanes',
    lineage: 'sdn-thp-default-v1',
  },
  {
    slug: 'direct-ascent',
    thpId: 'thp-default-direct-ascent-v1',
    name: 'Direct-Ascent Reach Default Lane',
    description: 'Default direct-ascent kinetic interceptor altitude-reach lane referencing $SIT and $OMM.',
    category: thpThreatCategory.DIRECT_ASCENT,
    accessTier: thpAccessTier.OPEN,
    active: true,
    linkedSchema: '$SIT',
    linkedRecordId: 'sit-default-da-asat-pad',
    linkedNoradCatId: 0,
    additionalRefs: [
      { schemaFileId: '$OMM', recordId: 'omm-default-target-ref', noradCatId: 25544, role: 'TARGET' },
    ],
    parameters: [
      {
        paramKey: 'APOGEE_REACH_KM',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 1200.0,
        units: 'km',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/direct-ascent',
        certainty: thpCertaintyLevel.KINEMATIC_BOUND,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'ASCENT_LEAD_TIME_S',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 420.0,
        units: 's',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/direct-ascent',
        certainty: thpCertaintyLevel.KINEMATIC_BOUND,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'WHAT_IF_TEMPLATE',
        valueKind: thpParamValueKind.TEXT,
        textValue: 'APOGEE_REACH_KM:400-2000:km',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/direct-ascent',
        certainty: thpCertaintyLevel.STATED_WHATIF,
        accessTier: thpAccessTier.OPEN,
      },
    ],
    sourceId: 'sdn-thp-defaults',
    originator: 'Space Data Network',
    providerId: 'sdn-default-thp',
    producer: 'sdn-js/thp-lanes',
    lineage: 'sdn-thp-default-v1',
  },
  {
    slug: 'rf-jammer',
    thpId: 'thp-default-rf-jammer-v1',
    name: 'RF Jammer Default Lane',
    description: 'Default uplink/downlink RF jamming lane referencing $RFB and $SIT with NOT_PUBLIC power parameters and what-if template.',
    category: thpThreatCategory.RF_JAMMER,
    accessTier: thpAccessTier.OPEN,
    active: true,
    linkedSchema: '$RFB',
    linkedRecordId: 'rfb-default-uHF-sband-jammer',
    linkedNoradCatId: 0,
    additionalRefs: [
      { schemaFileId: '$SIT', recordId: 'sit-default-rf-uplink-site', role: 'SITE' },
      { schemaFileId: '$OMM', recordId: 'omm-default-rf-victim', noradCatId: 43013, role: 'TARGET' },
    ],
    parameters: [
      {
        paramKey: 'jammer_eirp_dbw',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 72.0,
        units: 'dBW',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/rf-jammer',
        certainty: thpCertaintyLevel.NOT_PUBLIC,
        accessTier: thpAccessTier.OPEN,
        notes: 'NOT_PUBLIC emitter EIRP nominal placeholder; override via analyst what-if template.',
      },
      {
        paramKey: 'POWER_CLASSIFICATION',
        valueKind: thpParamValueKind.TEXT,
        textValue: 'NOT_PUBLIC',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/rf-jammer',
        certainty: thpCertaintyLevel.NOT_PUBLIC,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'CENTER_FREQ_MHZ',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 2250.0,
        units: 'MHz',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/rf-jammer',
        certainty: thpCertaintyLevel.PUBLISHED,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'WHAT_IF_TEMPLATE',
        valueKind: thpParamValueKind.TEXT,
        textValue: 'jammer_eirp_dbw:40-90:dBW',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/rf-jammer',
        certainty: thpCertaintyLevel.STATED_WHATIF,
        accessTier: thpAccessTier.OPEN,
      },
    ],
    sourceId: 'sdn-thp-defaults',
    originator: 'Space Data Network',
    providerId: 'sdn-default-thp',
    producer: 'sdn-js/thp-lanes',
    lineage: 'sdn-thp-default-v1',
  },
  {
    slug: 'gnss-denial',
    thpId: 'thp-default-gnss-denial-v1',
    name: 'GNSS Denial Default Lane',
    description: 'Default PNT / GNSS L-band interference & spoofing lane referencing $RFB and $SIT.',
    category: thpThreatCategory.GNSS_DENIAL,
    accessTier: thpAccessTier.OPEN,
    active: true,
    linkedSchema: '$RFB',
    linkedRecordId: 'rfb-default-gnss-l1-l2',
    linkedNoradCatId: 0,
    additionalRefs: [
      { schemaFileId: '$SIT', recordId: 'sit-default-gnss-denial-zone', role: 'SITE' },
    ],
    parameters: [
      {
        paramKey: 'DENIAL_RADIUS_KM',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 450.0,
        units: 'km',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/gnss-denial',
        certainty: thpCertaintyLevel.KINEMATIC_BOUND,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'JAMMING_TO_SIGNAL_DB',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 38.0,
        units: 'dB',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/gnss-denial',
        certainty: thpCertaintyLevel.NOT_PUBLIC,
        accessTier: thpAccessTier.OPEN,
        notes: 'NOT_PUBLIC emitter J/S margin template.',
      },
      {
        paramKey: 'WHAT_IF_TEMPLATE',
        valueKind: thpParamValueKind.TEXT,
        textValue: 'DENIAL_RADIUS_KM:100-1000:km',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/gnss-denial',
        certainty: thpCertaintyLevel.STATED_WHATIF,
        accessTier: thpAccessTier.OPEN,
      },
    ],
    sourceId: 'sdn-thp-defaults',
    originator: 'Space Data Network',
    providerId: 'sdn-default-thp',
    producer: 'sdn-js/thp-lanes',
    lineage: 'sdn-thp-default-v1',
  },
  {
    slug: 'directed-energy',
    thpId: 'thp-default-directed-energy-v1',
    name: 'Directed Energy Default Lane',
    description: 'Default optical laser blinding/dazzling lane referencing ground optical $SIT and $OMM.',
    category: thpThreatCategory.DIRECTED_ENERGY,
    accessTier: thpAccessTier.OPEN,
    active: true,
    linkedSchema: '$SIT',
    linkedRecordId: 'sit-default-dew-optical-facility',
    linkedNoradCatId: 0,
    additionalRefs: [
      { schemaFileId: '$OMM', recordId: 'omm-default-eo-asset', noradCatId: 40697, role: 'TARGET' },
    ],
    parameters: [
      {
        paramKey: 'LASER_WAVELENGTH_NM',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 1064.0,
        units: 'nm',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/directed-energy',
        certainty: thpCertaintyLevel.PUBLISHED,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'BEAM_FLUENCE_J_CM2',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 0.15,
        units: 'J/cm^2',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/directed-energy',
        certainty: thpCertaintyLevel.NOT_PUBLIC,
        accessTier: thpAccessTier.OPEN,
        notes: 'NOT_PUBLIC emitter fluence class; adjust via what-if override.',
      },
      {
        paramKey: 'WHAT_IF_TEMPLATE',
        valueKind: thpParamValueKind.TEXT,
        textValue: 'BEAM_FLUENCE_J_CM2:0.05-1.0:J/cm^2',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/directed-energy',
        certainty: thpCertaintyLevel.STATED_WHATIF,
        accessTier: thpAccessTier.OPEN,
      },
    ],
    sourceId: 'sdn-thp-defaults',
    originator: 'Space Data Network',
    providerId: 'sdn-default-thp',
    producer: 'sdn-js/thp-lanes',
    lineage: 'sdn-thp-default-v1',
  },
  {
    slug: 'space-weather',
    thpId: 'thp-default-space-weather-v1',
    name: 'Space Weather & Radiation Default Lane',
    description: 'Default geomagnetic/SEE radiation hazard lane referencing $SWS and $RBF.',
    category: thpThreatCategory.SPACE_WEATHER,
    accessTier: thpAccessTier.OPEN,
    active: true,
    linkedSchema: '$SWS',
    linkedRecordId: 'sws-default-geomag-kp',
    linkedNoradCatId: 0,
    additionalRefs: [
      { schemaFileId: '$RBF', recordId: 'rbf-default-van-allen-belt', role: 'ENVIRONMENT' },
      { schemaFileId: '$OMM', recordId: 'omm-default-target-ref', noradCatId: 25544, role: 'TARGET' },
    ],
    parameters: [
      {
        paramKey: 'KP_THRESHOLD',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 6.0,
        units: 'Kp',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/space-weather',
        certainty: thpCertaintyLevel.MEASURED,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'PROTON_FLUX_PFU',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 100.0,
        units: 'pfu',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/space-weather',
        certainty: thpCertaintyLevel.PUBLISHED,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'WHAT_IF_TEMPLATE',
        valueKind: thpParamValueKind.TEXT,
        textValue: 'KP_THRESHOLD:4-9:Kp',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/space-weather',
        certainty: thpCertaintyLevel.STATED_WHATIF,
        accessTier: thpAccessTier.OPEN,
      },
    ],
    sourceId: 'sdn-thp-defaults',
    originator: 'Space Data Network',
    providerId: 'sdn-default-thp',
    producer: 'sdn-js/thp-lanes',
    lineage: 'sdn-thp-default-v1',
  },
  {
    slug: 'isr-custody',
    thpId: 'thp-default-isr-custody-v1',
    name: 'ISR Overflight & Tracking Custody Default Lane',
    description: 'Default ISR collection geometry & tracking custody vulnerability lane referencing $SIT and $OMM.',
    category: thpThreatCategory.ISR_CUSTODY,
    accessTier: thpAccessTier.OPEN,
    active: true,
    linkedSchema: '$SIT',
    linkedRecordId: 'sit-default-optical-radar-site',
    linkedNoradCatId: 0,
    additionalRefs: [
      { schemaFileId: '$OMM', recordId: 'omm-default-isr-collector', noradCatId: 49044, role: 'PLATFORM' },
      { schemaFileId: '$RFB', recordId: 'rfb-default-xband-sar', role: 'EMITTER' },
    ],
    parameters: [
      {
        paramKey: 'COLLECTION_ELEVATION_DEG',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 10.0,
        units: 'deg',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/isr-custody',
        certainty: thpCertaintyLevel.KINEMATIC_BOUND,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'CUSTODY_REVISIT_S',
        valueKind: thpParamValueKind.SCALAR_DOUBLE,
        numericValue: 3600.0,
        units: 's',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/isr-custody',
        certainty: thpCertaintyLevel.PUBLISHED,
        accessTier: thpAccessTier.OPEN,
      },
      {
        paramKey: 'WHAT_IF_TEMPLATE',
        valueKind: thpParamValueKind.TEXT,
        textValue: 'CUSTODY_REVISIT_S:600-14400:s',
        sourceUri: 'https://spacedatanetwork.org/thp/defaults/isr-custody',
        certainty: thpCertaintyLevel.STATED_WHATIF,
        accessTier: thpAccessTier.OPEN,
      },
    ],
    sourceId: 'sdn-thp-defaults',
    originator: 'Space Data Network',
    providerId: 'sdn-default-thp',
    producer: 'sdn-js/thp-lanes',
    lineage: 'sdn-thp-default-v1',
  },
];

function encodeTHPObject(obj: THPT): Uint8Array {
  const builder = new flatbuffers.Builder(1024);
  const offset = obj.pack(builder);
  THP.finishTHPBuffer(builder, offset);
  return builder.asUint8Array().slice();
}

export function decodeThreatProfileRecord(bytes: Uint8Array): THPT {
  const view = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  const bb = new flatbuffers.ByteBuffer(view);
  if (!THP.bufferHasIdentifier(bb)) {
    throw new Error('Expected $THP FlatBuffer payload');
  }
  return THP.getRootAsTHP(bb).unpack();
}

export function canonicalThreatProfilePayload(bytes: Uint8Array): Uint8Array {
  const unpacked = decodeThreatProfileRecord(bytes);
  unpacked.SIGNATURE = [];
  const unsignedBytes = encodeTHPObject(unpacked);
  const out = new Uint8Array(THP_SIGNATURE_DOMAIN_PREFIX.length + unsignedBytes.length);
  out.set(THP_SIGNATURE_DOMAIN_PREFIX, 0);
  out.set(unsignedBytes, THP_SIGNATURE_DOMAIN_PREFIX.length);
  return out;
}

export async function signThreatProfileRecord(
  bytes: Uint8Array,
  signingPrivateKey: Uint8Array,
): Promise<Uint8Array> {
  const ready = await initHDWallet();
  if (!ready) {
    throw new Error('HD Wallet WASM unavailable for Ed25519 signing');
  }
  const unpacked = decodeThreatProfileRecord(bytes);
  const payload = canonicalThreatProfilePayload(bytes);
  const signature = await sign(signingPrivateKey, payload);
  unpacked.SIGNATURE = Array.from(signature);
  return encodeTHPObject(unpacked);
}

export async function verifyThreatProfileSignature(
  bytes: Uint8Array,
  publicKey: Uint8Array,
): Promise<ThreatSignatureVerificationResult> {
  const unpacked = decodeThreatProfileRecord(bytes);
  const sigBytes = new Uint8Array(unpacked.SIGNATURE ?? []);
  const thpId = typeof unpacked.THP_ID === 'string'
    ? unpacked.THP_ID
    : new TextDecoder().decode(unpacked.THP_ID ?? new Uint8Array());
  if (sigBytes.length === 0) {
    return {
      valid: false,
      signed: false,
      signatureLength: 0,
      thpId,
      category: unpacked.CATEGORY,
      accessTier: unpacked.ACCESS_TIER,
    };
  }
  const ready = await initHDWallet();
  if (!ready) {
    throw new Error('HD Wallet WASM unavailable for Ed25519 signature verification');
  }
  const payload = canonicalThreatProfilePayload(bytes);
  const valid = await verify(publicKey, payload, sigBytes);
  return {
    valid,
    signed: true,
    signatureLength: sigBytes.length,
    thpId,
    category: unpacked.CATEGORY,
    accessTier: unpacked.ACCESS_TIER,
  };
}

export async function buildThreatProfileRecord(
  spec: ThreatLaneSpec,
  options: ThreatLaneBuildOptions = {},
): Promise<BuiltThreatLane> {
  const timestamp = options.timestamp ?? new Date().toISOString();
  const refOverride = options.linkedRefsOverride?.[spec.slug];
  const linkedSchema = refOverride?.linkedSchema ?? spec.linkedSchema;
  const linkedRecordId = refOverride?.linkedRecordId ?? spec.linkedRecordId;
  const linkedNoradCatId = refOverride?.linkedNoradCatId ?? spec.linkedNoradCatId;
  const additionalRefs = (refOverride?.additionalRefs ?? spec.additionalRefs).map(
    (ref) =>
      new THPReferenceT(
        ref.schemaFileId,
        ref.recordId,
        ref.noradCatId ?? 0,
        ref.cid ?? null,
        ref.role ?? null,
      ),
  );
  const accessTier = options.accessTierOverride ?? spec.accessTier;
  const parameters = spec.parameters.map(
    (param) =>
      new THPParameterT(
        param.paramKey,
        param.valueKind,
        param.numericValue ?? 0,
        param.textValue ?? null,
        param.boolValue ?? false,
        param.units ?? null,
        new THPProvenanceT(
          param.sourceUri ?? 'https://spacedatanetwork.org/thp/defaults',
          param.publishedDate ?? timestamp,
          param.certainty ?? thpCertaintyLevel.PUBLISHED,
          param.accessTier ?? accessTier,
          null,
          param.notes ?? null,
        ),
      ),
  );

  const provenance = new PVNT(
    spec.sourceId,
    pvnSourceKind.Reference,
    spec.originator,
    spec.providerId,
    spec.producer,
    pvnLicenseClass.Reproducible,
    'CC-BY-4.0',
    spec.lineage,
    'https://spacedatanetwork.org/thp/defaults',
    '1.0.0',
    timestamp,
    null,
    0n,
    null,
  );

  const thp = new THPT(
    spec.thpId,
    spec.name,
    spec.description,
    spec.category,
    accessTier,
    spec.active,
    linkedSchema,
    linkedRecordId,
    linkedNoradCatId >>> 0,
    additionalRefs,
    parameters,
    provenance,
    timestamp,
    timestamp,
    [],
  );

  let bytes = encodeTHPObject(thp);
  let signed = false;
  if (options.signingPrivateKey) {
    bytes = await signThreatProfileRecord(bytes, options.signingPrivateKey);
    signed = true;
  }

  return {
    slug: spec.slug,
    thpId: spec.thpId,
    category: spec.category,
    accessTier,
    bytes,
    signed,
  };
}

export async function buildDefaultThreatProfileRecords(
  options: ThreatLaneBuildOptions = {},
): Promise<BuiltThreatLane[]> {
  const timestamp = options.timestamp ?? new Date().toISOString();
  const out: BuiltThreatLane[] = [];
  for (const spec of DEFAULT_THP_LANE_SPECS) {
    out.push(await buildThreatProfileRecord(spec, { ...options, timestamp }));
  }
  return out;
}
