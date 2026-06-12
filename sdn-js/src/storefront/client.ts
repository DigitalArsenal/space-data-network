/**
 * Storefront client for Space Data Network
 *
 * Provides methods for interacting with the SDN marketplace:
 * - Browse and search listings
 * - Purchase data access
 * - Manage subscriptions
 * - Post reviews
 */

import type {
  Listing,
  AccessGrant,
  PurchaseRequest,
  Review,
  ReviewStats,
  SearchQuery,
  SearchResult,
  CreditsBalance,
  CreateListingRequest,
  CreatePurchaseRequest,
  CreateReviewRequest,
  PurchaseStatus,
  PaymentMethod,
  FiatGatewayRequest,
  FiatGatewayResult,
  CreditsTransaction,
  SellerDashboard,
  BuyerDashboard,
  TrustScore,
  ManualDevPaymentConfirmation,
  ManualDevPaymentResult,
  PaymentAuditEvent,
  CreateCryptoIntentRequest,
  CryptoBuyerIntent,
  SubmitCryptoPaymentRequest,
  UsageSummary,
  Invoice,
} from './types';

export interface DeliveryTopicSubscription {
  unsubscribe?: () => void | Promise<void>;
}

export type DeliveryTopicMessage =
  | Uint8Array
  | ArrayBuffer
  | { data?: Uint8Array | ArrayBuffer; payload?: Uint8Array | ArrayBuffer };

export interface StorefrontPubSub {
  subscribe(
    topic: string,
    handler: (message: DeliveryTopicMessage) => void | Promise<void>,
  ): void | DeliveryTopicSubscription | Promise<void | DeliveryTopicSubscription>;
}

export interface StorefrontLibp2pPubSub {
  subscribe(topic: string): void | Promise<void>;
  unsubscribe?(topic: string): void | Promise<void>;
  addEventListener(
    type: 'message',
    listener: (event: unknown) => void,
    options?: { signal?: AbortSignal },
  ): void;
  removeEventListener?(type: 'message', listener: (event: unknown) => void): void;
}

export interface DeliverySubscription {
  grantId: string;
  topic: string;
  unsubscribe: () => Promise<void>;
}

/** Storefront client configuration */
export interface StorefrontClientConfig {
  /** API base URL for server-side operations */
  apiBaseUrl?: string;
  /** PubSub instance for real-time updates */
  pubsub?: StorefrontPubSub;
  /** Peer ID for this client */
  peerId: string;
  /** Signing function for requests */
  sign?: (data: Uint8Array) => Promise<Uint8Array>;
  /** Encryption public key for receiving data */
  encryptionPubkey?: Uint8Array;
  /** Key algorithm (x25519, secp256k1, p256) */
  keyAlgorithm?: string;
}

/** Storefront events */
export interface StorefrontEvents {
  'listing:new': Listing;
  'listing:updated': Listing;
  'purchase:status': { requestId: string; status: PurchaseStatus };
  'grant:issued': AccessGrant;
  'data:received': { grantId: string; data: Uint8Array };
  'data:subscribed': { grantId: string; topic: string };
}

/** Event handler type */
type EventHandler<T> = (event: T) => void;

/**
 * Storefront client for interacting with SDN marketplace
 */
export class StorefrontClient {
  private config: StorefrontClientConfig;
  private eventHandlers: Map<string, Set<EventHandler<unknown>>> = new Map();

  constructor(config: StorefrontClientConfig) {
    this.config = {
      ...config,
      apiBaseUrl: normalizeStorefrontAPIBaseUrl(config.apiBaseUrl),
    };
  }

