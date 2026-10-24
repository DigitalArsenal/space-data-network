// Builds the $PCF 1.0 and $PRW fixtures that TestPCFAndPRWEventsFromPublishedJS
// decodes, with the PUBLISHED spacedatastandards.org JavaScript bindings
// (sdn-js/node_modules), never with Go or by hand.
//
//   node sdn-server/internal/sds/testdata/pcf-prw/build-fixtures.mjs
//
// It writes pcf-1-0.bin, prw-execution-request-events.bin,
// prw-execution-result-events.bin and fixtures.json (the SDS version that
// built them, and each file's size and sha256). The Go test refuses fixtures
// built by any other version than the Go module it links, so a pin bump
// rebuilds them with this one command.
//
// Every record is a bare finished buffer, the canonical stored form. Values
// are synthetic and deliberately off their defaults, so a field decoded from
// the wrong slot cannot pass by accident.
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const installed = join(here, '../../../../../sdn-js/node_modules');
const sdsPackage = join(installed, 'spacedatastandards.org');
const load = (path) => import(pathToFileURL(path).href);
const flatbuffers = await load(join(installed, 'flatbuffers/js/flatbuffers.js'));
const pcf = await load(join(sdsPackage, 'lib/js/PCF/main.js'));
const prw = await load(join(sdsPackage, 'lib/js/PRW/main.js'));
const sdsVersion = JSON.parse(readFileSync(join(sdsPackage, 'package.json'), 'utf8')).version;

const make = (type, fields) => Object.assign(new type(), fields);

// The tables $PRW shares with $PCF are built once per binding set, from the
// binding that owns the message, so each message uses its own generated classes.
function instant(lib, iso) {
  return make(lib.TIMInstantT, {
    TIME_SYSTEM: lib.timingStandard.UTC,
    EPOCH_FORMAT: lib.timEpochRepresentation.ISO8601,
    ISO8601: iso,
  });
}

function integrator(lib) {
  return make(lib.PCFIntegratorSettingsT, {
    ALGORITHM: lib.pcfIntegratorAlgorithm.DOPRI853,
    INITIAL_STEP_SECONDS: 30,
    MINIMUM_STEP_SECONDS: 0.5,
    MAXIMUM_STEP_SECONDS: 900,
    ABSOLUTE_TOLERANCES: [0.001, 0.002, 0.003, 0.000001, 0.000002, 0.000003],
    RELATIVE_TOLERANCE: 1e-11,
    MAXIMUM_STEPS: 250000,
  });
}

