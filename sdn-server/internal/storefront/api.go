package storefront

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	nodeapi "github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// DevPaymentsEnvVar gates the manual/dev "mark this purchase paid without a
// real payment" endpoint. It must be explicitly set to "1" — matching the
// off-by-default pattern used elsewhere in this codebase for test/dev-only
// surfaces (see SDN_WIRESPEED_TEST in internal/api/channels.go) — and is
// unset in every production deployment config. Even with the flag set, the
// caller must still own the purchase (or hold peers.Admin trust); see
// handleManualDevPaid.
const DevPaymentsEnvVar = "SDN_STOREFRONT_DEV_PAYMENTS"

func devPaymentsEnabled() bool {
	return strings.TrimSpace(os.Getenv(DevPaymentsEnvVar)) == "1"
}

// APIHandler provides HTTP handlers for the storefront API
type APIHandler struct {
	service  *Service
	catalog  *Catalog
	delivery *DeliveryService
	payment  *PaymentProcessor
	trust    *TrustScorer
}

// NewAPIHandler creates a new API handler
func NewAPIHandler(service *Service, catalog *Catalog, delivery *DeliveryService, payment *PaymentProcessor, trust *TrustScorer) *APIHandler {
	return &APIHandler{
		service:  service,
		catalog:  catalog,
		delivery: delivery,
		payment:  payment,
		trust:    trust,
	}
}

// RegisterRoutes registers the storefront HTTP routes on a mux.
// authHandler may be nil (auth disabled), in which case all routes are open.
func (h *APIHandler) RegisterRoutes(mux *http.ServeMux, authHandler *auth.Handler) {
	// Helper to wrap with auth at a given trust level (no-op if authHandler is nil)
	requireAuth := func(minTrust peers.TrustLevel, handler http.HandlerFunc) http.HandlerFunc {
		if authHandler == nil {
			return handler
		}
		return authHandler.RequireAuth(minTrust, handler)
	}
	optionalAuth := func(handler http.HandlerFunc) http.HandlerFunc {
		if authHandler == nil {
			return handler
		}
		return authHandler.OptionalAuth(handler)
	}

	// Listings — read is public, write requires auth
	mux.HandleFunc("/api/storefront/listings", optionalAuth(h.handleListings))
	mux.HandleFunc("/api/storefront/listings/search", optionalAuth(h.handleSearchListings))
	mux.HandleFunc("/api/storefront/listings/", optionalAuth(h.handleListingByID))
	nodeapi.RegisterStorefrontListingRoutes(mux, storefrontListingBackend{handler: h}, func(next http.HandlerFunc) http.HandlerFunc {
		return requireAuth(peers.Admin, next)
	})

	// Purchases — all require auth
	mux.HandleFunc("/api/storefront/purchases", requireAuth(peers.Standard, h.handleCreatePurchase))
	mux.HandleFunc("/api/storefront/purchases/", requireAuth(peers.Standard, h.handlePurchaseByID))

	// Grants — require auth
	mux.HandleFunc("/api/storefront/grants", requireAuth(peers.Standard, h.handleGrants))
	mux.HandleFunc("/api/storefront/grants/", requireAuth(peers.Standard, h.handleGrantByID))

	// Reviews — read is public via listing sub-path, create requires auth
	mux.HandleFunc("/api/storefront/reviews", requireAuth(peers.Standard, h.handleCreateReview))
	mux.HandleFunc("/api/storefront/reviews/", requireAuth(peers.Standard, h.handleReviewByID))

	// Credits — require auth
	mux.HandleFunc("/api/storefront/credits/", requireAuth(peers.Standard, h.handleCredits))

	// Trust — public (read-only)
	mux.HandleFunc("/api/storefront/trust/", h.handleTrust)

	// Dashboards — require auth
	mux.HandleFunc("/api/storefront/dashboard/seller", requireAuth(peers.Standard, h.handleSellerDashboard))
	mux.HandleFunc("/api/storefront/dashboard/buyer", requireAuth(peers.Standard, h.handleBuyerDashboard))
	mux.HandleFunc("/api/storefront/dashboard/admin", requireAuth(peers.Admin, h.handleAdminDashboard))

	// Usage metering and enterprise invoices — require auth
	mux.HandleFunc("/api/storefront/usage/", requireAuth(peers.Standard, h.handleUsageSummary))
	mux.HandleFunc("/api/storefront/invoices", requireAuth(peers.Standard, h.handleInvoices))
	mux.HandleFunc("/api/storefront/invoices/", requireAuth(peers.Standard, h.handleInvoiceByID))

	// Stripe webhook — no auth (validated by HMAC signature)
	mux.HandleFunc("/api/storefront/payments/stripe/webhook", h.handleStripeWebhook)
}

