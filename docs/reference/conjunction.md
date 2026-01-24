# Conjunction Data Schemas

Reference for conjunction and collision avoidance data schemas.

## CDM - Conjunction Data Message

The primary schema for space safety - contains close approach predictions between space objects.

### Schema Definition

```flatbuffers
table CDM {
  // CCSDS Header
  CCSDS_CDM_VERS: string;          // Version (e.g., "2.0")
  CREATION_DATE: string;           // Message creation time
  ORIGINATOR: string;              // Issuing organization
  MESSAGE_FOR: string;             // Recipient/operator
  MESSAGE_ID: string;              // Unique message identifier

  // Event Identification
  TCA: string;                     // Time of Closest Approach
  MISS_DISTANCE: double;           // Miss distance at TCA (km)

  // Collision Assessment
  COLLISION_PROBABILITY: double;   // Probability of collision
  COLLISION_PROBABILITY_METHOD: string; // Calculation method

  // Object 1 (Primary/Protected Asset)
  OBJECT1_DESIGNATOR: string;
  OBJECT1_CATALOG_NAME: string;
  OBJECT1_OBJECT_NAME: string;
  OBJECT1_INTERNATIONAL_DESIGNATOR: string;
  OBJECT1_OBJECT_TYPE: string;
  OBJECT1_OPERATOR_CONTACT_POSITION: string;
  OBJECT1_OPERATOR_ORGANIZATION: string;
  OBJECT1_OPERATOR_PHONE: string;
  OBJECT1_OPERATOR_EMAIL: string;

  // Object 1 Orbit State at TCA
  OBJECT1_EPHEMERIS_NAME: string;
  OBJECT1_COVARIANCE_METHOD: string;
  OBJECT1_MANEUVERABLE: string;
  OBJECT1_REF_FRAME: string;
  OBJECT1_X: double;               // Position X (km)
  OBJECT1_Y: double;               // Position Y (km)
  OBJECT1_Z: double;               // Position Z (km)
  OBJECT1_X_DOT: double;           // Velocity X (km/s)
  OBJECT1_Y_DOT: double;           // Velocity Y (km/s)
  OBJECT1_Z_DOT: double;           // Velocity Z (km/s)

  // Object 1 Covariance (RTN frame)
  OBJECT1_CR_R: double;
  OBJECT1_CT_R: double;
  OBJECT1_CT_T: double;
  OBJECT1_CN_R: double;
  OBJECT1_CN_T: double;
  OBJECT1_CN_N: double;
  OBJECT1_CRDOT_R: double;
  OBJECT1_CRDOT_T: double;
  OBJECT1_CRDOT_N: double;
  OBJECT1_CRDOT_RDOT: double;
  // ... additional covariance terms

  // Object 2 (Secondary/Threat Object)
  OBJECT2_DESIGNATOR: string;
  OBJECT2_CATALOG_NAME: string;
  OBJECT2_OBJECT_NAME: string;
  OBJECT2_INTERNATIONAL_DESIGNATOR: string;
  OBJECT2_OBJECT_TYPE: string;
  // ... same fields as Object 1

  // Additional Information
  RELATIVE_SPEED: double;          // Relative velocity (km/s)
  RELATIVE_POSITION_R: double;     // Radial separation (km)
  RELATIVE_POSITION_T: double;     // In-track separation (km)
  RELATIVE_POSITION_N: double;     // Cross-track separation (km)
  RELATIVE_VELOCITY_R: double;     // Radial velocity (km/s)
  RELATIVE_VELOCITY_T: double;     // In-track velocity (km/s)
  RELATIVE_VELOCITY_N: double;     // Cross-track velocity (km/s)

  // Screen information
  SCREEN_VOLUME_FRAME: string;
  SCREEN_VOLUME_SHAPE: string;
  SCREEN_VOLUME_X: double;
  SCREEN_VOLUME_Y: double;
  SCREEN_VOLUME_Z: double;
  SCREEN_ENTRY_TIME: string;
  SCREEN_EXIT_TIME: string;
}
```

### Key Fields Explained

