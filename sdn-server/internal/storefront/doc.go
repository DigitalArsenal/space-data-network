// Package storefront provides the data marketplace functionality for SDN.
//
// The storefront enables providers to list their space data offerings and
// buyers to discover, purchase, and access that data. Key features include:
//
//   - Listing Management: Create, update, and manage data listings (STF)
//   - Access Control: Issue and manage access grants (ACL)
//   - Purchase Flow: Handle purchase requests (PUR) and payment verification
//   - Reviews: Buyer reviews and ratings (REV)
//   - Search & Discovery: DHT-based catalog and SQLite indexing
//   - Encrypted Delivery: ECIES-encrypted data delivery to buyers
//
// The package integrates with:
//   - PubSub for listing announcements and real-time data delivery
//   - SQLite for search indexing and local storage
//   - Crypto module for signatures and encryption
package storefront
