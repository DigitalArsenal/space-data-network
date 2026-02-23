// Package subscription provides subscription management and routing for SDN.
//
// This package implements Phase 10 of the SDN roadmap: Subscription UI and Routing.
//
// # Data Type Subscription System
//
// The subscription system allows nodes to subscribe to specific SDS data types
// from selected peers with optional encryption and streaming modes:
//
//	config := SubscriptionConfig{
//	    DataTypes:   []string{"OMM.fbs", "CDM.fbs"},
//	    SourcePeers: []string{"all"},
//	    Encrypted:   true,
//	    Streaming:   true,
//	    Filters:     []QueryFilter{{Field: "OBJECT_NAME", Operator: OpEqual, Value: "ISS"}},
//	    RateLimit:   1000,
//	}
//	sub, err := manager.CreateSubscription(config)
//
// # Header-Based Routing
//
// Messages include a routing header (RHD) that contains unencrypted metadata
// for routing purposes:
//
//   - schemaType: Schema identifier (e.g., "OMM", "CDM") for topic-based routing
//   - destinationPeers: Specific peer IDs for targeted delivery
//   - ttl: Time-to-live (hop count) for limiting broadcast range
//   - priority: Message priority for routing decisions
//   - encrypted: Whether the payload is encrypted
//
// The header remains unencrypted so edge relays can route messages without
// needing to decrypt the payload.
//
// # PubSub Topic Routing
//
// Messages are routed using topic-based PubSub:
//
//   - Schema-based: /sdn/data/{schema_type} (e.g., /sdn/data/OMM)
//   - Peer-based: /sdn/peer/{peer_id} for targeted delivery
//   - Standard: /spacedatanetwork/sds/{schema}.fbs for compatibility
//
// # Streaming Modes
//
// Three streaming modes are supported:
//
//   - Encrypted: ECIES per-message or session key encryption
//   - Unencrypted: For public data like TLEs
//   - Hybrid: Headers unencrypted, payload encrypted
//
// # Admin API
//
// The package provides HTTP handlers for subscription management:
//
//   - GET    /api/subscriptions         - List all subscriptions
//   - POST   /api/subscriptions         - Create subscription
//   - GET    /api/subscriptions/{id}    - Get subscription details
//   - PUT    /api/subscriptions/{id}    - Update subscription
//   - DELETE /api/subscriptions/{id}    - Delete subscription
//   - POST   /api/subscriptions/{id}/pause  - Pause subscription
//   - POST   /api/subscriptions/{id}/resume - Resume subscription
//   - GET    /api/subscriptions/topics  - Get required PubSub topics
//   - GET    /api/subscriptions/stats   - Get subscription statistics
//
// # Admin UI
//
// A web-based admin interface is available at /admin/subscriptions for:
//
//   - Viewing and managing subscriptions
//   - Creating new subscriptions with data type selector
//   - Monitoring message counts and statistics
//   - Pausing/resuming subscriptions
//
// # Metrics
//
// The package exports Prometheus-compatible metrics:
//
//   - sdn_subscriptions_total: Total number of subscriptions
//   - sdn_subscriptions_active: Active subscriptions
//   - sdn_subscriptions_paused: Paused subscriptions
//   - sdn_messages_total: Total messages received
//   - sdn_subscriptions_by_schema: Subscriptions per schema type
package subscription