  /**
   * Search for listings
   */
  async searchListings(query: SearchQuery): Promise<SearchResult> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/listings/search`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(query),
      });
      if (!response.ok) {
        throw new Error(`Search failed: ${response.statusText}`);
      }
      return response.json();
    }

    // Local/P2P search would go here
    throw new Error('API URL required for search');
  }

  /**
   * Get a listing by ID
   */
  async getListing(listingId: string): Promise<Listing | null> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/listings/${listingId}`);
      if (response.status === 404) {
        return null;
      }
      if (!response.ok) {
        throw new Error(`Failed to get listing: ${response.statusText}`);
      }
      return response.json();
    }

    throw new Error('API URL required');
  }

  /**
   * Create a new listing (for providers)
   */
  async createListing(request: CreateListingRequest): Promise<Listing> {
    if (!this.config.sign) {
      throw new Error('Signing function required to create listings');
    }

    const listing: Partial<Listing> = {
      ...request,
      providerPeerId: this.config.peerId,
      active: true,
      version: 1,
    };

    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/listings`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(listing),
      });
      if (!response.ok) {
        throw new Error(`Failed to create listing: ${response.statusText}`);
      }
      return response.json();
    }

    throw new Error('API URL required');
  }

  /**
   * Update a listing
   */
  async updateListing(listingId: string, updates: Partial<CreateListingRequest>): Promise<Listing> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/listings/${listingId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(updates),
      });
      if (!response.ok) {
        throw new Error(`Failed to update listing: ${response.statusText}`);
      }
      return response.json();
    }

    throw new Error('API URL required');
  }

  /**
   * Deactivate a listing
   */
  async deactivateListing(listingId: string): Promise<void> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/listings/${listingId}`, {
        method: 'DELETE',
      });
      if (!response.ok) {
        throw new Error(`Failed to deactivate listing: ${response.statusText}`);
      }
      return;
    }

    throw new Error('API URL required');
  }

  /**
   * Create a purchase request
   */
  async createPurchase(request: CreatePurchaseRequest): Promise<PurchaseRequest> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/purchases`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          listing_id: request.listingId,
          tier_name: request.tierName,
          buyer_peer_id: this.config.peerId,
          buyer_encryption_pubkey: bytesToBase64(request.encryptionPubkey || this.config.encryptionPubkey),
          key_algorithm: request.keyAlgorithm || this.config.keyAlgorithm,
          payment_method: request.paymentMethod,
          preferred_delivery_method: request.preferredDeliveryMethod,
          webhook_url: request.webhookUrl,
        }),
      });
      if (!response.ok) {
        throw new Error(`Failed to create purchase: ${response.statusText}`);
      }
      return normalizePurchaseRequest(await response.json());
    }

    throw new Error('API URL required');
  }

  /**
   * Confirm a crypto payment
   */
  async confirmCryptoPayment(requestId: string, txHash: string, chain: string): Promise<void> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/purchases/${requestId}/confirm`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ txHash, chain }),
      });
      if (!response.ok) {
        throw new Error(`Failed to confirm payment: ${response.statusText}`);
      }
      return;
    }

    throw new Error('API URL required');
  }

  /**
   * Create a server-authored crypto buyer intent for a purchase.
   */
  async createCryptoBuyerIntent(requestId: string, request: CreateCryptoIntentRequest): Promise<CryptoBuyerIntent> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/purchases/${requestId}/pay-crypto`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          chain: request.chain,
          asset: request.asset,
          asset_contract: request.assetContract,
          native_asset: request.nativeAsset,
          recipient: request.recipient,
          method: request.method,
          expires_at: request.expiresAt?.toISOString(),
        }),
      });
      if (!response.ok) {
        throw new Error(`Failed to create crypto payment intent: ${response.statusText}`);
      }
      return normalizeCryptoBuyerIntent(await response.json());
    }

    throw new Error('API URL required');
  }

  /**
   * Submit a crypto transaction reference against a server-created buyer intent.
   */
  async submitCryptoPayment(requestId: string, request: SubmitCryptoPaymentRequest): Promise<void> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/purchases/${requestId}/confirm`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          txHash: request.txHash,
          chain: request.chain,
          reference: request.reference,
          recipientAddress: request.recipientAddress,
          amount: request.amount,
          currency: request.currency,
          assetContract: request.assetContract,
          nativeAsset: request.nativeAsset,
          senderAddress: request.senderAddress,
        }),
      });
      if (!response.ok) {
        throw new Error(`Failed to submit crypto payment: ${response.statusText}`);
      }
      return;
    }

    throw new Error('API URL required');
  }

  /**
   * Pay with SDN credits
   */
  async payWithCredits(requestId: string): Promise<AccessGrant> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/purchases/${requestId}/pay-credits`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
      });
      if (!response.ok) {
        throw new Error(`Failed to pay with credits: ${response.statusText}`);
      }
      return normalizeAccessGrant(await response.json());
    }

    throw new Error('API URL required');
  }

  /**
   * Get purchase status
   */
  async getPurchaseStatus(requestId: string): Promise<PurchaseRequest | null> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/purchases/${requestId}`);
      if (response.status === 404) {
        return null;
      }
      if (!response.ok) {
        throw new Error(`Failed to get purchase: ${response.statusText}`);
      }
      return normalizePurchaseRequest(await response.json());
    }

    throw new Error('API URL required');
  }

  /**
   * Get access grants for the current buyer
   */
  async getMyGrants(): Promise<AccessGrant[]> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/grants?buyer=${this.config.peerId}`);
      if (!response.ok) {
        throw new Error(`Failed to get grants: ${response.statusText}`);
      }
      const payload = await response.json();
      return Array.isArray(payload) ? payload.map(normalizeAccessGrant) : [];
    }

    throw new Error('API URL required');
  }

  /**
   * Get a specific grant
   */
  async getGrant(grantId: string): Promise<AccessGrant | null> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/grants/${grantId}`);
      if (response.status === 404) {
        return null;
      }
      if (!response.ok) {
        throw new Error(`Failed to get grant: ${response.statusText}`);
      }
      return normalizeAccessGrant(await response.json());
    }

    throw new Error('API URL required');
  }

  /**
   * Create a review
   */
  async createReview(request: CreateReviewRequest): Promise<Review> {
    const review: Partial<Review> = {
      ...request,
      reviewerPeerId: this.config.peerId,
    };

    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/reviews`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(review),
      });
      if (!response.ok) {
        throw new Error(`Failed to create review: ${response.statusText}`);
      }
      return response.json();
    }

    throw new Error('API URL required');
  }

  /**
   * Get reviews for a listing
   */
  async getListingReviews(listingId: string, limit = 20, offset = 0): Promise<{ reviews: Review[]; stats: ReviewStats }> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(
        `${this.config.apiBaseUrl}/storefront/listings/${listingId}/reviews?limit=${limit}&offset=${offset}`
      );
      if (!response.ok) {
        throw new Error(`Failed to get reviews: ${response.statusText}`);
      }
      return response.json();
    }

    throw new Error('API URL required');
  }

  /**
   * Vote on a review's helpfulness
   */
  async voteReview(reviewId: string, helpful: boolean): Promise<void> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/reviews/${reviewId}/vote`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ helpful }),
      });
      if (!response.ok) {
        throw new Error(`Failed to vote on review: ${response.statusText}`);
      }
      return;
    }

    throw new Error('API URL required');
  }

  /**
   * Get credits balance
   */
  async getCreditsBalance(): Promise<CreditsBalance> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/credits/${this.config.peerId}`);
      if (!response.ok) {
        throw new Error(`Failed to get credits balance: ${response.statusText}`);
      }
      return response.json();
    }

    throw new Error('API URL required');
  }

  /**
   * Purchase credits (returns payment intent or address)
   */
  async purchaseCredits(amount: number, paymentMethod: PaymentMethod): Promise<{ paymentTarget: string }> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/credits/purchase`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ amount, paymentMethod, peerId: this.config.peerId }),
      });
      if (!response.ok) {
        throw new Error(`Failed to initiate credits purchase: ${response.statusText}`);
      }
      return response.json();
    }

    throw new Error('API URL required');
  }

  /**
   * Subscribe to real-time events
   */
  on<K extends keyof StorefrontEvents>(event: K, handler: EventHandler<StorefrontEvents[K]>): void {
    if (!this.eventHandlers.has(event)) {
      this.eventHandlers.set(event, new Set());
    }
    this.eventHandlers.get(event)!.add(handler as EventHandler<unknown>);
  }

  /**
   * Unsubscribe from events
   */
  off<K extends keyof StorefrontEvents>(event: K, handler: EventHandler<StorefrontEvents[K]>): void {
    const handlers = this.eventHandlers.get(event);
    if (handlers) {
      handlers.delete(handler as EventHandler<unknown>);
    }
  }

  // --- 14.4 Payment Integration ---

  /**
   * Initiate a fiat payment via Stripe gateway
   */
  async createFiatPayment(requestId: string, req: FiatGatewayRequest): Promise<FiatGatewayResult> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/purchases/${requestId}/pay-fiat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(req),
      });
      if (!response.ok) {
        throw new Error(`Failed to create fiat payment: ${response.statusText}`);
      }
      return response.json();
    }
    throw new Error('API URL required');
  }

  /**
   * Complete a purchase with an explicit manual/dev paid state.
   */
  async completeManualDevPayment(
    requestId: string,
    confirmation: ManualDevPaymentConfirmation = {},
  ): Promise<ManualDevPaymentResult> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/purchases/${requestId}/manual-dev-paid`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          operator_peer_id: confirmation.operatorPeerId,
          reference: confirmation.reference,
          note: confirmation.note,
        }),
      });
      if (!response.ok) {
        throw new Error(`Failed to complete manual/dev payment: ${response.statusText}`);
      }
      const payload = await response.json() as { purchase?: unknown; grant?: unknown };
      return {
        mode: 'manual-dev',
        purchase: normalizePurchaseRequest(payload.purchase),
        grant: normalizeAccessGrant(payload.grant),
      };
    }
    throw new Error('API URL required');
  }

  /**
   * Get payment/grant audit history for a purchase.
   */
  async getPurchaseAudit(requestId: string): Promise<PaymentAuditEvent[]> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/purchases/${requestId}/audit`);
      if (!response.ok) {
        throw new Error(`Failed to get purchase audit: ${response.statusText}`);
      }
      const payload = await response.json() as { events?: unknown[] };
      return Array.isArray(payload.events) ? payload.events.map(normalizePaymentAuditEvent) : [];
    }
    throw new Error('API URL required');
  }

  /**
   * Get credits transaction history
   */
  async getCreditsTransactions(limit = 50, offset = 0): Promise<CreditsTransaction[]> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(
        `${this.config.apiBaseUrl}/storefront/credits/${this.config.peerId}/transactions?limit=${limit}&offset=${offset}`
      );
      if (!response.ok) {
        throw new Error(`Failed to get transactions: ${response.statusText}`);
      }
      return response.json();
    }
    throw new Error('API URL required');
  }

  // --- 14.5 Data Delivery ---

  /**
   * Subscribe to a data delivery stream for a grant
   */
  async subscribeToDelivery(grantId: string): Promise<DeliverySubscription> {
    // Connect to the PubSub topic for this grant's delivery
    // Topic format: /sdn/data/{listing_id}/{buyer_peer_id}
    const grant = await this.getGrant(grantId);
    if (!grant) {
      throw new Error('Grant not found');
    }
    if (!grant.deliveryTopic) {
      throw new Error('Grant does not include a delivery topic');
    }
    if (!this.config.pubsub?.subscribe) {
      throw new Error('PubSub adapter required for delivery subscription');
    }
    const topic = grant.deliveryTopic;
    const subscription = await this.config.pubsub.subscribe(topic, (message) => {
      this.emit('data:received', { grantId, data: normalizeDeliveryTopicMessage(message) });
    });
    this.emit('data:subscribed', { grantId, topic });
    return {
      grantId,
      topic,
      unsubscribe: async () => {
        await subscription?.unsubscribe?.();
      },
    };
  }

  // --- 14.6 Dashboard APIs ---

  /**
   * Get the seller dashboard data
   */
  async getSellerDashboard(): Promise<SellerDashboard> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(
        `${this.config.apiBaseUrl}/storefront/dashboard/seller?peerId=${this.config.peerId}`
      );
      if (!response.ok) {
        throw new Error(`Failed to get seller dashboard: ${response.statusText}`);
      }
      return response.json();
    }
    throw new Error('API URL required');
  }

  /**
   * Get the buyer dashboard data
   */
  async getBuyerDashboard(): Promise<BuyerDashboard> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(
        `${this.config.apiBaseUrl}/storefront/dashboard/buyer?peerId=${this.config.peerId}`
      );
      if (!response.ok) {
        throw new Error(`Failed to get buyer dashboard: ${response.statusText}`);
      }
      return response.json();
    }
    throw new Error('API URL required');
  }

  /**
   * Respond to a review (as a provider)
   */
  async respondToReview(reviewId: string, response: string): Promise<void> {
    if (this.config.apiBaseUrl) {
      const res = await fetch(`${this.config.apiBaseUrl}/storefront/reviews/${reviewId}/respond`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ response }),
      });
      if (!res.ok) {
        throw new Error(`Failed to respond to review: ${res.statusText}`);
      }
      return;
    }
    throw new Error('API URL required');
  }

  // --- 14.7 Trust and Reputation ---

  /**
   * Get provider trust score
   */
  async getProviderTrust(peerId: string): Promise<TrustScore> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/trust/${peerId}`);
      if (!response.ok) {
        throw new Error(`Failed to get trust score: ${response.statusText}`);
      }
      return response.json();
    }
    throw new Error('API URL required');
  }

  // --- Usage-based billing and invoicing ---

  /**
   * Get usage summary for a listing/buyer over a date range.
   * GET /storefront/usage/{listingId}/summary?buyer={peerId}&from={from}&to={to}
   */
  async getUsageSummary(listingId: string, from: string, to: string): Promise<UsageSummary> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(
        `${this.config.apiBaseUrl}/storefront/usage/${listingId}/summary?buyer=${this.config.peerId}&from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`
      );
      if (!response.ok) {
        throw new Error(`Failed to get usage summary: ${response.statusText}`);
      }
      return normalizeUsageSummary(await response.json());
    }
    throw new Error('API URL required');
  }

  /**
   * List invoices for the authenticated buyer.
   * GET /storefront/invoices?buyer={peerId}&limit={limit}&offset={offset}
   */
  async getMyInvoices(limit = 20, offset = 0): Promise<Invoice[]> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(
        `${this.config.apiBaseUrl}/storefront/invoices?buyer=${this.config.peerId}&limit=${limit}&offset=${offset}`
      );
      if (!response.ok) {
        throw new Error(`Failed to get invoices: ${response.statusText}`);
      }
      const payload = await response.json();
      return Array.isArray(payload) ? payload.map(normalizeInvoice) : [];
    }
    throw new Error('API URL required');
  }

  /**
   * Get a single invoice by ID.
   * GET /storefront/invoices/{invoiceId}
   */
  async getInvoice(invoiceId: string): Promise<Invoice | null> {
    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/invoices/${invoiceId}`);
      if (response.status === 404) {
        return null;
      }
      if (!response.ok) {
        throw new Error(`Failed to get invoice: ${response.statusText}`);
      }
      return normalizeInvoice(await response.json());
    }
    throw new Error('API URL required');
  }

  // --- Event system ---

  /**
   * Start listening for PubSub messages
   */
  async startListening(): Promise<void> {
    // Subscribe to PubSub topics for real-time updates
    // Topics: /sdn/storefront/listings, /sdn/storefront/purchases
    this.emit('listening:started', {});
  }

  /**
   * Stop listening
   */
  async stopListening(): Promise<void> {
    this.emit('listening:stopped', {});
  }

  /**
   * Emit an event to registered handlers
   */
  private emit(event: string, data: unknown): void {
    const handlers = this.eventHandlers.get(event);
    if (handlers) {
      for (const handler of handlers) {
        try {
          handler(data);
        } catch (err) {
          // Swallow handler errors
        }
      }
    }
  }
}

