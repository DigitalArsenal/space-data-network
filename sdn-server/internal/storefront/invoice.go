package storefront

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// InvoiceStatus represents the lifecycle state of an invoice.
type InvoiceStatus string

const (
	InvoiceStatusIssued InvoiceStatus = "issued"
	InvoiceStatusPaid   InvoiceStatus = "paid"
	InvoiceStatusVoid   InvoiceStatus = "void"
)

// InvoiceLineItem is a single line on an invoice.
type InvoiceLineItem struct {
	Description string `json:"description"`
	Quantity    uint64 `json:"quantity"`
	UnitAmount  uint64 `json:"unit_amount"` // cents
	Amount      uint64 `json:"amount"`      // cents
}

// Invoice represents a period-based enterprise invoice.
type Invoice struct {
	InvoiceID       string            `json:"invoice_id"`
	BuyerPeerID     string            `json:"buyer_peer_id"`
	ProviderPeerID  string            `json:"provider_peer_id"`
	PeriodStart     time.Time         `json:"period_start"`
	PeriodEnd       time.Time         `json:"period_end"`
	LineItems       []InvoiceLineItem `json:"line_items"`
	TotalAmount     uint64            `json:"total_amount"` // cents
	Currency        string            `json:"currency"`
	Status          InvoiceStatus     `json:"status"`
	StripeInvoiceID string            `json:"stripe_invoice_id,omitempty"`
	POReference     string            `json:"po_reference,omitempty"`
	Notes           string            `json:"notes,omitempty"`
	IssuedAt        time.Time         `json:"issued_at"`
	PaidAt          time.Time         `json:"paid_at,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// ── Store methods ──────────────────────────────────────────────────────────────

// CreateInvoice persists a new invoice.
func (s *Store) CreateInvoice(inv *Invoice) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lineItemsJSON, err := json.Marshal(inv.LineItems)
	if err != nil {
		return fmt.Errorf("failed to marshal line items: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO storefront_invoices (
			invoice_id, buyer_peer_id, provider_peer_id,
			period_start, period_end, line_items,
			total_amount, currency, status,
			stripe_invoice_id, po_reference, notes,
			issued_at, paid_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, inv.InvoiceID, inv.BuyerPeerID, inv.ProviderPeerID,
		inv.PeriodStart.Unix(), inv.PeriodEnd.Unix(), string(lineItemsJSON),
		inv.TotalAmount, inv.Currency, string(inv.Status),
		inv.StripeInvoiceID, inv.POReference, inv.Notes,
		inv.IssuedAt.Unix(), unixOrZero(inv.PaidAt), inv.CreatedAt.Unix(), inv.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to create invoice: %w", err)
	}
	return nil
}

// GetInvoice retrieves a single invoice by ID.
func (s *Store) GetInvoice(invoiceID string) (*Invoice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scanInvoiceRow(s.db.QueryRow(`
		SELECT invoice_id, buyer_peer_id, provider_peer_id,
		       period_start, period_end, line_items,
		       total_amount, currency, status,
		       stripe_invoice_id, po_reference, notes,
		       issued_at, paid_at, created_at, updated_at
		FROM storefront_invoices WHERE invoice_id = ?
	`, invoiceID))
}

// GetBuyerInvoices retrieves invoices for a buyer (most recent first).
func (s *Store) GetBuyerInvoices(buyerPeerID string, limit, offset int) ([]*Invoice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT invoice_id, buyer_peer_id, provider_peer_id,
		       period_start, period_end, line_items,
		       total_amount, currency, status,
		       stripe_invoice_id, po_reference, notes,
		       issued_at, paid_at, created_at, updated_at
		FROM storefront_invoices
		WHERE buyer_peer_id = ?
		ORDER BY period_start DESC
		LIMIT ? OFFSET ?
	`, buyerPeerID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query buyer invoices: %w", err)
	}
	defer rows.Close()

	var invoices []*Invoice
	for rows.Next() {
		var inv Invoice
		var periodStart, periodEnd, issuedAt, paidAt, createdAt, updatedAt int64
		var lineItemsJSON string

		if err := rows.Scan(
			&inv.InvoiceID, &inv.BuyerPeerID, &inv.ProviderPeerID,
			&periodStart, &periodEnd, &lineItemsJSON,
			&inv.TotalAmount, &inv.Currency, &inv.Status,
			&inv.StripeInvoiceID, &inv.POReference, &inv.Notes,
			&issuedAt, &paidAt, &createdAt, &updatedAt,
		); err != nil {
			log.Warnf("Failed to scan invoice row: %v", err)
			continue
		}
		inv.PeriodStart = time.Unix(periodStart, 0)
		inv.PeriodEnd = time.Unix(periodEnd, 0)
		inv.IssuedAt = time.Unix(issuedAt, 0)
		if paidAt != 0 {
			inv.PaidAt = time.Unix(paidAt, 0)
		}
		inv.CreatedAt = time.Unix(createdAt, 0)
		inv.UpdatedAt = time.Unix(updatedAt, 0)
		json.Unmarshal([]byte(lineItemsJSON), &inv.LineItems) //nolint:errcheck
		invoices = append(invoices, &inv)
	}
	return invoices, nil
}

