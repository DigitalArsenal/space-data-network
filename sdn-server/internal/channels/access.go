package channels

const (
	BoundaryListPrivate        AccessBoundary = "list_private"
	BoundarySubscribe          AccessBoundary = "subscribe"
	BoundaryUnsubscribe        AccessBoundary = "unsubscribe"
	BoundaryPublish            AccessBoundary = "publish"
	BoundaryGrantIssue         AccessBoundary = "grant_issue"
	BoundaryStreamOpen         AccessBoundary = "stream_open"
	BoundaryByteRangeRead      AccessBoundary = "byte_range_read"
	BoundaryKeyUnwrap          AccessBoundary = "key_unwrap"
	BoundaryShardImport        AccessBoundary = "shard_import"
	BoundaryModuleFeedDelivery AccessBoundary = "module_feed_delivery"
	BoundaryLocalCacheRead     AccessBoundary = "local_cache_read"
)

type AccessBoundary string

type AccessRequest struct {
	Channel  ChannelID
	Boundary AccessBoundary
	Subject  string
	GrantID  string
}

type AccessDecision struct {
	Allowed    bool
	GrantState string
	Reason     string
}

type GrantChecker interface {
	HasChannelGrant(AccessRequest) bool
}

type AccessGate struct {
	checker GrantChecker
}

func NewAccessGate(checker GrantChecker) *AccessGate {
	return &AccessGate{checker: checker}
}

func (g *AccessGate) Authorize(req AccessRequest) AccessDecision {
	if req.Boundary == "" {
		return AccessDecision{
			Allowed:    false,
			GrantState: "required",
			Reason:     "access boundary is required",
		}
	}
	if g != nil && g.checker != nil && g.checker.HasChannelGrant(req) {
		return AccessDecision{
			Allowed:    true,
			GrantState: "verified",
			Reason:     "grant verified",
		}
	}
	return AccessDecision{
		Allowed:    false,
		GrantState: "required",
		Reason:     "verified channel grant required",
	}
}