/**
 * Create a storefront client
 */
export function createStorefrontClient(config: StorefrontClientConfig): StorefrontClient {
  return new StorefrontClient(config);
}

export function createStorefrontLibp2pPubSubAdapter(pubsub: StorefrontLibp2pPubSub): StorefrontPubSub {
  return {
    async subscribe(topic: string, handler: (message: DeliveryTopicMessage) => void | Promise<void>) {
      await pubsub.subscribe(topic);
      const controller = new AbortController();
      const listener = (event: unknown) => {
        const detail = eventDetail(event);
        if (!detail || detail.topic !== topic) {
          return;
        }
        const data = deliveryDataFromEventDetail(detail);
        if (!data) {
          return;
        }
        void Promise.resolve(handler(data));
      };
      pubsub.addEventListener('message', listener, { signal: controller.signal });
      return {
        unsubscribe: async () => {
          controller.abort();
          pubsub.removeEventListener?.('message', listener);
          await pubsub.unsubscribe?.(topic);
        },
      };
    },
  };
}

function normalizeStorefrontAPIBaseUrl(apiBaseUrl: string | undefined): string | undefined {
  if (!apiBaseUrl) {
    return undefined;
  }

  const trimmed = apiBaseUrl.replace(/\/+$/u, '');
  if (trimmed.endsWith('/api')) {
    return trimmed;
  }
  return `${trimmed}/api`;
}

