package storefront

import (
	"fmt"
	"math"
	"time"
)

// TrustScore represents a computed trust score for a provider
type TrustScore struct {
	PeerID             string  `json:"peer_id"`
	OverallScore       float64 `json:"overall_score"`        // 0-100
	ReputationScore    float64 `json:"reputation_score"`     // 0-100 from marketplace
	UptimeScore        float64 `json:"uptime_score"`         // 0-100
	DeliveryScore      float64 `json:"delivery_score"`       // 0-100
	DataQualityScore   float64 `json:"data_quality_score"`   // 0-100
	DisputeScore       float64 `json:"dispute_score"`        // 0-100 (100 = no disputes)
	TenureScore        float64 `json:"tenure_score"`         // 0-100 based on time active
	VolumeScore        float64 `json:"volume_score"`         // 0-100 based on sales volume
	EscrowRequired     bool    `json:"escrow_required"`      // Whether escrow is needed
	Featured           bool    `json:"featured"`             // Whether eligible for featured
	ComputedAt         int64   `json:"computed_at"`
}

// TrustWeights defines the weights for computing overall trust score
type TrustWeights struct {
	Reputation  float64 `json:"reputation"`   // Rating weight
	Uptime      float64 `json:"uptime"`       // Uptime weight
	Delivery    float64 `json:"delivery"`     // Delivery latency weight
	DataQuality float64 `json:"data_quality"` // Data quality weight
	Disputes    float64 `json:"disputes"`     // Dispute count weight
	Tenure      float64 `json:"tenure"`       // Time as provider weight
	Volume      float64 `json:"volume"`       // Sales volume weight
}

// DefaultTrustWeights returns default trust scoring weights
func DefaultTrustWeights() TrustWeights {
	return TrustWeights{
		Reputation:  0.30,
		Uptime:      0.15,
		Delivery:    0.15,
		DataQuality: 0.15,
		Disputes:    0.10,
		Tenure:      0.05,
		Volume:      0.10,
	}
}

// TrustScorer computes trust scores for providers
type TrustScorer struct {
	store   *Store
	weights TrustWeights
}

// NewTrustScorer creates a new trust scorer
func NewTrustScorer(store *Store, weights TrustWeights) *TrustScorer {
	return &TrustScorer{
		store:   store,
		weights: weights,
	}
}

