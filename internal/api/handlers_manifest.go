package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/AhmadShamli/DistriMax/internal/cache"
	"github.com/AhmadShamli/DistriMax/internal/db"
)

type ManifestHandler struct {
	db    *db.DB
	cache *cache.ManifestCache
}

func NewManifestHandler(database *db.DB, manifestCache *cache.ManifestCache) *ManifestHandler {
	return &ManifestHandler{
		db:    database,
		cache: manifestCache,
	}
}

func (h *ManifestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	productID := r.PathValue("product")
	if productID == "" {
		JSONError(w, http.StatusBadRequest, "bad_request", "Missing product identifier in route")
		return
	}

	// 1. Check in-memory cache
	if manifest, found := h.cache.Get(productID); found {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("ETag", fmt.Sprintf(`"%s"`, manifest.SHA256))
		_ = json.NewEncoder(w).Encode(manifest)
		return
	}

	// 2. Query database
	product, err := h.db.GetProduct(r.Context(), productID)
	if err != nil {
		if errors.Is(err, db.ErrProductNotFound) {
			JSONError(w, http.StatusNotFound, "product_not_found", fmt.Sprintf("Product %q not found", productID))
			return
		}
		JSONError(w, http.StatusInternalServerError, "database_error", "Failed to query product")
		return
	}

	version, err := h.db.GetCurrentVersion(r.Context(), productID)
	if err != nil {
		if errors.Is(err, db.ErrVersionNotFound) {
			JSONError(w, http.StatusNotFound, "version_not_found", fmt.Sprintf("No active version published for product %q", productID))
			return
		}
		JSONError(w, http.StatusInternalServerError, "database_error", "Failed to query current version")
		return
	}

	manifest := &cache.ProductManifest{
		Product:     product.ID,
		EditionID:   product.EditionID,
		Version:     version.Version,
		ReleasedAt:  version.ReleasedAt,
		SHA256:      version.SHA256,
		SizeBytes:   version.SizeBytes,
		Filename:    product.ArtifactFilename,
		DownloadURL: fmt.Sprintf("/v1/products/%s/download", product.ID),
	}

	// 3. Cache manifest
	h.cache.Set(productID, manifest)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", fmt.Sprintf(`"%s"`, manifest.SHA256))
	_ = json.NewEncoder(w).Encode(manifest)
}
