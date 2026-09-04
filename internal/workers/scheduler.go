package workers

import (
	"context"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
	"github.com/AhmadShamli/DistriMax/internal/syncer"
)

type Scheduler struct {
	db         *db.DB
	storage    storage.StorageBackend
	syncer     *syncer.Syncer
	backupRoot string
}

func NewScheduler(database *db.DB, store storage.StorageBackend, syncEngine *syncer.Syncer, backupRoot string) *Scheduler {
	return &Scheduler{
		db:         database,
		storage:    store,
		syncer:     syncEngine,
		backupRoot: backupRoot,
	}
}

// Start begins the background worker loops until ctx is canceled.
func (s *Scheduler) Start(ctx context.Context) {
	cleanupTicker := time.NewTicker(1 * time.Hour)
	syncTicker := time.NewTicker(24 * time.Hour)
	backupTicker := time.NewTicker(24 * time.Hour)

	defer cleanupTicker.Stop()
	defer syncTicker.Stop()
	defer backupTicker.Stop()

	// Run initial cleanup pass on startup
	_, _ = RunCleanup(ctx, s.db, s.storage)

	for {
		select {
		case <-ctx.Done():
			return

		case <-cleanupTicker.C:
			_, _ = RunCleanup(ctx, s.db, s.storage)

		case <-syncTicker.C:
			s.runScheduledSync(ctx)

		case <-backupTicker.C:
			_, _ = CreateBackup(ctx, s.db, s.backupRoot, nil, 7)
		}
	}
}

func (s *Scheduler) runScheduledSync(ctx context.Context) {
	products, err := s.db.ListProducts(ctx)
	if err != nil {
		return
	}
	for _, p := range products {
		if !p.IsEnabled {
			continue
		}
		_, _ = s.syncer.SyncProduct(ctx, p.ID, "SCHEDULED")
	}
}
