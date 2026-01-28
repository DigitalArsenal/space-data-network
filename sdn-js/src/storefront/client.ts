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
} from './types';

/** Storefront client configuration */
export interface StorefrontClientConfig {
  /** API base URL for server-side operations */
  apiBaseUrl?: string;
  /** PubSub instance for real-time updates */
  pubsub?: unknown; // Would be typed to actual PubSub type
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
    this.config = config;
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
    const purchaseRequest: Partial<PurchaseRequest> = {
      ...request,
      buyerPeerId: this.config.peerId,
      buyerEncryptionPubkey: request.encryptionPubkey || this.config.encryptionPubkey,
      keyAlgorithm: request.keyAlgorithm || this.config.keyAlgorithm,
    };

    if (this.config.apiBaseUrl) {
      const response = await fetch(`${this.config.apiBaseUrl}/storefront/purchases`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(purchaseRequest),
      });
      if (!response.ok) {
        throw new Error(`Failed to create purchase: ${response.statusText}`);
      }
      return response.json();
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
      return response.json();
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
      return response.json();
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
      return response.json();
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
      return response.json();
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

  /**
   * Start listening for PubSub messages
   */
  async startListening(): Promise<void> {
    // TODO: Subscribe to PubSub topics for real-time updates
    // This would integrate with the actual PubSub implementation
  }

  /**
   * Stop listening
   */
  async stopListening(): Promise<void> {
    // TODO: Unsubscribe from PubSub topics
  }
}

/**
 * Create a storefront client
 */
export function createStorefrontClient(config: StorefrontClientConfig): StorefrontClient {
  return new StorefrontClient(config);
}
