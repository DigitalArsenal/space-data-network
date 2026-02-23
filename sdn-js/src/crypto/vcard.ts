/**
 * vCard utilities for SDN identity
 *
 * Generate and parse vCards with embedded cryptographic public keys.
 * Compatible with iOS Contacts and other vCard 3.0 readers.
 */

// eslint-disable-next-line @typescript-eslint/ban-ts-comment
// @ts-nocheck - vcard-cryptoperson doesn't have proper type definitions
import { createV3, readVCARD, UPMT, CryptoKeyT, ContactPointT, OrganizationT, OccupationT } from 'vcard-cryptoperson';
import type { DerivedIdentity } from './types';

/**
 * vCard person information
 */
export interface VCardPersonInfo {
  /** Given name (first name) */
  givenName?: string;
  /** Family name (last name) */
  familyName?: string;
  /** Additional/middle name */
  additionalName?: string;
  /** Name prefix (Dr., Mr., etc.) */
  honorificPrefix?: string;
  /** Name suffix (Jr., III, etc.) */
  honorificSuffix?: string;
  /** Email address */
  email?: string;
  /** Organization name */
  organization?: string;
  /** Job title */
  title?: string;
  /** Photo as base64 data URI (data:image/jpeg;base64,...) */
  photo?: string;
  /** Website URL */
  url?: string;
}

/**
 * vCard generation options
 */
export interface VCardOptions {
  /** Include cryptographic public keys */
  includeKeys?: boolean;
  /** Skip photo in output (for QR codes) */
  skipPhoto?: boolean;
  /** Note to include in vCard */
  note?: string;
}

/**
 * Convert Uint8Array to hex string
 */
function toHex(bytes: Uint8Array): string {
  return Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}

/**
 * Convert hex string to Uint8Array
 */
function fromHex(hex: string): Uint8Array {
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < hex.length; i += 2) {
    bytes[i / 2] = parseInt(hex.substr(i, 2), 16);
  }
  return bytes;
}

/**
 * Post-process vCard for iOS compatibility
 * Converts PHOTO;VALUE=URI:data:... to PHOTO;ENCODING=b;TYPE=JPEG:...
 */
function makeIOSCompatible(vcard: string): string {
  // Find PHOTO;VALUE=URI:data:image/jpeg;base64,...
  const photoMatch = vcard.match(/PHOTO;VALUE=URI:data:image\/(jpeg|png);base64,([^\n]+)/i);
  if (photoMatch) {
    const imageType = photoMatch[1].toUpperCase();
    const base64Data = photoMatch[2];

    // iOS requires line folding for long lines (max 75 chars)
    const foldedData = base64Data.match(/.{1,74}/g)?.join('\n ') || base64Data;

    const iosPhoto = `PHOTO;ENCODING=b;TYPE=${imageType}:${foldedData}`;
    vcard = vcard.replace(/PHOTO;VALUE=URI:data:image\/[^;]+;base64,[^\n]+/, iosPhoto);
  }

  return vcard;
}

// Key type constants for ADDRESS_TYPE field
const KEY_TYPE_ED25519 = 1;
const KEY_TYPE_X25519 = 2;

/**
 * Generate a vCard with optional cryptographic keys
 */
