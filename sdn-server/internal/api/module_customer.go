package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
)

type moduleCustomer interface {
	ModuleCustomerCatalog() (json.RawMessage, error)
	TestPurchaseModule(context.Context, json.RawMessage) (json.RawMessage, error)
}

func (h *CoreAPIHandler) registerModuleCustomerRoutes(mux *http.ServeMux) {
	customer, ok := h.publisher.(moduleCustomer)
	if !ok {
		return
	}
	mux.HandleFunc("/api/v1/modules/customer", h.withRL(h.requireAdminStrict(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if os.Getenv("SDN_STOREFRONT_DEV_PAYMENTS") != "1" {
			http.NotFound(w, r)
			return
		}
		var result json.RawMessage
		var err error
		switch r.Method {
		case http.MethodGet:
			result, err = customer.ModuleCustomerCatalog()
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			var raw []byte
			raw, err = io.ReadAll(r.Body)
			if err == nil {
				result, err = customer.TestPurchaseModule(r.Context(), raw)
			}
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "module_delivery_failed", "message": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(result)
	})))
}