function forces(lib) {
  return make(lib.PCFForceModelT, {
    GRAVITY_CHOICE: lib.pcfGravitySelection.EGM2008,
    GRAVITATIONAL_PARAMETER: 3.986004418e14,
    MAXIMUM_DEGREE: 70, HAS_MAXIMUM_DEGREE: true,
    MAXIMUM_ORDER: 60, HAS_MAXIMUM_ORDER: true,
    ENABLE_THIRD_BODY: true, THIRD_BODY_IDS: [10, 301],
    ENABLE_SRP: true, ENABLE_DRAG: true,
    INITIAL_MASS_KG: 540, AREA_M2: 12.5,
    REFLECTIVITY_COEFFICIENT: 1.3, DRAG_COEFFICIENT: 2.1,
    ATMOSPHERE_MODEL: lib.pcfAtmosphereFamily.JB2008,
    WEATHER: make(lib.PCFSpaceWeatherT, {
      EPOCH: instant(lib, '2026-10-24T00:00:00Z'),
      F107: 148.25, F107_AVERAGE: 151.75, AP_INDEX: 9, KP_INDEX: 2,
    }),
    EPHEMERIS_SOURCE: 'DE440',
    SOLID_TIDES: lib.pcfSolidTideModel.IERS_2010,
    RELATIVITY: lib.pcfRelativityTerms.IERS_2010,
    IN_TRACK_ACCELERATION_M_S2: 1.5e-9, HAS_IN_TRACK_ACCELERATION_M_S2: true,
    MAXIMUM_TESSERAL_DEGREE: 40, HAS_MAXIMUM_TESSERAL_DEGREE: true,
    RADIATION_PRESSURE_MODEL: lib.pcfRadiationPressureFamily.GNSS_BOX_WING,
    GNSS_BLOCK: lib.pcfGnssSpacecraftBlock.GPS_IIF,
    ECOM2: make(lib.PCFEcom2T, {
      D0_M_S2: 1e-9, Y0_M_S2: 2e-10, B0_M_S2: 3e-10, D2_COS_M_S2: 4e-11,
      D2_SIN_M_S2: 5e-11, D4_COS_M_S2: 6e-12, D4_SIN_M_S2: 7e-12,
      B1_COS_M_S2: 8e-11, B1_SIN_M_S2: 9e-11, B3_COS_M_S2: 1e-12, B3_SIN_M_S2: 2e-12,
    }),
    EARTH_RADIATION: lib.pcfEarthRadiationModel.KNOCKE,
    EARTH_RADIATION_RESOLUTION_DEG: 10,
    EARTH_RADIATION_AREA_OVER_MASS_M2_KG: 0.02, HAS_EARTH_RADIATION_AREA_OVER_MASS_M2_KG: true,
    OCEAN_TIDES: lib.pcfOceanTideModel.FES2004,
    OCEAN_TIDE_MAXIMUM_DEGREE: 20, OCEAN_TIDE_MAXIMUM_ORDER: 20,
    CENTRAL_BODY_NAIF_ID: 399,
    EPHEMERIS_KIND: lib.pcfEphemerisKind.PLANETARY_KERNEL,
    SHADOW_MODEL: lib.evlShadowModel.CONICAL,
    WEATHER_SOURCE: lib.pcfWeatherSource.CONSTANT,
    WEATHER_EXTENSION: lib.pcfWeatherExtension.FORECAST_CHAIN,
    DENSITY_CORRECTION: lib.pcfDensityCorrection.DCA_COMMON_MODE,
    DENSITY_CORRECTION_RELAXATION_HOURS: 6, HAS_DENSITY_CORRECTION_RELAXATION_HOURS: true,
    NEUTRAL_WIND: lib.pcfNeutralWind.HWM14,
    EMPIRICAL_TERMS: make(lib.PCFEmpiricalTermsT, { ONCE_PER_REVOLUTION: false }),
  });
}

function events(lib) {
  const term = (OPERATION, VALUE = 0, PARAMETER = lib.pceParameter.UNSPECIFIED) =>
    make(lib.PCFExpressionTermT, { OPERATION, VALUE, PARAMETER });
  return [
    make(lib.PCFEventDefinitionT, {
      KIND: lib.pcfEventKind.APSIS, DIRECTION: lib.pcfEventDirection.INCREASING,
      ACTION: lib.pcfEventAction.RECORD, TOLERANCE_SECONDS: 1e-6, OCCURRENCE: 1,
    }),
    make(lib.PCFEventDefinitionT, {
      KIND: lib.pcfEventKind.PLANE_CROSSING, DIRECTION: lib.pcfEventDirection.DECREASING,
      ACTION: lib.pcfEventAction.CONTINUE, TOLERANCE_SECONDS: 1e-7, OCCURRENCE: 3,
      PLANE_NORMAL: make(lib.FRMVector3T, { X: 0, Y: 0.5, Z: 0.8660254037844386 }),
    }),
    make(lib.PCFEventDefinitionT, {
      KIND: lib.pcfEventKind.EXPRESSION, DIRECTION: lib.pcfEventDirection.DECREASING,
      ACTION: lib.pcfEventAction.STOP, OCCURRENCE: 2, SECOND_TRAJECTORY_PORT: 'second_trajectory',
      EXPRESSION: make(lib.PCFExpressionT, {
        TERMS: [
          term(lib.pcfExpressionOperation.PARAMETER, 0, lib.pceParameter.ALTITUDE_OF_PERIAPSIS),
          term(lib.pcfExpressionOperation.CONSTANT, 180000),
          term(lib.pcfExpressionOperation.SUBTRACT),
        ],
      }),
    }),
  ];
}

