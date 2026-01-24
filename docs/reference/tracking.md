# Tracking Data Schemas

Reference for observation and tracking data schemas.

## TDM - Tracking Data Message

Contains ground-based or space-based tracking measurements.

### Schema Definition

```flatbuffers
table TDM {
  // CCSDS Header
  CCSDS_TDM_VERS: string;          // Version (e.g., "2.0")
  CREATION_DATE: string;           // Message creation time
  ORIGINATOR: string;              // Data source

  // Metadata
  PARTICIPANT_1: string;           // Ground station identifier
  PARTICIPANT_2: string;           // Space object identifier
  MODE: string;                    // SEQUENTIAL, SINGLE_DIFF
  PATH: string;                    // 1,2 or 2,1
  PATH_1: string;                  // First path
  PATH_2: string;                  // Second path

  // Time
  START_TIME: string;              // First observation time
  STOP_TIME: string;               // Last observation time
  TIME_SYSTEM: string;             // Time reference (UTC)

  // Reference Frame
  REF_FRAME: string;               // ITRF, GCRF, etc.

  // Data
  OBSERVATIONS: [TrackingObservation];
}

table TrackingObservation {
  EPOCH: string;                   // Observation timestamp

  // Range measurements
  RANGE: double;                   // Range (km)
  RANGE_RATE: double;              // Range rate (km/s)
  RANGE_UNITS: string;             // km, m, etc.
  RANGE_MODE: string;              // COHERENT, NON_COHERENT

  // Angle measurements
  AZIMUTH: double;                 // Azimuth (degrees)
  ELEVATION: double;               // Elevation (degrees)
  ANGLE_TYPE: string;              // AZEL, RADEC

  // Doppler
  DOPPLER_INSTANTANEOUS: double;   // Instantaneous Doppler (Hz)
  DOPPLER_INTEGRATED: double;      // Integrated Doppler (cycles)

  // Signal properties
  RECEIVE_FREQ: double;            // Receive frequency (MHz)
  TRANSMIT_FREQ: double;           // Transmit frequency (MHz)
  CARRIER_TO_NOISE: double;        // C/N0 (dB-Hz)

  // Quality
  DATA_QUALITY: string;            // Quality indicator
  RESIDUAL: double;                // Fit residual
}
```

### Measurement Types

| Type | Unit | Description |
|------|------|-------------|
| RANGE | km | Distance to object |
| RANGE_RATE | km/s | Rate of change of distance |
| AZIMUTH | degrees | Horizontal angle from North |
| ELEVATION | degrees | Vertical angle from horizon |
| DOPPLER | Hz | Frequency shift |

### Example

```typescript
const tdm = {
  CCSDS_TDM_VERS: "2.0",
  CREATION_DATE: "2024-01-15T12:30:00.000Z",
  ORIGINATOR: "EXAMPLE-STATION",

  PARTICIPANT_1: "STATION-01",
  PARTICIPANT_2: "25544",
  MODE: "SEQUENTIAL",
  TIME_SYSTEM: "UTC",
  REF_FRAME: "ITRF",

  START_TIME: "2024-01-15T12:00:00.000Z",
  STOP_TIME: "2024-01-15T12:15:00.000Z",

  OBSERVATIONS: [
    {
      EPOCH: "2024-01-15T12:00:00.000Z",
      RANGE: 450.5,
      RANGE_RATE: -2.3,
      AZIMUTH: 45.0,
      ELEVATION: 30.0,
      CARRIER_TO_NOISE: 55.0,
      DATA_QUALITY: "NOMINAL"
    },
    {
      EPOCH: "2024-01-15T12:00:10.000Z",
      RANGE: 427.8,
      RANGE_RATE: -2.2,
      AZIMUTH: 46.5,
      ELEVATION: 32.5,
      CARRIER_TO_NOISE: 56.0,
      DATA_QUALITY: "NOMINAL"
    }
    // ... more observations
  ]
};
```

---