function normalizeDeliveryTopicMessage(message: DeliveryTopicMessage): Uint8Array {
  if (message instanceof Uint8Array) {
    return message;
  }
  if (message instanceof ArrayBuffer) {
    return new Uint8Array(message);
  }
  const payload = message?.data ?? message?.payload;
  if (payload instanceof Uint8Array) {
    return payload;
  }
  if (payload instanceof ArrayBuffer) {
    return new Uint8Array(payload);
  }
  throw new TypeError('Delivery topic message must contain Uint8Array data.');
}

function eventDetail(event: unknown): { topic?: unknown; data?: unknown; payload?: unknown } | undefined {
  if (!isRecord(event)) {
    return undefined;
  }
  const detail = event.detail;
  return isRecord(detail) ? detail : undefined;
}

function deliveryDataFromEventDetail(detail: { data?: unknown; payload?: unknown }): DeliveryTopicMessage | undefined {
  if (isDeliveryTopicMessage(detail.data)) {
    return detail.data;
  }
  if (isDeliveryTopicMessage(detail.payload)) {
    return detail.payload;
  }
  return undefined;
}

function isDeliveryTopicMessage(value: unknown): value is DeliveryTopicMessage {
  if (value instanceof Uint8Array || value instanceof ArrayBuffer) {
    return true;
  }
  return isRecord(value) && (isDeliveryTopicMessage(value.data) || isDeliveryTopicMessage(value.payload));
}

