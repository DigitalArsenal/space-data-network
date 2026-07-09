export type EcosystemMode = 'sandbox' | 'live';
export type EcosystemItemKind = 'node' | 'data' | 'module' | 'evidence';
export type EcosystemShape = 'circle' | 'triangle' | 'square' | 'diamond';
export type EcosystemAction =
  | { type: 'create-test-data'; schema: 'OMM' | 'DPM' | 'PNM' | 'EPM' | 'ENC' | 'PLG' | 'CHN' }
  | { type: 'subscribe-channel'; sourceId: string; standardCode: string }
  | { type: 'pin-product'; targetId: string }
  | { type: 'create-module-listing'; name: string; moduleId: string; inputSchema: string; outputSchema: string }
  | { type: 'simulate-module-invocation'; moduleId: string }
  | { type: 'set-mode'; mode: EcosystemMode }
  | { type: 'select-item'; itemId: string };

export interface EcosystemItem {
  id: string;
  kind: EcosystemItemKind;
  shape: EcosystemShape;
  title: string;
  description: string;
  status?: string;
}

export interface EcosystemEdge {
  id: string;
  from: string;
  to: string;
  label: string;
  style: 'solid' | 'dashed';
}

export interface EcosystemEvent {
  type:
    | 'data-created'
    | 'channel-subscribed'
    | 'artifact-pinned'
    | 'module-listed'
    | 'module-invoked'
    | 'live-mode-requested'
    | 'item-selected';
  title: string;
  detail: string;
}

export interface EcosystemState {
  mode: EcosystemMode;
  selectedItemId: string;
  items: EcosystemItem[];
  edges: EcosystemEdge[];
  events: EcosystemEvent[];
  live: {
    explicitlyRequested: boolean;
    connections: Array<{ id: string; label: string; status: 'unavailable' | 'connected'; detail: string }>;
  };
  pins: Array<{ targetId: string; durable: boolean; label: string }>;
  subscriptions: Array<{ sourceId: string; standardCode: string; topic: string }>;
  moduleListings: Array<{ name: string; moduleId: string; inputSchema: string; outputSchema: string; signed: boolean }>;
  invocations: Array<{ moduleId: string; inputSchema: string; outputSchema: string; status: 'simulated' }>;
}

export const ecosystemShapeLegend = [
  {
    shape: 'circle' as const,
    label: 'Node',
    description: 'SDN full node, browser peer, provider, pinning node, archive node, or consumer.',
  },
  {
    shape: 'triangle' as const,
    label: 'Data',
    description: 'SDS record, data product, PNM announcement, or DPM product manifest.',
  },
  {
    shape: 'square' as const,
    label: 'Module',
    description: 'SDN module bundle, module provider artifact, or schema-bound computation.',
  },
  {
    shape: 'diamond' as const,
    label: 'Verification',
    description: 'Signature, CID, grant, wrapped key, PLG listing, or pin proof.',
  },
];

const initialItems: EcosystemItem[] = [
  {
    id: 'node-browser',
    kind: 'node',
    shape: 'circle',
    title: 'Browser SDN Node',
    description: 'Ephemeral sandbox browser node that creates, signs, subscribes, and pins demo artifacts.',
  },
  {
    id: 'node-celestrak',
    kind: 'node',
    shape: 'circle',
    title: 'celestrak.eth',
    description: 'OMM provider node in the architecture. Sandbox mode models latest-product publication.',
  },
  {
    id: 'node-spaceaware',
    kind: 'node',
    shape: 'circle',
    title: 'sdn.spaceaware.io',
    description: 'Module provider node for signed and encrypted module delivery.',
  },
  {
    id: 'node-pinning',
    kind: 'node',
    shape: 'circle',
    title: 'Core Pinning Node',
    description: 'Network node that keeps selected artifacts available by CID.',
  },
  {
    id: 'node-archive',
    kind: 'node',
    shape: 'circle',
    title: 'Archive Node',
    description: 'Long-lived archive/query node for historical CelesTrak products.',
  },
  {
    id: 'node-consumer',
    kind: 'node',
    shape: 'circle',
    title: 'Consumer Node',
    description: 'Analyst or application node that subscribes, verifies, and uses artifacts.',
  },
  {
    id: 'data-omm',
    kind: 'data',
    shape: 'triangle',
    title: 'OMM Record',
    description: 'SDS orbital mean elements data from a provider.',
  },
  {
    id: 'data-dpm',
    kind: 'data',
    shape: 'triangle',
    title: 'DPM Product',
    description: 'Current authoritative product manifest for a provider record set.',
  },
  {
    id: 'data-pnm',
    kind: 'data',
    shape: 'triangle',
    title: 'PNM Announcement',
    description: 'Signed publish notification that points subscribers to the current artifact CID.',
  },
  {
    id: 'module-sgp4',
    kind: 'module',
    shape: 'square',
    title: 'SGP4 Propagator Module',
    description: 'Module requested by ID and provider peer, then decrypted locally after grant exchange.',
  },
  {
    id: 'badge-plg',
    kind: 'evidence',
    shape: 'diamond',
    title: 'PLG Listing',
    description: 'Signed module marketplace/listing record for one module version.',
  },
  {
    id: 'badge-enc',
    kind: 'evidence',
    shape: 'diamond',
    title: 'ENC / Grant',
    description: 'Encrypted artifact or wrapped content-key evidence.',
  },
];