| Field | Description |
|-------|-------------|
| **TCA** | Time of Closest Approach - when objects are nearest |
| **MISS_DISTANCE** | Predicted separation at TCA in kilometers |
| **COLLISION_PROBABILITY** | Likelihood of collision (0.0 to 1.0) |
| **RELATIVE_SPEED** | How fast objects approach each other |
| **OBJECT*_MANEUVERABLE** | Whether object can perform avoidance maneuver |

### Probability Thresholds

| Probability | Risk Level | Typical Action |
|-------------|------------|----------------|
| < 1e-7 | Negligible | Monitor |
| 1e-7 to 1e-5 | Low | Enhanced monitoring |
| 1e-5 to 1e-4 | Elevated | Evaluate maneuver options |
| 1e-4 to 1e-3 | High | Plan avoidance maneuver |
| > 1e-3 | Critical | Execute maneuver |

### Example

```typescript
const cdm = {
  CCSDS_CDM_VERS: "2.0",
  CREATION_DATE: "2024-01-14T18:00:00.000Z",
  ORIGINATOR: "18 SDS",
  MESSAGE_FOR: "OPERATOR",
  MESSAGE_ID: "CDM-12345-67890",

  TCA: "2024-01-15T12:34:56.789Z",
  MISS_DISTANCE: 0.245,
  COLLISION_PROBABILITY: 0.00015,
  COLLISION_PROBABILITY_METHOD: "FOSTER-1992",

  OBJECT1_DESIGNATOR: "25544",
  OBJECT1_OBJECT_NAME: "ISS (ZARYA)",
  OBJECT1_INTERNATIONAL_DESIGNATOR: "1998-067A",
  OBJECT1_OBJECT_TYPE: "PAYLOAD",
  OBJECT1_MANEUVERABLE: "YES",
  OBJECT1_X: 6678.137,
  OBJECT1_Y: 0.0,
  OBJECT1_Z: 0.0,
  OBJECT1_X_DOT: 0.0,
  OBJECT1_Y_DOT: 7.725,
  OBJECT1_Z_DOT: 0.0,

  OBJECT2_DESIGNATOR: "45678",
  OBJECT2_OBJECT_NAME: "DEBRIS",
  OBJECT2_OBJECT_TYPE: "DEBRIS",
  OBJECT2_MANEUVERABLE: "NO",

  RELATIVE_SPEED: 14.2,
  RELATIVE_POSITION_R: 0.1,
  RELATIVE_POSITION_T: 0.2,
  RELATIVE_POSITION_N: 0.05
};
```

---

## CSM - Conjunction Summary Message

Provides a summary overview of conjunction events, typically aggregating multiple CDMs.

### Schema Definition

```flatbuffers
table CSM {
  CCSDS_CSM_VERS: string;
  CREATION_DATE: string;
  ORIGINATOR: string;

  // Summary period
  START_TIME: string;
  END_TIME: string;

  // Statistics
  TOTAL_CONJUNCTIONS: uint32;
  HIGH_RISK_COUNT: uint32;
  MANEUVERS_PLANNED: uint32;

  // Top events
  EVENTS: [ConjunctionEvent];
}

table ConjunctionEvent {
  TCA: string;
  OBJECT1_NAME: string;
  OBJECT2_NAME: string;
  MISS_DISTANCE: double;
  COLLISION_PROBABILITY: double;
  STATUS: string;               // MONITORING, PLANNED_MANEUVER, etc.
}
```

---

## CRM - Collision Risk Message

Provides detailed collision risk assessment with uncertainty quantification.

### Schema Definition

```flatbuffers
table CRM {
  CCSDS_CRM_VERS: string;
  CREATION_DATE: string;
  ORIGINATOR: string;

  // Event reference
  CDM_MESSAGE_ID: string;
  TCA: string;

  // Risk assessment
  COLLISION_PROBABILITY: double;
  PROBABILITY_UPPER_BOUND: double;
  PROBABILITY_LOWER_BOUND: double;
  CONFIDENCE_LEVEL: double;

  // Monte Carlo analysis
  MONTE_CARLO_RUNS: uint32;
  IMPACT_SCENARIOS: [ImpactScenario];

  // Recommendations
  RECOMMENDED_ACTION: string;
  ACTION_DEADLINE: string;
}
```