function normalizePaymentAuditEvent(value: unknown): PaymentAuditEvent {
  const record = isRecord(value) ? value : {};
  return {
    eventId: stringField(record, 'event_id') || stringField(record, 'eventId') || '',
    requestId: stringField(record, 'request_id') || stringField(record, 'requestId') || '',
    eventType: stringField(record, 'event_type') || stringField(record, 'eventType') || '',
    actorPeerId: stringField(record, 'actor_peer_id') || stringField(record, 'actorPeerId'),
    reference: stringField(record, 'reference'),
    message: stringField(record, 'message'),
    purchaseStatus: numberField(record, 'purchase_status') ?? numberField(record, 'purchaseStatus') ?? 0,
    createdAt: dateField(record, 'created_at') ?? dateField(record, 'createdAt'),
  };
}

function normalizePurchaseRequest(value: unknown): PurchaseRequest {
  const record = isRecord(value) ? value : {};
  return {
    requestId: stringField(record, 'request_id') || stringField(record, 'requestId') || '',
    listingId: stringField(record, 'listing_id') || stringField(record, 'listingId') || '',
    tierName: stringField(record, 'tier_name') || stringField(record, 'tierName') || '',
    buyerPeerId: stringField(record, 'buyer_peer_id') || stringField(record, 'buyerPeerId') || '',
    buyerEncryptionPubkey:
      bytesField(record, 'buyer_encryption_pubkey') ?? bytesField(record, 'buyerEncryptionPubkey'),
    keyAlgorithm: stringField(record, 'key_algorithm') || stringField(record, 'keyAlgorithm'),
    buyerEmail: stringField(record, 'buyer_email') || stringField(record, 'buyerEmail'),
    paymentMethod: (numberField(record, 'payment_method') ?? numberField(record, 'paymentMethod') ?? 0) as PaymentMethod,
    paymentAmount: numberField(record, 'payment_amount') ?? numberField(record, 'paymentAmount') ?? 0,
    paymentCurrency: stringField(record, 'payment_currency') || stringField(record, 'paymentCurrency') || '',
    paymentTxHash: stringField(record, 'payment_tx_hash') || stringField(record, 'paymentTxHash'),
    paymentChain: stringField(record, 'payment_chain') || stringField(record, 'paymentChain'),
    senderAddress: stringField(record, 'sender_address') || stringField(record, 'senderAddress'),
    confirmationBlock: numberField(record, 'confirmation_block') ?? numberField(record, 'confirmationBlock'),
    paymentIntentId: stringField(record, 'payment_intent_id') || stringField(record, 'paymentIntentId'),
    creditsTransactionId: stringField(record, 'credits_transaction_id') || stringField(record, 'creditsTransactionId'),
    status: (numberField(record, 'status') ?? 0) as PurchaseStatus,
    statusMessage: stringField(record, 'status_message') || stringField(record, 'statusMessage'),
    createdAt: dateField(record, 'created_at') ?? dateField(record, 'createdAt') ?? new Date(0),
    updatedAt: dateField(record, 'updated_at') ?? dateField(record, 'updatedAt') ?? new Date(0),
    paymentDeadline: dateField(record, 'payment_deadline') ?? dateField(record, 'paymentDeadline'),
    paymentConfirmedAt: dateField(record, 'payment_confirmed_at') ?? dateField(record, 'paymentConfirmedAt'),
    grantIssuedAt: dateField(record, 'grant_issued_at') ?? dateField(record, 'grantIssuedAt'),
    grantId: stringField(record, 'grant_id') || stringField(record, 'grantId'),
    providerPeerId: stringField(record, 'provider_peer_id') || stringField(record, 'providerPeerId'),
    providerAcknowledgedAt: dateField(record, 'provider_acknowledged_at') ?? dateField(record, 'providerAcknowledgedAt'),
    preferredDeliveryMethod: (
      stringField(record, 'preferred_delivery_method') || stringField(record, 'preferredDeliveryMethod')
    ) as PurchaseRequest['preferredDeliveryMethod'],
    webhookUrl: stringField(record, 'webhook_url') || stringField(record, 'webhookUrl'),
    buyerSignature: bytesField(record, 'buyer_signature') ?? bytesField(record, 'buyerSignature'),
    providerSignature: bytesField(record, 'provider_signature') ?? bytesField(record, 'providerSignature'),
  };
}