## RFM - RF Metrics Message

Radio frequency measurements for signal characterization.

### Schema Definition

```flatbuffers
table RFM {
  // Header
  CREATION_DATE: string;
  ORIGINATOR: string;

  // Target
  OBJECT_ID: string;               // Space object identifier
  OBJECT_NAME: string;

  // Observation
  EPOCH: string;                   // Measurement time
  STATION_ID: string;              // Receiving station

  // Frequency
  CENTER_FREQUENCY: double;        // Center frequency (MHz)
  BANDWIDTH: double;               // Signal bandwidth (kHz)
  POLARIZATION: string;            // RHCP, LHCP, LINEAR

  // Signal Strength
  EIRP: double;                    // Effective Isotropic Radiated Power (dBW)
  RECEIVED_POWER: double;          // Received power (dBm)
  CARRIER_TO_NOISE: double;        // C/N0 (dB-Hz)
  SIGNAL_TO_NOISE: double;         // SNR (dB)

  // Doppler
  DOPPLER_SHIFT: double;           // Doppler shift (Hz)
  DOPPLER_RATE: double;            // Doppler rate (Hz/s)

  // Modulation
  MODULATION_TYPE: string;         // BPSK, QPSK, etc.
  DATA_RATE: double;               // Data rate (bps)
  BIT_ERROR_RATE: double;          // Measured BER

  // Interference
  INTERFERENCE_DETECTED: bool;
  INTERFERENCE_SOURCE: string;
  INTERFERENCE_LEVEL: double;      // Interference level (dB)
}
```

---

## CTR - Contact Report

Communication contact/pass summary.

### Schema Definition

```flatbuffers
table CTR {
  // Header
  CREATION_DATE: string;
  ORIGINATOR: string;

  // Contact Identification
  CONTACT_ID: string;              // Unique contact identifier
  STATION_ID: string;              // Ground station
  OBJECT_ID: string;               // Space object

  // Timing
  AOS: string;                     // Acquisition of Signal
  LOS: string;                     // Loss of Signal
  CONTACT_DURATION: double;        // Duration (seconds)
  MAX_ELEVATION_TIME: string;      // Time of max elevation
  MAX_ELEVATION: double;           // Maximum elevation (degrees)

  // Data Transfer
  UPLINK_DATA_VOLUME: uint64;      // Bytes uplinked
  DOWNLINK_DATA_VOLUME: uint64;    // Bytes downlinked
  COMMANDS_SENT: uint32;           // Commands transmitted
  TELEMETRY_FRAMES: uint32;        // Telemetry frames received

  // Quality
  CONTACT_STATUS: string;          // NOMINAL, DEGRADED, FAILED
  ANOMALIES: [string];             // Any anomalies noted
  AVERAGE_SNR: double;             // Average SNR (dB)
  DROPOUTS: uint32;                // Number of signal dropouts

  // Next Contact
  NEXT_AOS: string;                // Next acquisition of signal
  NEXT_STATION: string;            // Next ground station
}
```

---

## Usage Patterns

### Collect Tracking Data

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Subscribe to tracking data
node.subscribe('TDM', (tdm, peerId) => {
  console.log(`Tracking data from ${tdm.PARTICIPANT_1}`);
  console.log(`Target: ${tdm.PARTICIPANT_2}`);
  console.log(`Observations: ${tdm.OBSERVATIONS.length}`);

  for (const obs of tdm.OBSERVATIONS) {
    console.log(`  ${obs.EPOCH}: Range=${obs.RANGE}km, El=${obs.ELEVATION}deg`);
  }
});
```

### Publish Observations

```typescript
// Publish tracking observations
const tdm = {
  CCSDS_TDM_VERS: "2.0",
  CREATION_DATE: new Date().toISOString(),
  ORIGINATOR: "MY-STATION",
  PARTICIPANT_1: "STATION-01",
  PARTICIPANT_2: "25544",
  START_TIME: startTime,
  STOP_TIME: endTime,
  OBSERVATIONS: observations
};

