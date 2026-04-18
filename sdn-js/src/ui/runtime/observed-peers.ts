import type {
  ObservedPeerObservation,
  ObservedPeerRecord,
  ObservedPeerSource,
} from './types';

interface MutableObservedPeerRecord {
  peerId: string;
  observedAt: number;
  detail?: string;
  sources: Set<ObservedPeerSource>;
}

export class ObservedPeerIndex {
  readonly #peers = new Map<string, MutableObservedPeerRecord>();

  record(observation: ObservedPeerObservation): ObservedPeerRecord {
    const peerId = observation.peerId.trim();
    if (!peerId) {
      throw new Error('peerId is required');
    }

    const observedAt = observation.observedAt ?? Date.now();
    const current = this.#peers.get(peerId) ?? {
      peerId,
      observedAt,
      sources: new Set<ObservedPeerSource>(),
    };

    current.sources.add(observation.source);

    if (observedAt >= current.observedAt) {
      current.observedAt = observedAt;
      if (observation.detail) {
        current.detail = observation.detail;
      }
    } else if (!current.detail && observation.detail) {
      current.detail = observation.detail;
    }

    this.#peers.set(peerId, current);
    return materializeObservedPeerRecord(current);
  }

  get(peerId: string): ObservedPeerRecord | undefined {
    const record = this.#peers.get(peerId.trim());
    return record ? materializeObservedPeerRecord(record) : undefined;
  }

  count(): number {
    return this.#peers.size;
  }

  list(): ObservedPeerRecord[] {
    return [...this.#peers.values()]
      .sort((left, right) => right.observedAt - left.observedAt)
      .map((record) => materializeObservedPeerRecord(record));
  }
}

function materializeObservedPeerRecord(
  record: MutableObservedPeerRecord,
): ObservedPeerRecord {
  return {
    peerId: record.peerId,
    observedAt: record.observedAt,
    detail: record.detail,
    sources: [...record.sources].sort(),
  };
}