function normalizeAccessGrant(value: unknown): AccessGrant {
  const record = isRecord(value) ? value : {};
  return {
    grantId: stringField(record, 'grant_id') || stringField(record, 'grantId') || '',
    listingId: stringField(record, 'listing_id') || stringField(record, 'listingId') || '',
    tierName: stringField(record, 'tier_name') || stringField(record, 'tierName') || '',
    buyerPeerId: stringField(record, 'buyer_peer_id') || stringField(record, 'buyerPeerId') || '',
    buyerEncryptionPubkey:
      bytesField(record, 'buyer_encryption_pubkey') ?? bytesField(record, 'buyerEncryptionPubkey'),
    keyAlgorithm: stringField(record, 'key_algorithm') || stringField(record, 'keyAlgorithm'),
    accessType: (numberField(record, 'access_type') ?? numberField(record, 'accessType') ?? 0) as AccessGrant['accessType'],
    rateLimit: numberField(record, 'rate_limit') ?? numberField(record, 'rateLimit'),
    maxRecordsPerRequest: numberField(record, 'max_records_per_request') ?? numberField(record, 'maxRecordsPerRequest'),
    grantedAt: dateField(record, 'granted_at') ?? dateField(record, 'grantedAt') ?? new Date(0),
    expiresAt: dateField(record, 'expires_at') ?? dateField(record, 'expiresAt'),
    status: (numberField(record, 'status') ?? 0) as AccessGrant['status'],
    paymentTxHash: stringField(record, 'payment_tx_hash') || stringField(record, 'paymentTxHash'),
    paymentMethod: (numberField(record, 'payment_method') ?? numberField(record, 'paymentMethod') ?? 0) as PaymentMethod,
    paymentAmount: numberField(record, 'payment_amount') ?? numberField(record, 'paymentAmount') ?? 0,
    paymentCurrency: stringField(record, 'payment_currency') || stringField(record, 'paymentCurrency') || '',
    paymentChain: stringField(record, 'payment_chain') || stringField(record, 'paymentChain'),
    nextRenewal: dateField(record, 'next_renewal') ?? dateField(record, 'nextRenewal'),
    autoRenew: booleanField(record, 'auto_renew') ?? booleanField(record, 'autoRenew') ?? false,
    renewalCount: numberField(record, 'renewal_count') ?? numberField(record, 'renewalCount') ?? 0,
    totalRequests: numberField(record, 'total_requests') ?? numberField(record, 'totalRequests') ?? 0,
    totalRecords: numberField(record, 'total_records') ?? numberField(record, 'totalRecords') ?? 0,
    lastAccess: dateField(record, 'last_access') ?? dateField(record, 'lastAccess'),
    deliveryTopic: stringField(record, 'delivery_topic') || stringField(record, 'deliveryTopic'),
    providerSignature: bytesField(record, 'provider_signature') ?? bytesField(record, 'providerSignature'),
    grantResponseBase64: stringField(record, 'grant_response_base64') || stringField(record, 'grantResponseBase64'),
    providerPeerId: stringField(record, 'provider_peer_id') || stringField(record, 'providerPeerId') || '',
  };
}

