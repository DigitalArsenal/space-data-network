/**
 * Session Auth — Ed25519 challenge-response authentication for SDN server.
 *
 * Implements the challenge-response flow matching sdn-server/internal/auth/handler.go:
 * 1. POST /api/auth/challenge with { xpub, client_pubkey_hex, ts }
 * 2. Sign the returned challenge bytes with Ed25519
 * 3. POST /api/auth/verify with { challenge_id, xpub, client_pubkey_hex, challenge, signature_hex }
 * 4. Server sets session cookie for subsequent requests
 */

import type { DerivedIdentity } from '../crypto/types';
import { sign } from '../crypto/index';

/** Auth provider interface used by HttpTransport. */
export interface AuthProvider {
  /** Authenticate with the server (call before making authenticated requests). */
  authenticate(): Promise<void>;
  /** Get auth headers to include in requests. */
  getAuthHeaders(): Promise<Record<string, string>>;
  /** Whether the provider has an active session. */
  isAuthenticated(): boolean;
}

/** Challenge response from the server. */
interface ChallengeResponse {
  challenge_id: string;
  challenge: string;
  expires_at: number;
}

/** Verify response from the server. */
interface VerifyResponse {
  user: {
    xpub: string;
    name: string;
    trust_level: string;
  };
  expires_at: number;
}

/**
 * Session-based authentication using Ed25519 challenge-response.
 *
 * After successful authentication, the server sets an HTTP-only session cookie.
 * Subsequent requests use `credentials: 'include'` to send the cookie automatically.
 */
export class SessionAuth implements AuthProvider {
  private baseUrl: string;
  private identity: DerivedIdentity;
  private authenticated = false;
  private expiresAt = 0;

  constructor(baseUrl: string, identity: DerivedIdentity) {
    this.baseUrl = baseUrl.replace(/\/+$/, '');
    this.identity = identity;
  }

  isAuthenticated(): boolean {
    if (!this.authenticated) return false;
    // Check if session has expired (with 60s buffer)
    if (this.expiresAt > 0 && Date.now() / 1000 > this.expiresAt - 60) {
      this.authenticated = false;
      return false;
    }
    return true;
  }

  async authenticate(): Promise<void> {
    const pubKeyHex = bytesToHex(this.identity.signingKey.publicKey);

    // Step 1: Request challenge
    const challengeResp = await globalThis.fetch(`${this.baseUrl}/api/auth/challenge`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({
        xpub: this.identity.xpub,
        client_pubkey_hex: pubKeyHex,
        ts: Math.floor(Date.now() / 1000),
      }),
    });

    if (!challengeResp.ok) {
      const err = await challengeResp.text().catch(() => '');
      throw new Error(`Challenge request failed (${challengeResp.status}): ${err}`);
    }

    const challenge: ChallengeResponse = await challengeResp.json();

    // Step 2: Sign the challenge
    // challenge.challenge is base64-encoded challenge bytes
    const challengeBytes = base64ToBytes(challenge.challenge);
    const signatureBytes = await sign(this.identity.signingKey.privateKey, challengeBytes);
    const signatureHex = bytesToHex(signatureBytes);

    // Step 3: Verify signature with server
    const verifyResp = await globalThis.fetch(`${this.baseUrl}/api/auth/verify`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({
        challenge_id: challenge.challenge_id,
        xpub: this.identity.xpub,
        client_pubkey_hex: pubKeyHex,
        challenge: challenge.challenge,
        signature_hex: signatureHex,
      }),
    });

    if (!verifyResp.ok) {
      const err = await verifyResp.text().catch(() => '');
      throw new Error(`Verify request failed (${verifyResp.status}): ${err}`);
    }

    const result: VerifyResponse = await verifyResp.json();
    this.authenticated = true;
    this.expiresAt = result.expires_at;
  }

  async getAuthHeaders(): Promise<Record<string, string>> {
    // Session cookie is sent automatically via credentials: 'include'.
    // No explicit auth header needed for session-based auth.
    return {};
  }
}

/** Convert Uint8Array to lowercase hex string. */
function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}

/** Decode base64 (standard, no padding) to Uint8Array. */
function base64ToBytes(b64: string): Uint8Array {
  // Handle both standard and URL-safe base64
  const normalized = b64.replace(/-/g, '+').replace(/_/g, '/');
  const binary = atob(normalized);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}