const initialEdges: EcosystemEdge[] = [
  {
    id: 'edge-omm-dpm',
    from: 'node-celestrak',
    to: 'data-dpm',
    label: 'publishes latest DPM',
    style: 'solid',
  },
  {
    id: 'edge-pnm-subscribe',
    from: 'data-pnm',
    to: 'node-consumer',
    label: 'subscription notice',
    style: 'dashed',
  },
  {
    id: 'edge-module-listing',
    from: 'node-spaceaware',
    to: 'badge-plg',
    label: 'signed PLG',
    style: 'solid',
  },
  {
    id: 'edge-module-delivery',
    from: 'badge-plg',
    to: 'module-sgp4',
    label: 'module ID + provider peer',
    style: 'solid',
  },
  {
    id: 'edge-pin',
    from: 'data-dpm',
    to: 'node-pinning',
    label: 'pin by CID',
    style: 'solid',
  },
];

export function createInitialEcosystemState(): EcosystemState {
  return {
    mode: 'sandbox',
    selectedItemId: 'node-browser',
    items: initialItems.map((item) => ({ ...item })),
    edges: initialEdges.map((edge) => ({ ...edge })),
    events: [],
    pins: [],
    subscriptions: [],
    moduleListings: [],
    invocations: [],
    live: {
      explicitlyRequested: false,
      connections: [
        {
          id: 'sdn.spaceaware.io',
          label: 'sdn.spaceaware.io',
          status: 'unavailable',
          detail: 'Live connection has not been requested.',
        },
        {
          id: 'celestrak.eth',
          label: 'celestrak.eth',
          status: 'unavailable',
          detail: 'Live connection has not been requested.',
        },
      ],
    },
  };
}

export function runEcosystemAction(state: EcosystemState, action: EcosystemAction): EcosystemState {
  if (action.type === 'create-test-data') {
    return appendEvent(state, {
      type: 'data-created',
      title: `${action.schema} sandbox data created`,
      detail: `${action.schema} data is ready for signing in sandbox mode.`,
    });
  }

  if (action.type === 'subscribe-channel') {
    const topic = `/spacedatanetwork/channels/${action.standardCode}`;

    return appendEvent(
      {
        ...state,
        subscriptions: [...state.subscriptions, { sourceId: action.sourceId, standardCode: action.standardCode, topic }],
      },
      {
        type: 'channel-subscribed',
        title: `${action.sourceId} subscribed`,
        detail: `Subscribed to ${topic}.`,
      },
    );
  }

  if (action.type === 'pin-product') {
    return appendEvent(
      {
        ...state,
        pins: [...state.pins, { targetId: action.targetId, durable: false, label: 'Sandbox pin' }],
      },
      {
        type: 'artifact-pinned',
        title: 'Sandbox pin recorded',
        detail: `${action.targetId} is pinned in browser sandbox state.`,
      },
    );
  }

  if (action.type === 'create-module-listing') {
    return appendEvent(
      {
        ...state,
        moduleListings: [
          ...state.moduleListings,
          {
            name: action.name,
            moduleId: action.moduleId,
            inputSchema: action.inputSchema,
            outputSchema: action.outputSchema,
            signed: true,
          },
        ],
      },
      {
        type: 'module-listed',
        title: `${action.name} listed`,
        detail: `${action.moduleId} accepts ${action.inputSchema} and emits ${action.outputSchema}.`,
      },
    );
  }

  if (action.type === 'simulate-module-invocation') {
    const listing = state.moduleListings.find((candidate) => candidate.moduleId === action.moduleId) ?? {
      moduleId: action.moduleId,
      inputSchema: 'OMM',
      outputSchema: 'OEM',
    };

    return appendEvent(
      {
        ...state,
        invocations: [
          ...state.invocations,
          {
            moduleId: listing.moduleId,
            inputSchema: listing.inputSchema,
            outputSchema: listing.outputSchema,
            status: 'simulated',
          },
        ],
      },
      {
        type: 'module-invoked',
        title: `${listing.moduleId} simulated`,
        detail: `Sandbox invocation transformed ${listing.inputSchema} input into ${listing.outputSchema} output metadata.`,
      },
    );
  }

  if (action.type === 'set-mode') {
    return appendEvent(
      {
        ...state,
        mode: action.mode,
        live: {
          ...state.live,
          explicitlyRequested: action.mode === 'live',
          connections: state.live.connections.map((connection) => ({
            ...connection,
            status: 'unavailable',
          })),
        },
      },
      {
        type: 'live-mode-requested',
        title: `${action.mode} mode selected`,
        detail:
          action.mode === 'live'
            ? 'Live mode is explicit; connections remain unavailable until wired.'
            : 'Sandbox mode restored.',
      },
    );
  }

  const selected = selectEcosystemItem(state, action.itemId);
  if (selected === state) {
    return state;
  }

  const selectedItem = selected.items.find((item) => item.id === action.itemId);
  return appendEvent(selected, {
    type: 'item-selected',
    title: selectedItem?.title ?? action.itemId,
    detail: `${selectedItem?.title ?? action.itemId} selected.`,
  });
}

export function selectEcosystemItem(state: EcosystemState, selectedItemId: string): EcosystemState {
  if (!state.items.some((item) => item.id === selectedItemId)) {
    return state;
  }

  return { ...state, selectedItemId };
}

function appendEvent(state: EcosystemState, event: EcosystemEvent): EcosystemState {
  return { ...state, events: [...state.events, event] };
}
