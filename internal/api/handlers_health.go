package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/db"
)

type HealthHandler struct {
	db *db.DB
}

func NewHealthHandler(database *db.DB) *HealthHandler {
	return &HealthHandler{db: database}
}

func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "alive"})
}

func (h *HealthHandler) Readiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// 1. Check database connectivity
	if err := h.db.PingContext(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "unready",
			"error":  "database ping failed",
		})
		return
	}

	// 2. Check product staleness
	products, err := h.db.ListProducts(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "unready",
			"error":  "failed to query products",
		})
		return
	}

	var staleProducts []string
	now := time.Now().UTC()
	for _, p := range products {
		if !p.IsEnabled {
			continue
		}
		cur, err := h.db.GetCurrentVersion(r.Context(), p.ID)
		if err != nil || cur == nil {
			staleProducts = append(staleProducts, p.ID+" (missing)")
			continue
		}
		age := now.Sub(cur.ReleasedAt)
		if age > time.Duration(p.StalenessDays)*24*time.Hour {
			staleProducts = append(staleProducts, p.ID+" (stale)")
		}
	}

	if len(staleProducts) > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":         "degraded",
			"stale_products": staleProducts,
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ready",
	})
}

func (h *HealthHandler) Status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	products, err := h.db.ListProducts(r.Context())
	if err != nil {
		JSONError(w, http.StatusInternalServerError, "error", "Failed to retrieve products")
		return
	}

	type productStatus struct {
		ID        string     `json:"id"`
		Enabled   bool       `json:"enabled"`
		Version   string     `json:"version,omitempty"`
		Released  *time.Time `json:"released_at,omitempty"`
		SizeBytes int64      `json:"size_bytes,omitempty"`
	}

	var list []productStatus
	for _, p := range products {
		ps := productStatus{
			ID:      p.ID,
			Enabled: p.IsEnabled,
		}
		cur, err := h.db.GetCurrentVersion(r.Context(), p.ID)
		if err == nil && cur != nil {
			ps.Version = cur.Version
			ps.Released = &cur.ReleasedAt
			ps.SizeBytes = cur.SizeBytes
		}
		list = append(list, ps)
	}

	storageBackend, _, _ := h.db.GetSetting(r.Context(), "storage_backend")

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"service":         "DistriMax",
		"storage_backend": storageBackend,
		"products":        list,
		"server_time":     time.Now().UTC(),
	})
}
