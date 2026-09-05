package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
)

type DownloadHandler struct {
	db      *db.DB
	storage storage.StorageBackend
}

func NewDownloadHandler(database *db.DB, store storage.StorageBackend) *DownloadHandler {
	return &DownloadHandler{
		db:      database,
		storage: store,
	}
}

func (h *DownloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	productID := r.PathValue("product")
	if productID == "" {
		JSONError(w, http.StatusBadRequest, "bad_request", "Missing product identifier in route")
		return
	}

	// 1. Fetch product
	product, err := h.db.GetProduct(r.Context(), productID)
	if err != nil {
		if errors.Is(err, db.ErrProductNotFound) {
			JSONError(w, http.StatusNotFound, "product_not_found", fmt.Sprintf("Product %q not found", productID))
			return
		}
		JSONError(w, http.StatusInternalServerError, "database_error", "Failed to query product")
		return
	}

	// 2. Fetch target version (specific version or active current version)
	var version *db.ProductVersion
	versionStr := r.URL.Query().Get("version")
	if versionStr == "" {
		if dt, ok := r.Context().Value(DownloadTokenContextKey).(*db.DownloadToken); ok && dt != nil && dt.Version != "" {
			versionStr = dt.Version
		}
	}

	if versionStr != "" {
		version, err = h.db.GetProductVersion(r.Context(), productID, versionStr)
		if err != nil {
			if errors.Is(err, db.ErrVersionNotFound) {
				JSONError(w, http.StatusNotFound, "version_not_found", fmt.Sprintf("Version %q not found for product %q", versionStr, productID))
				return
			}
			JSONError(w, http.StatusInternalServerError, "database_error", "Failed to query product version")
			return
		}
	} else {
		version, err = h.db.GetCurrentVersion(r.Context(), productID)
		if err != nil {
			if errors.Is(err, db.ErrVersionNotFound) {
				JSONError(w, http.StatusNotFound, "version_not_found", fmt.Sprintf("No active version published for product %q", productID))
				return
			}
			JSONError(w, http.StatusInternalServerError, "database_error", "Failed to query current version")
			return
		}
	}

	if version.IsDeleted || version.StoragePath == "" {
		JSONError(w, http.StatusNotFound, "artifact_missing", "Artifact file is no longer available in storage")
		return
	}

	etag := fmt.Sprintf(`"%s"`, version.SHA256)

	// 3. Conditional validation: If-None-Match
	inm := r.Header.Get("If-None-Match")
	if inm != "" {
		for _, tag := range strings.Split(inm, ",") {
			if strings.TrimSpace(tag) == etag || strings.TrimSpace(tag) == "*" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}

	// 4. Conditional validation: If-Modified-Since
	ims := r.Header.Get("If-Modified-Since")
	if ims != "" && inm == "" {
		if t, err := http.ParseTime(ims); err == nil {
			if !version.ReleasedAt.After(t) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}

	// 5. Open artifact from storage
	stream, _, err := h.storage.OpenArtifact(r.Context(), version.StoragePath)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			JSONError(w, http.StatusNotFound, "artifact_missing", "Artifact file is missing from underlying storage")
			return
		}
		JSONError(w, http.StatusInternalServerError, "storage_error", "Failed to open artifact stream")
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, product.ArtifactFilename))
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")

	// 6. Serve with Range support
	http.ServeContent(w, r, product.ArtifactFilename, version.ReleasedAt, stream)
}
