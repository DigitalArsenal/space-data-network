package storefront

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// UsageEvent records a single metered delivery event.
type UsageEvent struct {
	EventID        string    `json:"event_id"`
	GrantID        string    `json:"grant_id"`
	BuyerPeerID    string    `json:"buyer_peer_id"`
	ListingID      string    `json:"listing_id"`
	RecordsServed  uint64    `json:"records_served"`
	BytesDelivered uint64    `json:"bytes_delivered"`
	OccurredAt     time.Time `json:"occurred_at"`
}

// UsageSummary aggregates usage events for a buyer/listing over a billing period.
type UsageSummary struct {
	BuyerPeerID     string    `json:"buyer_peer_id"`
	ListingID       string    `json:"listing_id"`
	PeriodStart     time.Time `json:"period_start"`
	PeriodEnd       time.Time `json:"period_end"`
	TotalRecords    uint64    `json:"total_records"`
	TotalBytes      uint64    `json:"total_bytes"`
	TotalEvents     uint64    `json:"total_events"`
	BilledAmountUSD uint64    `json:"billed_amount_usd"` // cents
}

// RecordUsageEvent inserts a usage event into the store.
func (s *Store) RecordUsageEvent(event *UsageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO storefront_usage_events (
			event_id, grant_id, buyer_peer_id, listing_id,
			records_served, bytes_delivered, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, event.EventID, event.GrantID, event.BuyerPeerID, event.ListingID,
		event.RecordsServed, event.BytesDelivered, event.OccurredAt.Unix())
	if err != nil {
		return fmt.Errorf("failed to record usage event: %w", err)
	}
	return nil
}

// GetUsageSummary aggregates usage events for a buyer/listing within a time range.
func (s *Store) GetUsageSummary(buyerPeerID, listingID string, from, to time.Time) (*UsageSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	summary := &UsageSummary{
		BuyerPeerID: buyerPeerID,
		ListingID:   listingID,
		PeriodStart: from,
		PeriodEnd:   to,
	}

	err := s.db.QueryRow(`
		SELECT
			COALESCE(SUM(records_served), 0),
			COALESCE(SUM(bytes_delivered), 0),
			COUNT(*)
		FROM storefront_usage_events
		WHERE buyer_peer_id = ? AND listing_id = ?
		  AND occurred_at >= ? AND occurred_at <= ?
	`, buyerPeerID, listingID, from.Unix(), to.Unix()).Scan(
		&summary.TotalRecords,
		&summary.TotalBytes,
		&summary.TotalEvents,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate usage: %w", err)
	}
	return summary, nil
}

// ReportStripeUsageRecord calls POST /v1/subscription_items/{siID}/usage_records
// following the same thin-HTTP-client pattern as CreateFiatPaymentIntent.
func (pp *PaymentProcessor) ReportStripeUsageRecord(ctx context.Context, subscriptionItemID string, quantity uint64, timestamp int64) error {
	secret := strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY"))
	if secret == "" {
		log.Infof("ReportStripeUsageRecord (stub): item=%s quantity=%d ts=%d", subscriptionItemID, quantity, timestamp)
		return nil
	}

	endpoint := stripeAPIBase + "/v1/subscription_items/" + subscriptionItemID + "/usage_records"
	values := url.Values{}
	values.Set("quantity", fmt.Sprintf("%d", quantity))
	values.Set("timestamp", fmt.Sprintf("%d", timestamp))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("build stripe usage record request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("stripe usage record request failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	if resp.StatusCode >= 400 {
		return fmt.Errorf("stripe usage record failed: status=%d", resp.StatusCode)
	}
	return nil
}