function configuration() {
  const lib = pcf;
  return make(lib.PCFT, {
    CONFIGURATION_ID: 'sdn-sds-1-264-fixture',
    PROFILE: 'FFM-1',
    THEORY: lib.pcfTheory.FFM,
    INTEGRATOR: integrator(lib),
    FORCES: forces(lib),
    MEAN_ELEMENT: make(lib.PCFMeanElementOptionsT, {
      M_DAILY_MAXIMUM_DEGREE: 10, ZONAL_MAXIMUM_DEGREE: 18, SHORT_PERIODIC_MAXIMUM_DEGREE: 6,
      ENABLE_RESONANCE: false, RESONANCE_MAXIMUM_DEGREE: 28, RESONANCE_BAND: 0.05,
      RESONANCE_MAXIMUM_K: 2, NEAR_RESONANCE_BAND: 0.01, GRID_STEP_DIVISOR: 2,
      DENSITY_KERNEL: false, DRAG_REGIME_PERIGEE_KM: 2200,
    }),
    SEGMENTS: make(lib.PCFSegmentOptionsT, {
      MOON_GRAVITY_MAXIMUM_DEGREE: 60, MOON_SHORT_PERIODIC_MAXIMUM_DEGREE: 7,
      MOON_ECCENTRICITY_ORDER: 3, MOON_STEP_SECONDS: 200, CISLUNAR_STEP_SECONDS: 50,
      HELIOCENTRIC_STEP_SECONDS: 1500, SEGMENT_RMS_FACTOR: 2.5,
      SEGMENT_RMS_MARGIN_KM: 0.04, CISLUNAR_MINIMUM_SEGMENT_HOURS: 5,
    }),
    FIT: make(lib.PCFFitPolicyT, {
      DEFAULT_SPAN_HOURS: 72, MINIMUM_SPAN_HOURS: 30, MAXIMUM_SPAN_HOURS: 144,
      CANDIDATE_SPANS_HOURS: [48, 72, 96], TAIL_HOURS: 18, EXTENSION_MARGIN: 0.85,
      HOLDOUT_HOURS: 12, HAS_HOLDOUT_HOURS: true, ECCENTRICITY_THRESHOLD: 0.45,
      ECCENTRIC_DEFAULT_SPAN_HOURS: 20, ECCENTRIC_EXTENSION_MARGIN: 0.65,
      ECCENTRIC_MAXIMUM_PERIOD_HOURS: 30, IMPLAUSIBLE_RMS_KM: 80,
      DRAG_PRIOR: make(lib.PCFGaussianPriorT, { MEAN: 0.01, SIGMA: 0.5, HAS_SIGMA: true }),
      SRP_PRIOR: make(lib.PCFGaussianPriorT, { MEAN: 0.002, SIGMA: 0.04, HAS_SIGMA: true }),
    }),
    EVENTS: events(lib),
    MANEUVER_DETECTION: make(lib.PCFManeuverDetectionT, {
      FALSE_ALARMS_PER_OBJECT_YEAR: 2, CORRELATION_MODEL: lib.pcfInnovationCorrelation.CORRELATION_TIME,
      THRESHOLD: 22.5, MARGIN_STATES: 10, REFINE_WINDOW_HOURS: 3, MINIMUM_STATES: 60,
    }),
    REENTRY: make(lib.PCFReentryT, {
      ALTITUDE_KM: 85, HANDOFF_PERIGEE_ALTITUDE_KM: 190, HANDOFF_DECAY_RATIO: 0.06,
      HANDOFF_ECCENTRICITY_COLLAPSE: 0.02, HANDOFF_MINIMUM_ECCENTRICITY: 0.015,
      NODE_STEP_MINUTES: 20, HORIZON_DAYS: 90, WINDOW_METHOD: lib.pcfReentryWindowMethod.UNSCENTED,
      WINDOW_DENSITY_SIGMA: 0.2, WINDOW_CORRELATION_DAYS: 3, WINDOW_BALLISTIC_SIGMA: 0.07,
      OSCULATING_STEP_SECONDS: 8,
    }),
    LAUNCH: make(lib.PCFLaunchT, {
      DRAG_AREA_OVER_MASS_PRIOR_M2_KG: 0.02, SRP_AREA_OVER_MASS_PRIOR_M2_KG: 0.015,
      SEPARATION_SIGMA_M_S: 1.5, SHRINK_SIGMA_M2_KG: 0.003, HAS_SHRINK_SIGMA_M2_KG: true,
      BATCH_MEAN_SIGMA_M2_KG: 0.8,
    }),
    OUTPUT: make(lib.PCFOutputT, {
      FRAME: lib.pcfOutputFrame.GCRF, STEP_SECONDS: 45, MAXIMUM_STATES: 120000,
    }),
  });
}

