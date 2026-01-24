// Package sds provides Space Data Standards FlatBuffer builders and validators.
//
// This package offers a fluent API for creating FlatBuffer messages that conform
// to the Space Data Standards specification (https://spacedatastandards.org).
//
// # Supported Schemas
//
// The package provides builders for the following schemas:
//
//   - OMM (Orbit Mean-Elements Message) - Orbital element data
//   - EPM (Entity Profile Message) - Entity/organization profiles
//   - PNM (Publish Notification Message) - Content announcements
//   - CAT (Catalog) - Space object catalog entries
//
// # Builder Pattern
//
// All builders use a fluent API pattern with method chaining:
//
//	data := sds.NewOMMBuilder().
//	    WithObjectName("ISS (ZARYA)").
//	    WithNoradCatID(25544).
//	    WithEpoch("2024-01-15T12:00:00.000Z").
//	    Build()
//
// Builders return size-prefixed FlatBuffer bytes that can be directly used with
// the Space Data Standards Go library for deserialization.
//
// # Performance
//
// FlatBuffers provide zero-copy deserialization, achieving extremely fast read
// performance (typically 5ns or less per message). Serialization is also efficient,
// typically completing in under 500ns for most message types.
//
// # Thread Safety
//
// Builders are NOT thread-safe. Each goroutine should create its own builder
// instance. The Build() method returns a copy of the buffer, so the returned
// bytes can be safely shared across goroutines.
//
// # Validation
//
// The Validator type provides schema validation using file identifiers embedded
// in FlatBuffer messages. See [Validator] for details.
package sds
