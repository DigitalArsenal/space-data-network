package storefront

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

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

// RegisterRoutes registers the storefront HTTP routes on a mux
func (h *APIHandler) RegisterRoutes(mux *http.ServeMux) {
	// Listings
	mux.HandleFunc("/api/storefront/listings", h.handleListings)
	mux.HandleFunc("/api/storefront/listings/search", h.handleSearchListings)
	mux.HandleFunc("/api/storefront/listings/", h.handleListingByID)

	// Purchases
	mux.HandleFunc("/api/storefront/purchases", h.handleCreatePurchase)
	mux.HandleFunc("/api/storefront/purchases/", h.handlePurchaseByID)

	// Grants
	mux.HandleFunc("/api/storefront/grants", h.handleGrants)
	mux.HandleFunc("/api/storefront/grants/", h.handleGrantByID)

	// Reviews
	mux.HandleFunc("/api/storefront/reviews", h.handleCreateReview)
	mux.HandleFunc("/api/storefront/reviews/", h.handleReviewByID)

	// Credits
	mux.HandleFunc("/api/storefront/credits/", h.handleCredits)

	// Trust
	mux.HandleFunc("/api/storefront/trust/", h.handleTrust)

	// Seller dashboard
	mux.HandleFunc("/api/storefront/dashboard/seller", h.handleSellerDashboard)

	// Buyer dashboard
	mux.HandleFunc("/api/storefront/dashboard/buyer", h.handleBuyerDashboard)

	// Stripe webhook
	mux.HandleFunc("/api/storefront/payments/stripe/webhook", h.handleStripeWebhook)
}

func (h *APIHandler) handleListings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var listing Listing
		if err := json.NewDecoder(r.Body).Decode(&listing); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := h.service.CreateListing(r.Context(), &listing); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		// Default: return all active listings
		result, err := h.service.SearchListings(r.Context(), &SearchQuery{Limit: 50})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) handleSearchListings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var query SearchQuery
	if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
		http.Error(w, "invalid query", http.StatusBadRequest)
		return
	}

	result, err := h.service.SearchListings(r.Context(), &query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, result)
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if listing == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, listing)

	case http.MethodDelete:
		if err := h.service.store.UpdateListingActive(listingID, false); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodPatch:
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	var req PurchaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if err := h.service.CreatePurchaseRequest(r.Context(), &req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		}
	}

	// GET purchase by ID
	purchase, err := h.service.store.GetPurchaseRequest(requestID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if purchase == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, purchase)
}

func (h *APIHandler) handleConfirmPayment(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		TxHash        string `json:"txHash"`
		Chain         string `json:"chain"`
		SenderAddress string `json:"senderAddress"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	if h.payment != nil {
		result, err := h.payment.VerifyCryptoPayment(r.Context(), &CryptoPaymentRequest{
			RequestID:     requestID,
			TxHash:        body.TxHash,
			Chain:         body.Chain,
			SenderAddress: body.SenderAddress,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !result.Verified {
			http.Error(w, "payment not verified: "+result.Error, http.StatusPaymentRequired)
			return
		}
	}

	if err := h.service.ProcessPayment(r.Context(), requestID, body.TxHash, body.Chain); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
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

	if h.payment != nil {
		err = h.payment.ProcessCredits(r.Context(), requestID, purchase.BuyerPeerID, purchase.PaymentAmount, purchase.ProviderPeerID)
	} else {
		err = h.service.ProcessCreditsPayment(r.Context(), requestID, purchase.BuyerPeerID)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Issue grant
	grant, err := h.service.IssueGrant(r.Context(), requestID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	var body FiatGatewayRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.RequestID = requestID

	result, err := h.payment.CreateFiatPaymentIntent(r.Context(), &body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	if action != nil && action.Paid && action.RequestID != "" && h.service != nil {
		if _, err := h.service.CompleteStripeCheckout(r.Context(), action.RequestID, action.SessionID, action.SubscriptionID, action.CustomerID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
		grants, err := h.service.GetBuyerGrants(r.Context(), buyerID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, grants)
		return
	}

	providerID := r.URL.Query().Get("provider")
	if providerID != "" {
		limit := queryInt(r, "limit", 50)
		offset := queryInt(r, "offset", 0)
		grants, total, err := h.service.store.GetProviderGrants(providerID, limit, offset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	var review Review
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	if err := h.service.CreateReview(r.Context(), &review); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, review)
}

func (h *APIHandler) handleReviewByID(w http.ResponseWriter, r *http.Request) {
	path := extractPathParam(r.URL.Path, "/api/storefront/reviews/")
	parts := strings.SplitN(path, "/", 2)
	reviewID := parts[0]

	if len(parts) > 1 && parts[1] == "vote" {
		var body struct {
			Helpful bool `json:"helpful"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := h.service.store.UpdateReviewVote(reviewID, body.Helpful); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if len(parts) > 1 && parts[1] == "respond" {
		var body struct {
			Response string `json:"response"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := h.service.store.AddProviderResponse(reviewID, body.Response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, txs)
		return
	}

	balance, err := h.service.GetCreditsBalance(r.Context(), peerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, score)
}

// SellerDashboardResponse represents the seller dashboard data
type SellerDashboardResponse struct {
	Listings        []Listing          `json:"listings"`
	TotalListings   int                `json:"total_listings"`
	ActiveGrants    int                `json:"active_grants"`
	TotalEarnings   uint64             `json:"total_earnings"`
	RecentPurchases []*PurchaseRequest `json:"recent_purchases"`
	TrustScore      *TrustScore        `json:"trust_score,omitempty"`
	CreditsBalance  *CreditsBalance    `json:"credits_balance"`
}

func (h *APIHandler) handleSellerDashboard(w http.ResponseWriter, r *http.Request) {
	providerID := r.URL.Query().Get("peerId")
	if providerID == "" {
		http.Error(w, "peerId required", http.StatusBadRequest)
		return
	}

	// Get listings
	listingsResult, err := h.service.GetProviderListings(r.Context(), providerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get grants
	grants, totalGrants, _ := h.service.store.GetProviderGrants(providerID, 1, 0)
	_ = grants

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
		Listings:        listingsResult.Listings,
		TotalListings:   listingsResult.Total,
		ActiveGrants:    totalGrants,
		TotalEarnings:   earnings,
		RecentPurchases: purchases,
		TrustScore:      trustScore,
		CreditsBalance:  balance,
	})
}

// BuyerDashboardResponse represents the buyer dashboard data
type BuyerDashboardResponse struct {
	ActiveGrants    []*AccessGrant     `json:"active_grants"`
	TotalGrants     int                `json:"total_grants"`
	RecentPurchases []*PurchaseRequest `json:"recent_purchases,omitempty"`
	CreditsBalance  *CreditsBalance    `json:"credits_balance"`
}

func (h *APIHandler) handleBuyerDashboard(w http.ResponseWriter, r *http.Request) {
	buyerID := r.URL.Query().Get("peerId")
	if buyerID == "" {
		http.Error(w, "peerId required", http.StatusBadRequest)
		return
	}

	grants, err := h.service.GetBuyerGrants(r.Context(), buyerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	balance, _ := h.service.GetCreditsBalance(r.Context(), buyerID)

	writeJSON(w, http.StatusOK, BuyerDashboardResponse{
		ActiveGrants:   grants,
		TotalGrants:    len(grants),
		CreditsBalance: balance,
	})
}

// Helper functions

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
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