function normalizeCryptoBuyerIntent(value: unknown): CryptoBuyerIntent {
  const record = isRecord(value) ? value : {};
  return {
    reference: stringField(record, 'reference') || '',
    requestId: stringField(record, 'request_id') || stringField(record, 'requestId') || '',
    chain: stringField(record, 'chain') || '',
    asset: stringField(record, 'asset') || '',
    assetContract: stringField(record, 'asset_contract') || stringField(record, 'assetContract'),
    nativeAsset: booleanField(record, 'native_asset') ?? booleanField(record, 'nativeAsset'),
    amount: numberField(record, 'amount') ?? 0,
    recipient: stringField(record, 'recipient') || '',
    method: numberField(record, 'method') as PaymentMethod | undefined,
    createdAt: dateField(record, 'created_at') ?? dateField(record, 'createdAt'),
    expiresAt: dateField(record, 'expires_at') ?? dateField(record, 'expiresAt'),
    usedAt: dateField(record, 'used_at') ?? dateField(record, 'usedAt'),
    txHash: stringField(record, 'tx_hash') || stringField(record, 'txHash'),
    intentDigest: stringField(record, 'intent_digest') || stringField(record, 'intentDigest'),
    intentSignature: stringField(record, 'intent_signature') || stringField(record, 'intentSignature'),
  };
}

function normalizeUsageSummary(value: unknown): UsageSummary {
  const record = isRecord(value) ? value : {};
  return {
    buyerPeerId: stringField(record, 'buyer_peer_id') || stringField(record, 'buyerPeerId') || '',
    listingId: stringField(record, 'listing_id') || stringField(record, 'listingId') || '',
    periodStart: dateField(record, 'period_start') ?? dateField(record, 'periodStart') ?? new Date(0),
    periodEnd: dateField(record, 'period_end') ?? dateField(record, 'periodEnd') ?? new Date(0),
    totalRecords: numberField(record, 'total_records') ?? numberField(record, 'totalRecords') ?? 0,
    totalBytes: numberField(record, 'total_bytes') ?? numberField(record, 'totalBytes') ?? 0,
    totalEvents: numberField(record, 'total_events') ?? numberField(record, 'totalEvents') ?? 0,
    billedAmountUsd: numberField(record, 'billed_amount_usd') ?? numberField(record, 'billedAmountUsd') ?? 0,
  };
}