export function generateVCard(
  info: VCardPersonInfo,
  identity?: DerivedIdentity,
  options: VCardOptions = {}
): string {
  const { includeKeys = true, skipPhoto = false, note = '' } = options;

  const person = new UPMT();

  // Set name fields
  person.GIVEN_NAME = info.givenName || '';
  person.FAMILY_NAME = info.familyName || '';
  person.ADDITIONAL_NAME = info.additionalName || '';
  person.HONORIFIC_PREFIX = info.honorificPrefix || '';
  person.HONORIFIC_SUFFIX = info.honorificSuffix || '';

  // Set contact info
  if (info.email) {
    const contact = new ContactPointT();
    contact.EMAIL = info.email;
    contact.CONTACT_TYPE = 'work';
    person.CONTACT_POINT.push(contact);
  }

  // Set organization
  if (info.organization) {
    const org = new OrganizationT();
    org.NAME = info.organization;
    org.LEGAL_NAME = info.organization;
    person.AFFILIATION = org;
  }

  // Set job title
  if (info.title) {
    const occupation = new OccupationT();
    occupation.NAME = info.title;
    person.HAS_OCCUPATION = occupation;
  }

  // Set photo (skip if requested, e.g. for QR codes)
  if (info.photo && !skipPhoto) {
    person.IMAGE = info.photo;
  }

  // Set URL
  if (info.url) {
    person.SAME_AS = info.url;
  }

  // Add cryptographic keys
  if (includeKeys && identity) {
    // Ed25519 signing public key
    const ed25519Key = new CryptoKeyT();
    ed25519Key.PUBLIC_KEY = toHex(identity.signingKey.publicKey);
    ed25519Key.ADDRESS_TYPE = KEY_TYPE_ED25519;
    person.KEY.push(ed25519Key);

    // X25519 encryption public key
    const x25519Key = new CryptoKeyT();
    x25519Key.PUBLIC_KEY = toHex(identity.encryptionKey.publicKey);
    x25519Key.ADDRESS_TYPE = KEY_TYPE_X25519;
    person.KEY.push(x25519Key);
  }

  // Generate vCard 3.0
  let vcard = createV3(person, note);

  // Post-process for iOS compatibility
  vcard = makeIOSCompatible(vcard);

  return vcard;
}

/**
 * Parsed vCard result
 */
export interface ParsedVCard {
  /** Person information */
  person: VCardPersonInfo;
  /** Extracted cryptographic keys */
  keys: {
    ed25519PublicKey?: Uint8Array;
    x25519PublicKey?: Uint8Array;
  };
}

/**
 * Parse a vCard string and extract person info and keys
 */
export function parseVCard(vcardString: string): ParsedVCard {
  // Unfold long lines (RFC 5322 line folding)
  const unfolded = vcardString.replace(/\r?\n[ \t]/g, '');

  const person = readVCARD(unfolded);

  const result: ParsedVCard = {
    person: {
      givenName: person.GIVEN_NAME || undefined,
      familyName: person.FAMILY_NAME || undefined,
      additionalName: person.ADDITIONAL_NAME || undefined,
      honorificPrefix: person.HONORIFIC_PREFIX || undefined,
      honorificSuffix: person.HONORIFIC_SUFFIX || undefined,
      organization: person.AFFILIATION?.NAME || person.AFFILIATION?.LEGAL_NAME || undefined,
      title: person.HAS_OCCUPATION?.NAME || undefined,
      photo: person.IMAGE || undefined,
      url: person.SAME_AS || undefined,
    },
    keys: {},
  };

  // Extract email from contact points
  for (const contact of person.CONTACT_POINT || []) {
    if (contact.EMAIL && !result.person.email) {
      result.person.email = contact.EMAIL;
    }
  }

  // Extract cryptographic keys based on ADDRESS_TYPE
  for (const key of person.KEY || []) {
    if (key.PUBLIC_KEY) {
      const pubKeyHex = typeof key.PUBLIC_KEY === 'string' ? key.PUBLIC_KEY : '';
      if (key.ADDRESS_TYPE === KEY_TYPE_ED25519) {
        result.keys.ed25519PublicKey = fromHex(pubKeyHex);
      } else if (key.ADDRESS_TYPE === KEY_TYPE_X25519) {
        result.keys.x25519PublicKey = fromHex(pubKeyHex);
      }
    }
  }

  return result;
}

/**
 * Generate a vCard download blob
 */
export function createVCardBlob(vcardString: string): Blob {
  return new Blob([vcardString], { type: 'text/vcard;charset=utf-8' });
}

/**
 * Generate a vCard data URL for download
 */
export function createVCardDataURL(vcardString: string): string {
  const blob = createVCardBlob(vcardString);
  return URL.createObjectURL(blob);
}