// ComputeProviderTrust computes the trust score for a provider
func (ts *TrustScorer) ComputeProviderTrust(providerPeerID string) (*TrustScore, error) {
	// Get provider listings to extract reputation
	result, err := ts.store.SearchListings(&SearchQuery{
		ProviderPeerIDs: []string{providerPeerID},
		Limit:           100,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get provider listings: %w", err)
	}

	if len(result.Listings) == 0 {
		return &TrustScore{
			PeerID:         providerPeerID,
			OverallScore:   0,
			EscrowRequired: true,
			ComputedAt:     time.Now().Unix(),
		}, nil
	}

	// Aggregate reputation across all listings
	var totalSales uint64
	var totalRatings uint32
	var ratingSum float64
	var bestUptime uint16
	var bestLatency uint32 = math.MaxUint32
	var totalDisputes uint32
	var earliestSince uint64 = math.MaxUint64

	// Get review data for quality metrics
	var qualitySum DataQualityMetrics
	var qualityCount int

	for _, listing := range result.Listings {
		rep := listing.Reputation
		totalSales += rep.TotalSales
		totalRatings += rep.TotalRatings
		ratingSum += float64(rep.AverageRatingX10) * float64(rep.TotalRatings)
		if rep.UptimePercentageX100 > bestUptime {
			bestUptime = rep.UptimePercentageX100
		}
		if rep.AvgDeliveryLatencyMs > 0 && rep.AvgDeliveryLatencyMs < bestLatency {
			bestLatency = rep.AvgDeliveryLatencyMs
		}
		totalDisputes += rep.DisputeCount
		if rep.ProviderSince > 0 && rep.ProviderSince < earliestSince {
			earliestSince = rep.ProviderSince
		}

		// Fetch review quality metrics
		_, stats, err := ts.store.GetReviewsForListing(listing.ListingID, 1, 0)
		if err == nil && stats != nil {
			qualitySum.SchemaCompliance += stats.AvgQualityMetrics.SchemaCompliance
			qualitySum.DataFreshness += stats.AvgQualityMetrics.DataFreshness
			qualitySum.CoverageAccuracy += stats.AvgQualityMetrics.CoverageAccuracy
			qualitySum.DeliveryReliability += stats.AvgQualityMetrics.DeliveryReliability
			qualityCount++
		}
	}

	// Compute individual scores
	score := &TrustScore{
		PeerID:     providerPeerID,
		ComputedAt: time.Now().Unix(),
	}

	// Reputation score: average rating out of 5, scaled to 0-100
	if totalRatings > 0 {
		avgRating := ratingSum / float64(totalRatings) / 10.0 // Back to 1-5 scale
		score.ReputationScore = clamp(avgRating/5.0*100, 0, 100)
	}

	// Uptime score: percentage scaled to 0-100
	score.UptimeScore = clamp(float64(bestUptime)/100.0, 0, 100)

	// Delivery score: inverse of latency, capped
	if bestLatency > 0 && bestLatency < math.MaxUint32 {
		// Under 100ms = 100, 1s = 50, over 5s = 0
		latencyMs := float64(bestLatency)
		score.DeliveryScore = clamp(100.0-latencyMs/50.0, 0, 100)
	}

	// Data quality score: average of quality metrics
	if qualityCount > 0 {
		avgQ := DataQualityMetrics{
			SchemaCompliance:    qualitySum.SchemaCompliance / uint8(qualityCount),
			DataFreshness:       qualitySum.DataFreshness / uint8(qualityCount),
			CoverageAccuracy:    qualitySum.CoverageAccuracy / uint8(qualityCount),
			DeliveryReliability: qualitySum.DeliveryReliability / uint8(qualityCount),
		}
		score.DataQualityScore = clamp(float64(avgQ.SchemaCompliance+avgQ.DataFreshness+avgQ.CoverageAccuracy+avgQ.DeliveryReliability)/4.0, 0, 100)
	}

	// Dispute score: 100 = no disputes, decreases with more
	if totalSales > 0 {
		disputeRate := float64(totalDisputes) / float64(totalSales)
		score.DisputeScore = clamp(100.0-disputeRate*1000.0, 0, 100)
	} else {
		score.DisputeScore = 50 // Neutral for new providers
	}

	// Tenure score: 0 for new, 100 for 2+ years
	if earliestSince > 0 && earliestSince < math.MaxUint64 {
		since := time.Unix(int64(earliestSince), 0)
		daysSince := time.Since(since).Hours() / 24
		score.TenureScore = clamp(daysSince/730.0*100.0, 0, 100) // 730 days = 2 years = max
	}

	// Volume score: logarithmic scale, 100+ sales = 100
	if totalSales > 0 {
		score.VolumeScore = clamp(math.Log10(float64(totalSales)+1)/2.0*100.0, 0, 100)
	}

	// Compute overall score
	score.OverallScore = ts.weights.Reputation*score.ReputationScore +
		ts.weights.Uptime*score.UptimeScore +
		ts.weights.Delivery*score.DeliveryScore +
		ts.weights.DataQuality*score.DataQualityScore +
		ts.weights.Disputes*score.DisputeScore +
		ts.weights.Tenure*score.TenureScore +
		ts.weights.Volume*score.VolumeScore

	// Determine escrow/featured thresholds
	score.EscrowRequired = score.OverallScore < 50 || totalSales < 5
	score.Featured = score.OverallScore >= 80 && totalSales >= 20

	return score, nil
}

// ComputeListingTrust computes a trust score for a specific listing
func (ts *TrustScorer) ComputeListingTrust(listingID string) (*TrustScore, error) {
	listing, err := ts.store.GetListing(listingID)
	if err != nil || listing == nil {
		return nil, fmt.Errorf("listing not found: %s", listingID)
	}

	return ts.ComputeProviderTrust(listing.ProviderPeerID)
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