await node.publish('TDM', tdm);
```

### Query Historical Data

```typescript
// Get tracking data for specific object
const issTracking = await node.query('TDM', {
  where: { PARTICIPANT_2: '25544' },
  orderBy: 'START_TIME',
  order: 'desc',
  limit: 100
});

// Get data from specific station
const stationData = await node.query('TDM', {
  where: { PARTICIPANT_1: 'STATION-01' }
});

// Get RF metrics
const rfData = await node.query('RFM', {
  where: {
    INTERFERENCE_DETECTED: true
  }
});
```

### Orbit Determination Pipeline

```typescript
// Collect observations for orbit determination
const observations = [];

node.subscribe('TDM', (tdm) => {
  for (const obs of tdm.OBSERVATIONS) {
    observations.push({
      time: new Date(obs.EPOCH),
      station: tdm.PARTICIPANT_1,
      object: tdm.PARTICIPANT_2,
      range: obs.RANGE,
      rangeRate: obs.RANGE_RATE,
      azimuth: obs.AZIMUTH,
      elevation: obs.ELEVATION
    });
  }
});

// Process observations periodically
setInterval(() => {
  if (observations.length >= 10) {
    const orbitSolution = fitOrbit(observations);
    console.log('Updated orbit:', orbitSolution);
    observations.length = 0;
  }
}, 60000);
```

### Ground Station Dashboard

```typescript
// Monitor contact reports
node.subscribe('CTR', (ctr, peerId) => {
  console.log(`Contact ${ctr.CONTACT_ID}:`);
  console.log(`  Station: ${ctr.STATION_ID}`);
  console.log(`  Object: ${ctr.OBJECT_ID}`);
  console.log(`  Duration: ${ctr.CONTACT_DURATION}s`);
  console.log(`  Max El: ${ctr.MAX_ELEVATION}deg`);
  console.log(`  Data: ${ctr.DOWNLINK_DATA_VOLUME} bytes`);
  console.log(`  Status: ${ctr.CONTACT_STATUS}`);

  if (ctr.ANOMALIES.length > 0) {
    console.log(`  Anomalies: ${ctr.ANOMALIES.join(', ')}`);
  }
});

// Query contact history
const recentContacts = await node.query('CTR', {
  where: {
    STATION_ID: 'MY-STATION',
    CONTACT_STATUS: 'NOMINAL'
  },
  orderBy: 'AOS',
  order: 'desc',
  limit: 50
});
```

### RF Interference Monitoring

```typescript
// Monitor for interference
node.subscribe('RFM', (rfm, peerId) => {
  if (rfm.INTERFERENCE_DETECTED) {
    console.log('INTERFERENCE ALERT:');
    console.log(`  Object: ${rfm.OBJECT_NAME}`);
    console.log(`  Station: ${rfm.STATION_ID}`);
    console.log(`  Frequency: ${rfm.CENTER_FREQUENCY} MHz`);
    console.log(`  Level: ${rfm.INTERFERENCE_LEVEL} dB`);
    console.log(`  Source: ${rfm.INTERFERENCE_SOURCE || 'Unknown'}`);

    alertOperations(rfm);
  }
});
```

## Data Quality

### Tracking Data Validation

```typescript
import { validateTDM } from '@spacedatanetwork/sdn-js';

node.subscribe('TDM', (tdm) => {
  const validation = validateTDM(tdm);

  if (!validation.valid) {
    console.warn('Invalid TDM:', validation.errors);
    return;
  }

  // Check observation quality
  const goodObs = tdm.OBSERVATIONS.filter(
    obs => obs.DATA_QUALITY === 'NOMINAL'
  );

  console.log(`Good observations: ${goodObs.length}/${tdm.OBSERVATIONS.length}`);
});
```

## See Also

- [Schema Overview](/reference/schemas)
- [Orbital Data](/reference/orbital)
- [Data Operations](/guide/js-data)