// handleUsageSummary serves GET /api/storefront/usage/{listingId}/summary
// with buyer/from/to query parameters.
func (h *APIHandler) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := extractPathParam(r.URL.Path, "/api/storefront/usage/")
	listingID, suffix, ok := strings.Cut(rest, "/")
	if !ok || suffix != "summary" || listingID == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	buyerID, ok := authorizeStorefrontPeerQuery(w, r, r.URL.Query().Get("buyer"), "buyer")
	if !ok {
		return
	}
	from, err := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	if err != nil {
		http.Error(w, "invalid from time", http.StatusBadRequest)
		return
	}
	to, err := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if err != nil {
		http.Error(w, "invalid to time", http.StatusBadRequest)
		return
	}
	summary, err := h.service.store.GetUsageSummary(buyerID, listingID, from, to)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// handleInvoices serves GET /api/storefront/invoices for the authenticated buyer.
func (h *APIHandler) handleInvoices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	buyerID, ok := authorizeStorefrontPeerQuery(w, r, r.URL.Query().Get("buyer"), "buyer")
	if !ok {
		return
	}
	limit := queryInt(r, "limit", 20)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	invoices, err := h.service.store.GetBuyerInvoices(buyerID, limit, offset)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if invoices == nil {
		invoices = []*Invoice{}
	}
	writeJSON(w, http.StatusOK, invoices)
}

// handleInvoiceByID serves GET /api/storefront/invoices/{invoiceId}; buyers
// may only fetch their own invoices.
func (h *APIHandler) handleInvoiceByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	invoiceID := extractPathParam(r.URL.Path, "/api/storefront/invoices/")
	if invoiceID == "" {
		http.Error(w, "invoice id required", http.StatusBadRequest)
		return
	}
	invoice, err := h.service.store.GetInvoice(invoiceID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if invoice == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if _, ok := authorizeStorefrontPeerQuery(w, r, invoice.BuyerPeerID, "buyer"); !ok {
		return
	}
	writeJSON(w, http.StatusOK, invoice)
}

