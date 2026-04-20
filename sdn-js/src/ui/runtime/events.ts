import type {
  ModuleDeliveryEvent,
  ModuleDeliveryStage,
} from '../../module-delivery';
import type {
  AddressLookupKey,
  AppSectionId,
  CanonicalListing,
  ObservedPeerObservation,
} from './types';

export interface DeliveryTimelineEvent extends ModuleDeliveryEvent {
  stage: ModuleDeliveryStage;
}

export interface RuntimeEventMap {
  'network:peer-observed': ObservedPeerObservation;
  'marketplace:listing-observed': CanonicalListing;
  'delivery:event': DeliveryTimelineEvent;
  'identity:lookup-requested': AddressLookupKey;
  'shell:section-selected': { section: AppSectionId };
}

export type RuntimeEventName = keyof RuntimeEventMap;

type RuntimeEventListener<TEvent extends RuntimeEventName> =
  (payload: RuntimeEventMap[TEvent]) => void;

export class SDNUIEventBus {
  readonly #listeners = new Map<RuntimeEventName, Set<RuntimeEventListener<RuntimeEventName>>>();

  on<TEvent extends RuntimeEventName>(
    eventName: TEvent,
    listener: RuntimeEventListener<TEvent>,
  ): () => void {
    const listeners = this.#listeners.get(eventName) ?? new Set();
    listeners.add(listener as RuntimeEventListener<RuntimeEventName>);
    this.#listeners.set(eventName, listeners);

    return () => {
      listeners.delete(listener as RuntimeEventListener<RuntimeEventName>);
      if (listeners.size === 0) {
        this.#listeners.delete(eventName);
      }
    };
  }

  emit<TEvent extends RuntimeEventName>(
    eventName: TEvent,
    payload: RuntimeEventMap[TEvent],
  ): void {
    for (const listener of this.#listeners.get(eventName) ?? []) {
      listener(payload);
    }
  }

  clear(): void {
    this.#listeners.clear();
  }
}
