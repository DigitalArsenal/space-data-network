// Package pubsub provides PubSub topic management and the PNM-based tip/queue system
// for Space Data Network.
//
// # Overview
//
// This package implements two key features:
//
//  1. Topic Management - Manages libp2p PubSub topics for Space Data Standards schemas
//  2. TipQueue System - Uses Publish Notification Messages (PNM) for content discovery
//
// # Topic Management
//
// The TopicManager handles subscription and publishing for all SDS schema topics:
//
//	tm, _ := pubsub.NewTopicManager(ps, validator)
//	tm.SetupSDSTopics()
//
//	// Subscribe to OMM messages
//	sub, _ := tm.Subscribe("OMM")
//
//	// Publish an OMM message
//	tm.Publish("OMM", ommData)
//
// Topics follow the naming convention: /spacedatanetwork/sds/{SchemaName}
//
// # TipQueue System
//
// The TipQueue uses PNM (Publish Notification Message) as the core messaging mechanism
// for content discovery. Instead of broadcasting all pinned data, nodes announce
// content availability via PNM, allowing subscribers to selectively fetch and pin
// content based on configurable policies.
//
// # Configuration System
//
// The configuration system supports per-source AND per-schema settings with
// priority-based resolution:
//
//	config := pubsub.NewTipQueueConfig()
//
//	// System-wide defaults
//	config.DefaultAutoFetch = false
//	config.DefaultAutoPin = false
//	config.DefaultTTL = 24 * time.Hour
//
//	// Per-schema defaults (e.g., always fetch conjunction data)
//	config.SetSchemaDefault("CDM", &pubsub.SchemaConfig{
//	    AutoFetch: true,
//	    AutoPin:   true,
//	    TTL:       48 * time.Hour,
//	    Priority:  10,
//	})
//
//	// Per-source overrides (e.g., trust certain peers)
//	config.SetSourceOverride("trusted-peer-id", &pubsub.SourceConfig{
//	    Trusted:   true,
//	    AutoFetch: pubsub.BoolPtr(true),
//	    TTL:       pubsub.DurationPtr(72 * time.Hour),
//	})
//
//	// Per-source per-schema override (highest priority)
//	config.SetSourceSchemaOverride("trusted-peer-id", "OMM", &pubsub.SchemaConfig{
//	    AutoFetch: true,
//	    AutoPin:   true,
//	    TTL:       168 * time.Hour,
//	})
//
// # Configuration Resolution Order
//
// When a PNM is received, configuration is resolved with this priority:
//
//  1. Source+Schema Override (highest) - SourceOverrides[peerID].SchemaOverrides[schema]
//  2. Source Override - SourceOverrides[peerID]
//  3. Schema Default - SchemaDefaults[schema]
//  4. System Default (lowest) - Default* values
//
// # TipQueue Usage
//
// Create and configure a TipQueue:
//
//	tq := pubsub.NewTipQueue(config)
//	tq.SetTopicManager(topicManager)
//	tq.SetFetcher(fetcher)  // implements ContentFetcher
//	tq.SetPinner(pinner)    // implements ContentPinner
//
//	// Handle received tips
//	tq.OnTip(func(tip *pubsub.Tip, cfg pubsub.ResolvedConfig) {
//	    log.Printf("Received: %s from %s", tip.CID, tip.PeerID)
//	})
//
//	// Start listening for PNM messages
//	tq.Subscribe()
//
//	// Publish your own tips
//	tq.PublishTip(ctx, pubsub.PublishOptions{
//	    CID:        "bafybei...",
//	    SchemaType: "OMM",
//	    Signature:  "0x...",
//	})
//
// # Interfaces
//
// Implement these interfaces to integrate with your storage system:
//
//	type ContentFetcher interface {
//	    Fetch(ctx context.Context, cid string) ([]byte, error)
//	}
//
//	type ContentPinner interface {
//	    Pin(ctx context.Context, cid string, ttl time.Duration) error
//	    Unpin(ctx context.Context, cid string) error
//	}
//
// # Thread Safety
//
// TipQueueConfig and TipQueue are thread-safe. Configuration can be modified
// at runtime and changes take effect immediately for new PNM messages.
package pubsub