---

## Usage Patterns

### Monitor Conjunctions

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Subscribe to all conjunction warnings
node.subscribe('CDM', (cdm, peerId) => {
  if (cdm.COLLISION_PROBABILITY > 0.0001) {
    console.log('HIGH RISK CONJUNCTION:');
    console.log(`  Object 1: ${cdm.OBJECT1_OBJECT_NAME}`);
    console.log(`  Object 2: ${cdm.OBJECT2_OBJECT_NAME}`);
    console.log(`  TCA: ${cdm.TCA}`);
    console.log(`  Miss Distance: ${cdm.MISS_DISTANCE} km`);
    console.log(`  Probability: ${cdm.COLLISION_PROBABILITY}`);

    // Trigger alert
    sendAlert(cdm);
  }
});
```

### Query Conjunction History

```typescript
// Get recent high-risk conjunctions
const criticalEvents = await node.query('CDM', {
  where: {
    COLLISION_PROBABILITY: { $gt: 0.0001 },
    TCA: { $gte: new Date(Date.now() - 7 * 24 * 60 * 60 * 1000).toISOString() }
  },
  orderBy: 'COLLISION_PROBABILITY',
  order: 'desc'
});

// Find events involving specific object
const myObjectEvents = await node.query('CDM', {
  where: {
    $or: [
      { OBJECT1_DESIGNATOR: '25544' },
      { OBJECT2_DESIGNATOR: '25544' }
    ]
  },
  orderBy: 'TCA',
  order: 'asc'
});
```

### Risk Dashboard

```typescript
// Aggregate risk by object type
const debrisEvents = await node.query('CDM', {
  where: {
    OBJECT2_OBJECT_TYPE: 'DEBRIS'
  }
});

const rocketBodyEvents = await node.query('CDM', {
  where: {
    OBJECT2_OBJECT_TYPE: 'ROCKET BODY'
  }
});

const payloadEvents = await node.query('CDM', {
  where: {
    OBJECT2_OBJECT_TYPE: 'PAYLOAD'
  }
});

console.log('Conjunctions by secondary object type:');
console.log(`  Debris: ${debrisEvents.length}`);
console.log(`  Rocket Bodies: ${rocketBodyEvents.length}`);
console.log(`  Payloads: ${payloadEvents.length}`);
```

### Visualization

```typescript
// Prepare data for timeline visualization
const events = await node.query('CDM', {
  where: {
    TCA: {
      $gte: new Date().toISOString(),
      $lte: new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString()
    }
  },
  orderBy: 'TCA'
});

const timelineData = events.map(cdm => ({
  time: new Date(cdm.TCA),
  probability: cdm.COLLISION_PROBABILITY,
  missDistance: cdm.MISS_DISTANCE,
  objects: [cdm.OBJECT1_OBJECT_NAME, cdm.OBJECT2_OBJECT_NAME]
}));
```

## Best Practices

### Real-time Alerting

```typescript
// Set up tiered alerting based on probability
node.subscribe('CDM', async (cdm, peerId) => {
  const prob = cdm.COLLISION_PROBABILITY;

  if (prob > 0.001) {
    await sendCriticalAlert(cdm);
  } else if (prob > 0.0001) {
    await sendHighAlert(cdm);
  } else if (prob > 0.00001) {
    await sendModerateAlert(cdm);
  }
});
```

### Data Validation

```typescript
import { validateCDM } from '@spacedatanetwork/sdn-js';

node.subscribe('CDM', (cdm, peerId) => {
  const validation = validateCDM(cdm);

  if (!validation.valid) {
    console.warn('Invalid CDM:', validation.errors);
    return;
  }

  // Process valid CDM
  processCDM(cdm);
});
```

## See Also

- [Schema Overview](/reference/schemas)
- [Orbital Data](/reference/orbital)
- [Data Operations](/guide/js-data)
