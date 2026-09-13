// Package worker contient les workers en arrière-plan de PXL.
//
// ThumbnailWorker génère les miniatures de manière asynchrone via un pool
// de goroutines. Le nombre de workers est configurable via PXL_THUMBNAIL_WORKERS.
package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/imaging"
	"github.com/HeartBtz/pxl/internal/metrics"
	"github.com/HeartBtz/pxl/internal/repository"
	"github.com/HeartBtz/pxl/internal/storage"
)

// ThumbnailWorker génère les miniatures des images uploadées de manière asynchrone.
// Utilise un canal pour recevoir les requêtes et un pool de workers pour le traitement.
type ThumbnailWorker struct {
	queue     chan *domain.Image
	imageRepo *repository.ImageRepository
	store     storage.Backend
	processor *imaging.Processor
	size      int
	log       *slog.Logger
}

func NewThumbnailWorker(
	ir *repository.ImageRepository,
	store storage.Backend,
	proc *imaging.Processor,
	size int,
	queueSize int,
	logger *slog.Logger,
) *ThumbnailWorker {
	return &ThumbnailWorker{
		queue:     make(chan *domain.Image, queueSize),
		imageRepo: ir,
		store:     store,
		processor: proc,
		size:      size,
		log:       logger,
	}
}

// Queue submits an image for thumbnail generation. Non-blocking.
func (w *ThumbnailWorker) Queue(img *domain.Image) {
	select {
	case w.queue <- img:
	default:
		w.log.Warn("thumbnail queue full, dropping", slog.String("short_id", img.ShortID))
	}
}

// Start launches numWorkers goroutines that process the queue until ctx is cancelled.
func (w *ThumbnailWorker) Start(ctx context.Context, numWorkers int) {
	for i := range numWorkers {
		go w.worker(ctx, i)
	}
	go w.reconcile(ctx)
	w.log.Info("thumbnail workers started", slog.Int("workers", numWorkers))
}

// reconcile recovers jobs lost during a restart or a temporary queue burst.
// Completed rows are excluded by thumb_generated, making retries idempotent.
func (w *ThumbnailWorker) reconcile(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		images, err := w.imageRepo.ListMissingThumbnails(ctx, cap(w.queue))
		if err != nil && ctx.Err() == nil {
			w.log.Warn("thumbnail reconciliation failed", slog.String("error", err.Error()))
		}
	enqueue:
		for _, img := range images {
			select {
			case w.queue <- img:
			case <-ctx.Done():
				return
			default:
				break enqueue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *ThumbnailWorker) worker(ctx context.Context, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		case img, ok := <-w.queue:
			if !ok {
				return
			}
			if err := w.process(ctx, img); err != nil {
				metrics.ThumbErrors.Inc()
				w.log.Error("thumbnail generation failed",
					slog.String("short_id", img.ShortID),
					slog.String("error", err.Error()),
					slog.Int("worker", id),
				)
			}
		}
	}
}

func (w *ThumbnailWorker) process(ctx context.Context, img *domain.Image) error {
	// Read original from storage
	rc, err := w.store.Get(ctx, img.StoragePath)
	if err != nil {
		return fmt.Errorf("get original: %w", err)
	}
	defer rc.Close()

	// Generate thumbnail
	thumbData, _, thumbExt, err := w.processor.GenerateThumbnail(rc, img.MIMEType, w.size)
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}

	// Build thumbnail storage path
	thumbPath := thumbPath(img.StoragePath, w.size, thumbExt)

	// Store
	if _, err := w.store.Put(ctx, thumbPath, bytes.NewReader(thumbData), int64(len(thumbData))); err != nil {
		return fmt.Errorf("store thumb: %w", err)
	}

	// Mark as generated
	if err := w.imageRepo.SetThumbGenerated(ctx, img.ID, true); err != nil {
		if errors.Is(err, domain.ErrImageNotFound) {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_ = w.store.Delete(cleanupCtx, thumbPath)
		}
		return fmt.Errorf("mark generated: %w", err)
	}

	metrics.ThumbsGenerated.Inc()
	w.log.Debug("thumbnail generated",
		slog.String("short_id", img.ShortID),
		slog.Int("size", w.size),
		slog.Int("bytes", len(thumbData)),
	)
	return nil
}

func thumbPath(originalPath string, size int, ext string) string {
	origExt := filepath.Ext(originalPath)
	base := strings.TrimSuffix(originalPath, origExt)
	return fmt.Sprintf("%s_t%d%s", base, size, ext)
}