function normalizeInvoiceLineItem(value: unknown): import('./types').InvoiceLineItem {
  const record = isRecord(value) ? value : {};
  return {
    description: stringField(record, 'description') || '',
    quantity: numberField(record, 'quantity') ?? 0,
    unitAmount: numberField(record, 'unit_amount') ?? numberField(record, 'unitAmount') ?? 0,
    amount: numberField(record, 'amount') ?? 0,
  };
}

function normalizeInvoice(value: unknown): Invoice {
  const record = isRecord(value) ? value : {};
  const lineItemsRaw = record['line_items'] ?? record['lineItems'];
  const lineItems = Array.isArray(lineItemsRaw) ? lineItemsRaw.map(normalizeInvoiceLineItem) : [];
  return {
    invoiceId: stringField(record, 'invoice_id') || stringField(record, 'invoiceId') || '',
    buyerPeerId: stringField(record, 'buyer_peer_id') || stringField(record, 'buyerPeerId') || '',
    providerPeerId: stringField(record, 'provider_peer_id') || stringField(record, 'providerPeerId') || '',
    periodStart: dateField(record, 'period_start') ?? dateField(record, 'periodStart') ?? new Date(0),
    periodEnd: dateField(record, 'period_end') ?? dateField(record, 'periodEnd') ?? new Date(0),
    lineItems,
    totalAmount: numberField(record, 'total_amount') ?? numberField(record, 'totalAmount') ?? 0,
    currency: stringField(record, 'currency') || 'USD',
    status: (stringField(record, 'status') || 'issued') as import('./types').InvoiceStatus,
    stripeInvoiceId: stringField(record, 'stripe_invoice_id') || stringField(record, 'stripeInvoiceId'),
    poReference: stringField(record, 'po_reference') || stringField(record, 'poReference'),
    notes: stringField(record, 'notes'),
    issuedAt: dateField(record, 'issued_at') ?? dateField(record, 'issuedAt') ?? new Date(0),
    paidAt: dateField(record, 'paid_at') ?? dateField(record, 'paidAt'),
    createdAt: dateField(record, 'created_at') ?? dateField(record, 'createdAt') ?? new Date(0),
    updatedAt: dateField(record, 'updated_at') ?? dateField(record, 'updatedAt') ?? new Date(0),
  };
}

function bytesToBase64(value: Uint8Array | undefined): string | undefined {
  if (!value || value.length === 0) {
    return undefined;
  }
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  let output = '';
  for (let index = 0; index < value.length; index += 3) {
    const first = value[index];
    const second = value[index + 1];
    const third = value[index + 2];
    output += alphabet[first >> 2];
    output += alphabet[((first & 0x03) << 4) | ((second ?? 0) >> 4)];
    output += index + 1 < value.length ? alphabet[((second & 0x0f) << 2) | ((third ?? 0) >> 6)] : '=';
    output += index + 2 < value.length ? alphabet[(third ?? 0) & 0x3f] : '=';
  }
  return output;
}

function base64ToBytes(value: string): Uint8Array | undefined {
  const normalized = value.trim();
  if (!normalized) {
    return undefined;
  }
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  const clean = normalized.replace(/=+$/u, '');
  const bytes: number[] = [];
  let buffer = 0;
  let bits = 0;
  for (const char of clean) {
    const next = alphabet.indexOf(char);
    if (next < 0) {
      return undefined;
    }
    buffer = (buffer << 6) | next;
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      bytes.push((buffer >> bits) & 0xff);
    }
  }
  return new Uint8Array(bytes);
}

function bytesField(record: Record<string, unknown>, key: string): Uint8Array | undefined {
  const value = record[key];
  if (value instanceof Uint8Array) {
    return value;
  }
  if (typeof value === 'string') {
    return base64ToBytes(value);
  }
  return undefined;
}

function stringField(record: Record<string, unknown>, key: string): string | undefined {
  const value = record[key];
  return typeof value === 'string' ? value : undefined;
}

function numberField(record: Record<string, unknown>, key: string): number | undefined {
  const value = record[key];
  return typeof value === 'number' ? value : undefined;
}

function booleanField(record: Record<string, unknown>, key: string): boolean | undefined {
  const value = record[key];
  return typeof value === 'boolean' ? value : undefined;
}

function dateField(record: Record<string, unknown>, key: string): Date | undefined {
  const value = record[key];
  if (typeof value !== 'string' || !value) {
    return undefined;
  }
  const parsed = new Date(value);
  return Number.isFinite(parsed.getTime()) ? parsed : undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object';
}