// UpdateInvoiceStatus updates an invoice's status, Stripe invoice ID, PO reference, and paid timestamp.
func (s *Store) UpdateInvoiceStatus(invoiceID string, status InvoiceStatus, stripeInvoiceID, poRef string, paidAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	_, err := s.db.Exec(`
		UPDATE storefront_invoices
		SET status = ?, stripe_invoice_id = ?, po_reference = ?,
		    paid_at = ?, updated_at = ?
		WHERE invoice_id = ?
	`, string(status), stripeInvoiceID, poRef, unixOrZero(paidAt), now, invoiceID)
	if err != nil {
		return fmt.Errorf("failed to update invoice status: %w", err)
	}
	return nil
}

type invoiceScanner interface {
	Scan(dest ...interface{}) error
}

func (s *Store) scanInvoiceRow(row invoiceScanner) (*Invoice, error) {
	var inv Invoice
	var periodStart, periodEnd, issuedAt, paidAt, createdAt, updatedAt int64
	var lineItemsJSON string

	err := row.Scan(
		&inv.InvoiceID, &inv.BuyerPeerID, &inv.ProviderPeerID,
		&periodStart, &periodEnd, &lineItemsJSON,
		&inv.TotalAmount, &inv.Currency, &inv.Status,
		&inv.StripeInvoiceID, &inv.POReference, &inv.Notes,
		&issuedAt, &paidAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan invoice: %w", err)
	}
	inv.PeriodStart = time.Unix(periodStart, 0)
	inv.PeriodEnd = time.Unix(periodEnd, 0)
	inv.IssuedAt = time.Unix(issuedAt, 0)
	if paidAt != 0 {
		inv.PaidAt = time.Unix(paidAt, 0)
	}
	inv.CreatedAt = time.Unix(createdAt, 0)
	inv.UpdatedAt = time.Unix(updatedAt, 0)
	json.Unmarshal([]byte(lineItemsJSON), &inv.LineItems) //nolint:errcheck
	return &inv, nil
}

// ── PaymentProcessor invoice methods ──────────────────────────────────────────