func (h *APIHandler) handleListings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		var listing Listing
		if err := json.NewDecoder(r.Body).Decode(&listing); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// Field validation
		if strings.TrimSpace(listing.Title) == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		if len(listing.Pricing) == 0 {
			http.Error(w, "at least one pricing tier is required", http.StatusBadRequest)
			return
		}
		if err := h.service.CreateListing(r.Context(), &listing); err != nil {
			http.Error(w, "failed to create listing", http.StatusInternalServerError)
			return
		}
		if h.catalog != nil {
			h.catalog.PublishListing(r.Context(), &listing)
		}
		writeJSON(w, http.StatusCreated, listing)

	case http.MethodGet:
		providerID := r.URL.Query().Get("provider")
		if providerID != "" {
			result, err := h.service.GetProviderListings(r.Context(), providerID)
			if err != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		// Default: return all active listings
		result, err := h.service.SearchListings(r.Context(), &SearchQuery{Limit: 50})
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) handleSearchListings(w http.ResponseWriter, r *http.Request) {
	var query SearchQuery
	switch r.Method {
	case http.MethodGet:
		query.SearchText = strings.TrimSpace(r.URL.Query().Get("q"))
		if kind := strings.TrimSpace(r.URL.Query().Get("listing_kind")); kind != "" {
			query.ListingKinds = []ListingKind{listingKindFromSDS(kind)}
		}
		query.Limit = queryInt(r, "limit", 20)
		query.Offset = queryInt(r, "offset", 0)
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			http.Error(w, "invalid query", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	result, err := h.service.SearchListings(r.Context(), &query)
	if err != nil {
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *APIHandler) handlePublishListing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var draft ListingDraft
	if err := decoder.Decode(&draft); err != nil {
		http.Error(w, "invalid SDS listing draft: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := draft.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid SDS listing draft: trailing JSON value", http.StatusBadRequest)
		return
	}
	report, err := h.service.PublishListingDraft(r.Context(), draft)
	if err != nil {
		http.Error(w, "failed to publish listing: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, report)
}

func (h *APIHandler) handleListingByID(w http.ResponseWriter, r *http.Request) {
	listingID := extractPathParam(r.URL.Path, "/api/storefront/listings/")
	if listingID == "" {
		http.Error(w, "listing ID required", http.StatusBadRequest)
		return
	}

	// Check for sub-paths like /reviews
	parts := strings.SplitN(listingID, "/", 2)
	listingID = parts[0]

	if len(parts) > 1 && parts[1] == "reviews" {
		h.handleListingReviews(w, r, listingID)
		return
	}

	switch r.Method {
	case http.MethodGet:
		listing, err := h.service.GetListing(r.Context(), listingID)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if listing == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, listing)

	case http.MethodDelete:
		if err := h.service.store.UpdateListingActive(listingID, false); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodPatch:
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		var updates Listing
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		// Fetch existing and apply updates, simplified
		existing, err := h.service.GetListing(r.Context(), listingID)
		if err != nil || existing == nil {
			http.Error(w, "listing not found", http.StatusNotFound)
			return
		}
		if updates.Title != "" {
			existing.Title = updates.Title
		}
		if updates.Description != "" {
			existing.Description = updates.Description
		}
		writeJSON(w, http.StatusOK, existing)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) handleListingReviews(w http.ResponseWriter, r *http.Request, listingID string) {
	limit := queryInt(r, "limit", 20)
	offset := queryInt(r, "offset", 0)

	reviews, stats, err := h.service.GetListingReviews(r.Context(), listingID, limit, offset)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"reviews": reviews,
		"stats":   stats,
	})
}

func (h *APIHandler) handleCreatePurchase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req PurchaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ListingID) == "" {
		http.Error(w, "listing_id is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.TierName) == "" {
		http.Error(w, "tier_name is required", http.StatusBadRequest)
		return
	}

	if err := h.service.CreatePurchaseRequest(r.Context(), &req); err != nil {
		http.Error(w, "failed to create purchase", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, req)
}

func (h *APIHandler) handlePurchaseByID(w http.ResponseWriter, r *http.Request) {
	path := extractPathParam(r.URL.Path, "/api/storefront/purchases/")
	parts := strings.SplitN(path, "/", 2)
	requestID := parts[0]

	if len(parts) > 1 {
		switch parts[1] {
		case "confirm":
			h.handleConfirmPayment(w, r, requestID)
			return
		case "pay-credits":
			h.handlePayWithCredits(w, r, requestID)
			return
		case "pay-fiat":
			h.handlePayWithFiat(w, r, requestID)
			return
		case "pay-crypto":
			h.handleCreateCryptoIntent(w, r, requestID)
			return
		case "manual-dev-paid":
			h.handleManualDevPaid(w, r, requestID)
			return
		case "audit":
			h.handlePurchaseAudit(w, r, requestID)
			return
		}
	}

	// GET purchase by ID
	purchase, err := h.service.store.GetPurchaseRequest(requestID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if purchase == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, purchase)
}

// handleManualDevPaid marks a purchase paid out of band with no real
// payment evidence — it exists only for local/dev environments where no
// checkout provider is configured. It is gated behind BOTH an explicit
// opt-in env flag (off by default; see DevPaymentsEnvVar) AND a
// caller-ownership/admin check, so that in any production deployment that
// hasn't deliberately set the flag, this always fails closed regardless of
// who calls it or what trust level they hold.
func (h *APIHandler) handleManualDevPaid(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !devPaymentsEnabled() {
		// Deliberately indistinguishable from a route that was never
		// registered: this endpoint must not be reachable, or even
		// discoverable, in a production config.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	purchase, err := h.service.store.GetPurchaseRequest(requestID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if purchase == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if _, ok := authorizeStorefrontPeerQuery(w, r, purchase.BuyerPeerID, "buyer_peer_id"); !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body ManualDevPaymentConfirmation
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	grant, err := h.service.CompleteManualDevPayment(r.Context(), requestID, body)
	if err != nil {
		http.Error(w, "failed to complete manual/dev payment", http.StatusInternalServerError)
		return
	}
	purchase, err = h.service.store.GetPurchaseRequest(requestID)
	if err != nil {
		http.Error(w, "failed to load purchase", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mode":     "manual-dev",
		"purchase": purchase,
		"grant":    grant,
	})
}

func (h *APIHandler) handlePurchaseAudit(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	events, err := h.service.store.GetPaymentAuditEvents(requestID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": events,
	})
}

// handleConfirmPayment confirms a crypto payment. Every confirmation MUST
// resolve to a signed CryptoBuyerIntent (identified by "reference") and pass
// full chain verification via SubmitCryptoPayment/validateVerifiedCryptoPayment
// (B4) — there is no reference-less fallback path anymore. The previous
// behavior of auto-confirming (and issuing a grant for) a bare txHash/chain
// pair with no intent to check it against has been removed entirely: that
// path never verified recipient, amount, or asset, so it amounted to "any
// caller who can produce a request_id gets a free grant."
func (h *APIHandler) handleConfirmPayment(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body struct {
		TxHash           string `json:"txHash"`
		Chain            string `json:"chain"`
		SenderAddress    string `json:"senderAddress"`
		RecipientAddress string `json:"recipientAddress"`
		Reference        string `json:"reference"`
		Amount           uint64 `json:"amount"`
		Currency         string `json:"currency"`
		AssetContract    string `json:"assetContract"`
		NativeAsset      bool   `json:"nativeAsset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Reference) == "" {
		http.Error(w, "payment reference required", http.StatusBadRequest)
		return
	}
	if h.payment == nil {
		http.Error(w, "crypto payments not configured", http.StatusServiceUnavailable)
		return
	}

	req := &CryptoPaymentRequest{
		RequestID:        requestID,
		TxHash:           body.TxHash,
		Chain:            body.Chain,
		SenderAddress:    body.SenderAddress,
		RecipientAddress: body.RecipientAddress,
		Reference:        body.Reference,
		Amount:           body.Amount,
		Currency:         body.Currency,
		AssetContract:    body.AssetContract,
		NativeAsset:      body.NativeAsset,
	}
	result, err := h.payment.SubmitCryptoPayment(r.Context(), req)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !result.Verified {
		http.Error(w, "payment not verified: "+result.Error, http.StatusPaymentRequired)
		return
	}
	grant, err := h.service.CompleteCryptoPayment(r.Context(), requestID, result)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"payment": result,
		"grant":   grant,
	})
}

// handleCreateCryptoIntent creates the server-authored payment intent a
// buyer must pay against. Like handleManualDevPaid, it requires the caller
// to either own the purchase (match its BuyerPeerID) or hold peers.Admin
// trust — a crypto payment intent authorizes the caller to see (and, before
// the recipient-derivation fix in CreateCryptoBuyerIntent, to influence) the
// exact payment terms for a purchase, so it must not be creatable/readable
// by an arbitrary caller who merely knows another buyer's request_id.
func (h *APIHandler) handleCreateCryptoIntent(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.payment == nil {
		http.Error(w, "crypto payments not configured", http.StatusServiceUnavailable)
		return
	}

	purchase, err := h.service.store.GetPurchaseRequest(requestID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if purchase == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if _, ok := authorizeStorefrontPeerQuery(w, r, purchase.BuyerPeerID, "buyer_peer_id"); !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body CreateCryptoIntentRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.RequestID = requestID

	intent, err := h.payment.CreateCryptoBuyerIntent(r.Context(), &body)
	if err != nil {
		http.Error(w, "failed to create crypto intent: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, intent)
}

func (h *APIHandler) handlePayWithCredits(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	purchase, err := h.service.store.GetPurchaseRequest(requestID)
	if err != nil || purchase == nil {
		http.Error(w, "purchase not found", http.StatusNotFound)
		return
	}
	if purchase.Status == PurchaseStatusCompleted && purchase.GrantID != "" {
		grant, err := h.service.store.GetGrant(purchase.GrantID)
		if err == nil && grant != nil {
			writeJSON(w, http.StatusOK, grant)
			return
		}
	}

	if h.payment != nil {
		err = h.payment.ProcessCredits(r.Context(), requestID, purchase.BuyerPeerID, purchase.PaymentAmount, purchase.ProviderPeerID)
	} else {
		err = h.service.ProcessCreditsPayment(r.Context(), requestID, purchase.BuyerPeerID)
	}
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	grant, err := h.service.CompleteCreditsPayment(r.Context(), requestID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, grant)
}

func (h *APIHandler) handlePayWithFiat(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.payment == nil {
		http.Error(w, "fiat payments not configured", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body FiatGatewayRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.RequestID = requestID

	result, err := h.payment.CreateFiatPaymentIntent(r.Context(), &body)
	if err != nil {
		http.Error(w, "failed to create payment intent", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *APIHandler) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.payment == nil {
		http.Error(w, "payments not configured", http.StatusServiceUnavailable)
		return
	}

	payload, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	action, err := h.payment.HandleStripeWebhook(r.Context(), r.Header.Get("Stripe-Signature"), payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if action != nil && h.service != nil {
		if _, err := h.service.ApplyStripeWebhookAction(r.Context(), action); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"received": true,
		"action":   action,
	})
}

func (h *APIHandler) handleGrants(w http.ResponseWriter, r *http.Request) {
	buyerID := r.URL.Query().Get("buyer")
	if buyerID != "" {
		var ok bool
		buyerID, ok = authorizeStorefrontPeerQuery(w, r, buyerID, "buyer")
		if !ok {
			return
		}
		grants, err := h.service.GetBuyerGrants(r.Context(), buyerID)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, grants)
		return
	}

	providerID := r.URL.Query().Get("provider")
	if providerID != "" {
		var ok bool
		providerID, ok = authorizeStorefrontPeerQuery(w, r, providerID, "provider")
		if !ok {
			return
		}
		limit := queryInt(r, "limit", 50)
		offset := queryInt(r, "offset", 0)
		grants, total, err := h.service.store.GetProviderGrants(providerID, limit, offset)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"grants": grants,
			"total":  total,
		})
		return
	}

	http.Error(w, "buyer or provider query param required", http.StatusBadRequest)
}

func (h *APIHandler) handleGrantByID(w http.ResponseWriter, r *http.Request) {
	grantID := extractPathParam(r.URL.Path, "/api/storefront/grants/")

	// Check for verify subpath
	parts := strings.SplitN(grantID, "/", 2)
	grantID = parts[0]

	if len(parts) > 1 && parts[1] == "verify" {
		buyerID := r.URL.Query().Get("buyer")
		var ok bool
		buyerID, ok = authorizeStorefrontPeerQuery(w, r, buyerID, "buyer")
		if !ok {
			return
		}
		grant, err := h.service.VerifyGrant(r.Context(), grantID, buyerID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		writeJSON(w, http.StatusOK, grant)
		return
	}

	grant, err := h.service.store.GetGrant(grantID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if grant == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (h *APIHandler) handleCreateReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var review Review
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// Validate rating
	if review.Rating < 1 || review.Rating > 5 {
		http.Error(w, "rating must be between 1 and 5", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(review.ListingID) == "" {
		http.Error(w, "listing_id is required", http.StatusBadRequest)
		return
	}

	if err := h.service.CreateReview(r.Context(), &review); err != nil {
		http.Error(w, "failed to create review", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, review)
}

func (h *APIHandler) handleReviewByID(w http.ResponseWriter, r *http.Request) {
	path := extractPathParam(r.URL.Path, "/api/storefront/reviews/")
	parts := strings.SplitN(path, "/", 2)
	reviewID := parts[0]

	if len(parts) > 1 && parts[1] == "vote" {
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var body struct {
			Helpful bool `json:"helpful"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := h.service.store.UpdateReviewVote(reviewID, body.Helpful); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if len(parts) > 1 && parts[1] == "respond" {
		r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
		var body struct {
			Response string `json:"response"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := h.service.store.AddProviderResponse(reviewID, body.Response); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
}

func (h *APIHandler) handleCredits(w http.ResponseWriter, r *http.Request) {
	path := extractPathParam(r.URL.Path, "/api/storefront/credits/")
	parts := strings.SplitN(path, "/", 2)

	if parts[0] == "purchase" {
		// Purchase credits
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var body struct {
			Amount        uint64        `json:"amount"`
			PaymentMethod PaymentMethod `json:"paymentMethod"`
			PeerID        string        `json:"peerId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		// Stub: return payment target for credits purchase
		writeJSON(w, http.StatusOK, map[string]string{
			"paymentTarget": "sdn-credits-payment-address",
		})
		return
	}

	peerID := parts[0]

	if len(parts) > 1 && parts[1] == "transactions" {
		limit := queryInt(r, "limit", 50)
		offset := queryInt(r, "offset", 0)
		txs, err := h.service.store.GetCreditsTransactions(peerID, limit, offset)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, txs)
		return
	}

	balance, err := h.service.GetCreditsBalance(r.Context(), peerID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, balance)
}

func (h *APIHandler) handleTrust(w http.ResponseWriter, r *http.Request) {
	peerID := extractPathParam(r.URL.Path, "/api/storefront/trust/")
	if peerID == "" {
		http.Error(w, "peer ID required", http.StatusBadRequest)
		return
	}

	if h.trust == nil {
		http.Error(w, "trust scoring not configured", http.StatusServiceUnavailable)
		return
	}

	score, err := h.trust.ComputeProviderTrust(peerID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, score)
}

// SellerDashboardResponse represents the seller dashboard data
type SellerDashboardResponse struct {
	Listings         []Listing                 `json:"listings"`
	TotalListings    int                       `json:"total_listings"`
	ActiveGrants     int                       `json:"active_grants"`
	TotalEarnings    uint64                    `json:"total_earnings"`
	RecentPurchases  []*PurchaseRequest        `json:"recent_purchases"`
	Grants           []*AccessGrant            `json:"grants"`
	Deliveries       []DashboardDeliveryRecord `json:"deliveries"`
	DeliveryFailures []DashboardDeliveryRecord `json:"delivery_failures"`
	TrustScore       *TrustScore               `json:"trust_score,omitempty"`
	CreditsBalance   *CreditsBalance           `json:"credits_balance"`
}

func (h *APIHandler) handleSellerDashboard(w http.ResponseWriter, r *http.Request) {
	providerID := r.URL.Query().Get("peerId")
	var ok bool
	providerID, ok = authorizeStorefrontPeerQuery(w, r, providerID, "peerId")
	if !ok {
		return
	}

	// Get listings
	listingsResult, err := h.service.GetProviderListings(r.Context(), providerID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Get grants
	grants, totalGrants, _ := h.service.store.GetProviderGrants(providerID, 100, 0)

	// Get earnings
	earnings, _ := h.service.store.GetProviderEarnings(providerID)

	// Get recent purchases
	purchases, _, _ := h.service.store.GetProviderPurchases(providerID, 10, 0)

	// Get trust score
	var trustScore *TrustScore
	if h.trust != nil {
		trustScore, _ = h.trust.ComputeProviderTrust(providerID)
	}

	// Get credits balance
	balance, _ := h.service.GetCreditsBalance(r.Context(), providerID)

	writeJSON(w, http.StatusOK, SellerDashboardResponse{
		Listings:         listingsResult.Listings,
		TotalListings:    listingsResult.Total,
		ActiveGrants:     totalGrants,
		TotalEarnings:    earnings,
		RecentPurchases:  purchases,
		Grants:           grants,
		Deliveries:       dashboardDeliveries(grants, listingsResult.Listings),
		DeliveryFailures: dashboardDeliveryFailures(grants, listingsResult.Listings),
		TrustScore:       trustScore,
		CreditsBalance:   balance,
	})
}

// BuyerDashboardResponse represents the buyer dashboard data
type BuyerDashboardResponse struct {
	ActiveGrants []*AccessGrant `json:"active_grants"`
	Grants       []*AccessGrant `json:"grants"`
	// Listings is the grant -> listing join this handler ALREADY performs in
	// order to derive Deliveries. It used to be computed and thrown away, which
	// left the client re-fetching, one request per grant, exactly the rows the
	// server had just read. Same key as SellerDashboardResponse.Listings, and
	// joined by ListingID the same way dashboardDeliveries joins it.
	//
	// A listing can repeat when a buyer holds more than one grant against it;
	// callers index by ListingID rather than iterating positionally.
	Listings        []Listing                 `json:"listings"`
	TotalGrants     int                       `json:"total_grants"`
	RecentPurchases []*PurchaseRequest        `json:"recent_purchases,omitempty"`
	Purchases       []*PurchaseRequest        `json:"purchases,omitempty"`
	Deliveries      []DashboardDeliveryRecord `json:"deliveries"`
	CreditsBalance  *CreditsBalance           `json:"credits_balance"`
}

func (h *APIHandler) handleBuyerDashboard(w http.ResponseWriter, r *http.Request) {
	buyerID := r.URL.Query().Get("peerId")
	var ok bool
	buyerID, ok = authorizeStorefrontPeerQuery(w, r, buyerID, "peerId")
	if !ok {
		return
	}

	grants, err := h.service.GetBuyerGrants(r.Context(), buyerID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	balance, _ := h.service.GetCreditsBalance(r.Context(), buyerID)
	purchases, _, _ := h.service.store.GetBuyerPurchases(buyerID, 50, 0)
	listings := make([]Listing, 0, len(grants))
	for _, grant := range grants {
		listing, err := h.service.GetListing(r.Context(), grant.ListingID)
		if err == nil && listing != nil {
			listings = append(listings, *listing)
		}
	}

	writeJSON(w, http.StatusOK, BuyerDashboardResponse{
		ActiveGrants:    grants,
		Grants:          grants,
		Listings:        listings,
		TotalGrants:     len(grants),
		RecentPurchases: purchases,
		Purchases:       purchases,
		Deliveries:      dashboardDeliveries(grants, listings),
		CreditsBalance:  balance,
	})
}

// DashboardDeliveryRecord is a derived dashboard view over grant and protected delivery metadata.
type DashboardDeliveryRecord struct {
	ID               string `json:"id"`
	ListingID        string `json:"listing_id"`
	GrantID          string `json:"grant_id"`
	BuyerPeerID      string `json:"buyer_peer_id,omitempty"`
	ProviderPeerID   string `json:"provider_peer_id,omitempty"`
	Status           string `json:"status"`
	KeyWrapStatus    string `json:"key_wrap_status"`
	EncryptedCID     string `json:"encrypted_cid,omitempty"`
	ManifestCID      string `json:"manifest_cid,omitempty"`
	ContentHash      string `json:"content_hash,omitempty"`
	DeliveryTopic    string `json:"delivery_topic,omitempty"`
	FailureReason    string `json:"reason,omitempty"`
	DeliveryProtocol string `json:"delivery_protocol,omitempty"`
}

type AdminDashboardResponse struct {
	Moderation     []*Review          `json:"moderation"`
	Trust          []*TrustScore      `json:"trust"`
	PaymentHolds   []*PurchaseRequest `json:"payment_holds"`
	Disputes       []*Review          `json:"disputes"`
	FailedPayments []*PurchaseRequest `json:"failed_payments"`
}

func (h *APIHandler) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	if session := auth.SessionFromContext(r.Context()); session != nil && session.TrustLevel < peers.Admin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	moderation, _, _ := h.service.store.GetModerationReviews(100, 0)
	holds, _, _ := h.service.store.GetPurchasesByStatuses([]PurchaseStatus{
		PurchaseStatusRefundRequested,
		PurchaseStatusRefunded,
	}, 100, 0)
	failed, _, _ := h.service.store.GetPurchasesByStatuses([]PurchaseStatus{
		PurchaseStatusFailed,
		PurchaseStatusCancelled,
		PurchaseStatusExpired,
	}, 100, 0)
	disputes := make([]*Review, 0)
	for _, review := range moderation {
		if review.FlaggedCount > 0 || review.Status == ReviewStatusFlagged {
			disputes = append(disputes, review)
		}
	}
	var trustScores []*TrustScore
	if h.trust != nil {
		listings, err := h.service.SearchListings(r.Context(), &SearchQuery{Limit: 100})
		if err == nil {
			seen := make(map[string]struct{})
			for _, listing := range listings.Listings {
				if _, ok := seen[listing.ProviderPeerID]; ok {
					continue
				}
				seen[listing.ProviderPeerID] = struct{}{}
				score, err := h.trust.ComputeProviderTrust(listing.ProviderPeerID)
				if err == nil && score != nil {
					trustScores = append(trustScores, score)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, AdminDashboardResponse{
		Moderation:     moderation,
		Trust:          trustScores,
		PaymentHolds:   holds,
		Disputes:       disputes,
		FailedPayments: failed,
	})
}

// Helper functions

func authorizeStorefrontPeerQuery(w http.ResponseWriter, r *http.Request, requestedPeerID string, fieldName string) (string, bool) {
	requestedPeerID = strings.TrimSpace(requestedPeerID)
	session := auth.SessionFromContext(r.Context())
	if session == nil {
		if requestedPeerID == "" {
			http.Error(w, fieldName+" required", http.StatusBadRequest)
			return "", false
		}
		return requestedPeerID, true
	}
	if requestedPeerID == "" {
		requestedPeerID = session.XPub
	}
	if session.TrustLevel >= peers.Admin || requestedPeerID == session.XPub {
		return requestedPeerID, true
	}
	http.Error(w, "forbidden: cannot access another peer's storefront data", http.StatusForbidden)
	return "", false
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func dashboardDeliveries(grants []*AccessGrant, listings []Listing) []DashboardDeliveryRecord {
	listingsByID := make(map[string]Listing, len(listings))
	for _, listing := range listings {
		listingsByID[listing.ListingID] = listing
	}
	records := make([]DashboardDeliveryRecord, 0, len(grants))
	for _, grant := range grants {
		if grant == nil {
			continue
		}
		listing := listingsByID[grant.ListingID]
		protected := listing.ProtectedDelivery
		status := "grant_active"
		if protected.EncryptedCID != "" || protected.ManifestCID != "" {
			status = "encrypted_ready"
		}
		keyWrapStatus := "pending"
		if len(grant.ProviderSignature) > 0 || grant.Status == GrantStatusActive {
			keyWrapStatus = "issued"
		}
		records = append(records, DashboardDeliveryRecord{
			ID:               "delivery:" + grant.GrantID,
			ListingID:        grant.ListingID,
			GrantID:          grant.GrantID,
			BuyerPeerID:      grant.BuyerPeerID,
			ProviderPeerID:   grant.ProviderPeerID,
			Status:           status,
			KeyWrapStatus:    keyWrapStatus,
			EncryptedCID:     protected.EncryptedCID,
			ManifestCID:      protected.ManifestCID,
			ContentHash:      protected.ContentHash,
			DeliveryTopic:    grant.DeliveryTopic,
			DeliveryProtocol: protected.DeliveryProtocol,
		})
	}
	return records
}

func dashboardDeliveryFailures(grants []*AccessGrant, listings []Listing) []DashboardDeliveryRecord {
	deliveries := dashboardDeliveries(grants, listings)
	failures := make([]DashboardDeliveryRecord, 0)
	for _, delivery := range deliveries {
		if delivery.Status == "encrypted_ready" && delivery.KeyWrapStatus == "issued" {
			continue
		}
		delivery.FailureReason = "delivery_not_ready"
		failures = append(failures, delivery)
	}
	return failures
}

func extractPathParam(path, prefix string) string {
	return strings.TrimPrefix(path, prefix)
}

func queryInt(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return i
}
