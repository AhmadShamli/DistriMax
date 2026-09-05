package api

import (
	"net/http"

	"github.com/AhmadShamli/DistriMax/internal/cache"
	"github.com/AhmadShamli/DistriMax/internal/config"
	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
)

// Server coordinates the API router, middleware, and handlers.
type Server struct {
	router      *http.ServeMux
	db          *db.DB
	cfg         *config.Config
	storage     storage.StorageBackend
	cache       *cache.ManifestCache
	limiter     *ConcurrencyLimiter
}

func NewServer(cfg *config.Config, database *db.DB, store storage.StorageBackend, manifestCache *cache.ManifestCache) *Server {
	s := &Server{
		router:  http.NewServeMux(),
		db:      database,
		cfg:     cfg,
		storage: store,
		cache:   manifestCache,
		limiter: NewConcurrencyLimiter(50),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	healthHandler := NewHealthHandler(s.db)
	manifestHandler := NewManifestHandler(s.db, s.cache)
	downloadHandler := NewDownloadHandler(s.db, s.storage)
	recoverHandler := NewRecoverHandler(s.db, s.cfg.RecoverySecret)

	// Health Endpoints (Unauthenticated for k8s / load balancers)
	s.router.HandleFunc("GET /health/liveness", healthHandler.Liveness)
	s.router.HandleFunc("GET /health/readiness", healthHandler.Readiness)
	s.router.HandleFunc("GET /status", healthHandler.Status)

	// Break-glass recovery
	s.router.Handle("POST /recover", recoverHandler)

	// Client Download and Manifest API (Protected by API Key or Temporary Download Token)
	s.router.Handle("GET /v1/products/{product}/manifest", RequireAPIKey(s.db, "manifest:read")(manifestHandler))
	s.router.Handle("GET /v1/products/{product}/download", s.limiter.Limit(RequireDownloadAuth(s.db)(downloadHandler)))
}

func (s *Server) Handler() http.Handler {
	// Wrap all incoming requests in audit logging and proxy resolution
	return AuditLoggerMiddleware(s.db, s.cfg.IsTrustedProxy)(s.router)
}