// GenerateInvoice builds an invoice from usage records for a buyer/provider pair
// within a billing period.
func (pp *PaymentProcessor) GenerateInvoice(ctx context.Context, buyerPeerID, providerPeerID, listingID string, periodStart, periodEnd time.Time, currency string) (*Invoice, error) {
	_ = ctx

	summary, err := pp.store.GetUsageSummary(buyerPeerID, listingID, periodStart, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("failed to get usage summary: %w", err)
	}

	// 1 cent per 1000 records (rounded up)
	const unitCentRate uint64 = 1 // cents per 1000 records
	var lineItems []InvoiceLineItem
	var totalAmount uint64

	if summary.TotalRecords > 0 {
		recordAmount := ((summary.TotalRecords + 999) / 1000) * unitCentRate
		lineItems = append(lineItems, InvoiceLineItem{
			Description: fmt.Sprintf("Records delivered (%d records)", summary.TotalRecords),
			Quantity:    summary.TotalRecords,
			UnitAmount:  unitCentRate,
			Amount:      recordAmount,
		})
		totalAmount += recordAmount
	}

	if summary.TotalBytes > 0 {
		// 1 cent per MB (1<<20 bytes), rounded up
		const bytesPerCent uint64 = 1 << 20
		byteAmount := ((summary.TotalBytes + bytesPerCent - 1) / bytesPerCent)
		lineItems = append(lineItems, InvoiceLineItem{
			Description: fmt.Sprintf("Data transferred (%d bytes)", summary.TotalBytes),
			Quantity:    summary.TotalBytes,
			UnitAmount:  1,
			Amount:      byteAmount,
		})
		totalAmount += byteAmount
	}

	if currency == "" {
		currency = "USD"
	}

	now := time.Now()
	inv := &Invoice{
		InvoiceID:      uuid.New().String(),
		BuyerPeerID:    buyerPeerID,
		ProviderPeerID: providerPeerID,
		PeriodStart:    periodStart,
		PeriodEnd:      periodEnd,
		LineItems:      lineItems,
		TotalAmount:    totalAmount,
		Currency:       currency,
		Status:         InvoiceStatusIssued,
		IssuedAt:       now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := pp.store.CreateInvoice(inv); err != nil {
		return nil, fmt.Errorf("failed to store invoice: %w", err)
	}
	log.Infof("Generated invoice %s for buyer=%s provider=%s total=%d %s",
		inv.InvoiceID, buyerPeerID, providerPeerID, totalAmount, currency)
	return inv, nil
}

// CreateStripeInvoice creates a Stripe Invoice for the given local invoice using the
// thin-HTTP-client pattern: POST invoice_items, POST invoices, POST finalize.
func (pp *PaymentProcessor) CreateStripeInvoice(ctx context.Context, inv *Invoice, customerID string) (string, error) {
	secret := strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY"))
	if secret == "" {
		stubID := "in_stub_" + uuid.New().String()[:8]
		log.Infof("CreateStripeInvoice (stub): invoice=%s customer=%s stub_id=%s", inv.InvoiceID, customerID, stubID)
		return stubID, nil
	}

	client := &http.Client{Timeout: 30 * time.Second}

	// 1. Create invoice items
	for _, item := range inv.LineItems {
		values := url.Values{}
		values.Set("customer", customerID)
		values.Set("amount", fmt.Sprintf("%d", item.Amount))
		values.Set("currency", strings.ToLower(inv.Currency))
		values.Set("description", item.Description)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			stripeAPIBase+"/v1/invoice_items", strings.NewReader(values.Encode()))
		if err != nil {
			return "", fmt.Errorf("build stripe invoice_items request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("stripe invoice_items request failed: %w", err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("stripe invoice_items failed: status=%d", resp.StatusCode)
		}
	}

	// 2. Create the invoice
	values := url.Values{}
	values.Set("customer", customerID)
	values.Set("auto_advance", "true")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		stripeAPIBase+"/v1/invoices", strings.NewReader(values.Encode()))
	if err != nil {
		return "", fmt.Errorf("build stripe invoices request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("stripe invoices request failed: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("stripe invoices failed: status=%d", resp.StatusCode)
	}

	var invoiceResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &invoiceResp); err != nil || invoiceResp.ID == "" {
		return "", fmt.Errorf("stripe invoices response missing id")
	}
	stripeInvoiceID := invoiceResp.ID

	// 3. Finalize the invoice
	req, err = http.NewRequestWithContext(ctx, http.MethodPost,
		stripeAPIBase+"/v1/invoices/"+stripeInvoiceID+"/finalize", strings.NewReader(""))
	if err != nil {
		return "", fmt.Errorf("build stripe finalize request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err = client.Do(req)
	if err != nil {
		return "", fmt.Errorf("stripe finalize request failed: %w", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("stripe finalize failed: status=%d", resp.StatusCode)
	}

	log.Infof("Created Stripe invoice %s for local invoice %s", stripeInvoiceID, inv.InvoiceID)
	return stripeInvoiceID, nil
}

// MarkInvoicePaid marks an invoice as paid (for PO/offline settlement).
func (pp *PaymentProcessor) MarkInvoicePaid(ctx context.Context, invoiceID, poReference string) (*Invoice, error) {
	_ = ctx
	paidAt := time.Now()
	if err := pp.store.UpdateInvoiceStatus(invoiceID, InvoiceStatusPaid, "", poReference, paidAt); err != nil {
		return nil, fmt.Errorf("failed to mark invoice paid: %w", err)
	}
	inv, err := pp.store.GetInvoice(invoiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve invoice after marking paid: %w", err)
	}
	return inv, nil
}

// MarkInvoiceVoid voids an invoice.
func (pp *PaymentProcessor) MarkInvoiceVoid(ctx context.Context, invoiceID string) (*Invoice, error) {
	_ = ctx
	if err := pp.store.UpdateInvoiceStatus(invoiceID, InvoiceStatusVoid, "", "", time.Time{}); err != nil {
		return nil, fmt.Errorf("failed to void invoice: %w", err)
	}
	inv, err := pp.store.GetInvoice(invoiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve invoice after voiding: %w", err)
	}
	return inv, nil
}