function residentState(lib, iso, x, vy) {
  const vector = (X, Y, Z) => make(lib.FRMVector3T, { X, Y, Z });
  return make(lib.PRWResidentStateT, {
    OBJECT_ID: '2026-100A',
    CATALOG_NUMBER: 65000,
    STATE: make(lib.FRMStateVectorT, {
      REPRESENTATION: lib.frmStateRepresentation.CARTESIAN,
      POSITION: vector(x, 0, 0),
      VELOCITY: vector(0, vy, 0),
      COORDINATE_SYSTEM_NAME: 'GCRF', EPOCH: iso, EPOCH_TIME_SYSTEM: 'UTC',
    }),
    COORDINATE_SYSTEM: make(lib.RFMCoordinateSystemT, {
      NAME: 'GCRF', AXIS_TYPE: lib.rfmAxisType.ICRF,
      ORIGIN: make(lib.RFMOriginT, { KIND: lib.rfmOriginKind.CELESTIAL_BODY, CELESTIAL_BODY_ID: 399 }),
    }),
    MASS_KG: 540, HAS_MASS_KG: true,
  });
}

function executionRequest() {
  const lib = prw;
  return make(lib.PRWT, {
    EXECUTION_REQUEST: make(lib.PRWExecutionRequestT, {
      INITIAL: residentState(lib, '2026-10-24T12:00:00Z', 6778137, 7668.558),
      TARGET_EPOCH: instant(lib, '2026-10-25T12:00:00Z'),
      INTEGRATOR: integrator(lib),
      FORCES: forces(lib),
      EVENTS: events(lib),
    }),
  });
}

function executionResult() {
  const lib = prw;
  const record = (EVENT_INDEX, KIND, DIRECTION, iso, ELAPSED_SECONDS, RESIDUAL, ITERATIONS, x, vy) =>
    make(lib.PRWEventRecordT, {
      EVENT_INDEX, KIND, DIRECTION, EPOCH: instant(lib, iso), ELAPSED_SECONDS,
      STATE: residentState(lib, iso, x, vy), RESIDUAL, ITERATIONS,
    });
  return make(lib.PRWT, {
    EXECUTION_RESULT: make(lib.PRWExecutionResultT, {
      FINAL_SAMPLE: make(lib.PRWPropagationSampleT, {
        STATE: residentState(lib, '2026-10-24T15:30:00Z', 6791200.5, 7661.25),
        ACCEPTED_STEPS: 1440n, REJECTED_STEPS: 3n,
      }),
      ELAPSED_SECONDS: 12600.5,
      EPHEMERIS_SOURCE: 'DE440',
      EVENTS: [
        record(0, lib.pcfEventKind.APSIS, lib.pcfEventDirection.INCREASING,
          '2026-10-24T13:02:03Z', 3723.25, 1.25e-9, 4, 6771000.25, 7672.5),
        record(2, lib.pcfEventKind.EXPRESSION, lib.pcfEventDirection.DECREASING,
          '2026-10-24T15:30:00Z', 12600.5, -3.5e-8, 6, 6791200.5, 7661.25),
      ],
      EVENT_OCCURRENCES: [2, 0, 2],
      STOPPED_BY_EVENT: 2,
    }),
  });
}

function finished(lib, type, message) {
  const builder = new flatbuffers.Builder(4096);
  lib[type][`finish${type}Buffer`](builder, message.pack(builder));
  return builder.asUint8Array();
}

const files = {
  'pcf-1-0.bin': finished(pcf, 'PCF', configuration()),
  'prw-execution-request-events.bin': finished(prw, 'PRW', executionRequest()),
  'prw-execution-result-events.bin': finished(prw, 'PRW', executionResult()),
};
const manifest = { sdsVersion, files: {} };
for (const [name, bytes] of Object.entries(files)) {
  writeFileSync(join(here, name), bytes);
  manifest.files[name] = { bytes: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex') };
}
writeFileSync(join(here, 'fixtures.json'), JSON.stringify(manifest, null, 2) + '\n');
console.log(`built ${Object.keys(files).length} fixtures with spacedatastandards.org ${sdsVersion}`);
