// PXL server — point d'entrée principal de la plateforme d'hébergement d'images.
//
// Ce programme initialise tous les composants (base de données, stockage, services,
// handlers HTTP, workers de génération de miniatures, métriques Prometheus)
// puis démarre le serveur HTTP avec arrêt gracieux sur signal SIGINT/SIGTERM.
//
// Configuration via variables d'environnement préfixées PXL_.
// Voir internal/config pour la liste complète.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"io/fs"

	"github.com/HeartBtz/pxl/internal/config"
	"github.com/HeartBtz/pxl/internal/handler"
	"github.com/HeartBtz/pxl/internal/imaging"
	"github.com/HeartBtz/pxl/internal/metrics"
	mw "github.com/HeartBtz/pxl/internal/middleware"
	"github.com/HeartBtz/pxl/internal/repository"
	"github.com/HeartBtz/pxl/internal/service"
	"github.com/HeartBtz/pxl/internal/storage"
	"github.com/HeartBtz/pxl/internal/worker"
	"github.com/HeartBtz/pxl/web"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("pxl %s (%s)\n", version, buildTime)
		return
	}
	// ── Config ──
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// ── Logger ──
	var logHandler slog.Handler
	logLevel := slog.LevelInfo
	switch cfg.Log.Level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: logLevel}
	if cfg.Log.Format == "json" {
		logHandler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		logHandler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(logHandler)
	slog.SetDefault(logger)
	logger.Info("PXL starting", slog.String("version", version), slog.String("build_time", buildTime))

	// ── Database ──
	db, err := repository.NewDB(
		cfg.Database.DSN(),
		cfg.Database.MaxOpenConns,
		cfg.Database.MaxIdleConns,
		cfg.Database.ConnMaxLifetime,
	)
	if err != nil {
		logger.Error("database connection failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer db.Close()

	if err := db.RunMigrations(); err != nil {
		logger.Error("migrations failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	logger.Info("database connected and migrated")
	metrics.RegisterDBMetrics(db.DB)

	// ── Storage ──
	store, err := initStorage(cfg)
	if err != nil {
		logger.Error("storage init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	logger.Info("storage backend ready", slog.String("type", store.Type()))
	if local, ok := store.(*storage.LocalBackend); ok {
		defer local.Close()
	}
	if err := os.MkdirAll(cfg.Upload.TempDir, 0o750); err != nil {
		logger.Error("upload staging directory init failed", slog.String("path", cfg.Upload.TempDir), slog.String("error", err.Error()))
		os.Exit(1)
	}

	// ── Repositories ──
	imageRepo := repository.NewImageRepository(db)
	userRepo := repository.NewUserRepository(db)
	albumRepo := repository.NewAlbumRepository(db)
	auditRepo := repository.NewAuditRepository(db)
	refreshRepo := repository.NewRefreshTokenRepository(db)

	// ── Processor ──
	processor := imaging.NewProcessor(cfg.Thumbnail.Quality, cfg.Upload.MaxPixels)

	// ── Services ──
	authSvc := service.NewAuthService(userRepo, refreshRepo, cfg, logger)
	imgSvc := service.NewImageService(imageRepo, userRepo, store, processor, cfg, logger)
	albumSvc := service.NewAlbumService(albumRepo, imageRepo)
	auditSvc := service.NewAuditService(auditRepo, logger)

	// ── Admin user ──
	if err := authSvc.EnsureAdminExists(context.Background()); err != nil {
		logger.Error("admin setup failed", slog.String("error", err.Error()))
	}

	// ── Thumbnail worker ──
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	thumbWorker := worker.NewThumbnailWorker(imageRepo, store, processor, cfg.Thumbnail.Size, 500, logger)
	imgSvc.SetThumbQueuer(thumbWorker)
	if cfg.Thumbnail.Enabled {
		thumbWorker.Start(ctx, cfg.Thumbnail.Workers)
	}

	// ── Expired image cleanup ticker (M06) ──
	cleanupTicker := time.NewTicker(5 * time.Minute)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-cleanupTicker.C:
				expired, err := imageRepo.CleanupExpired(ctx, 100)
				if err != nil {
					logger.Error("cleanup expired images failed", slog.String("error", err.Error()))
					continue
				}
				for _, img := range expired {
					imgSvc.DeleteFiles(ctx, img)
				}
				if len(expired) > 0 {
					logger.Info("cleaned up expired images", slog.Int("count", len(expired)))
				}
			}
		}
	}()

	// ── Handlers ──
	imgHandler := handler.NewImageHandler(imgSvc, albumSvc, cfg, logger)
	authHandler := handler.NewAuthHandler(authSvc, auditSvc, cfg, logger)
	albumHandler := handler.NewAlbumHandler(albumSvc, imgSvc, logger)
	adminHandler := handler.NewAdminHandler(imgSvc, auditSvc, imageRepo, albumRepo, userRepo, cfg, logger)
	webHandler := handler.NewWebHandler(imgSvc, albumSvc, authSvc, cfg, logger)

	// ── Rate limiters ──
	publicLimiter := mw.NewRateLimiter(cfg.Security.PublicRateLimitRequests, cfg.Security.RateLimitWindow)
	defer publicLimiter.Stop()
	apiLimiter := mw.NewRateLimiter(cfg.Security.RateLimitRequests, cfg.Security.RateLimitWindow)
	defer apiLimiter.Stop()
	loginLimiter := mw.NewRateLimiter(5, time.Minute) // Strict: 5 attempts/min per IP
	defer loginLimiter.Stop()

	// ── Router ──
	r := chi.NewRouter()

	// Global middleware
	r.Use(mw.Recovery(logger))
	r.Use(mw.RequestID)
	r.Use(mw.RealIP(cfg.Security.TrustedProxies))
	r.Use(mw.CORS(cfg.Security.CORSAllowedOrigin))
	r.Use(mw.SecurityHeaders)
	r.Use(mw.CookieOriginProtection(cfg.Server.BaseURL))
	r.Use(mw.Logger(logger))
	r.Use(metrics.InstrumentHandler)
	r.Use(publicLimiter.Handler)

	// Health
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		if err := db.HealthCheck(r.Context()); err != nil {
			http.Error(w, `{"status":"unhealthy"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"status":"healthy"}`)); err != nil {
			return
		}
	})

	// Image serve routes (public, no auth)
	r.With(mw.OptionalAuth(authSvc)).Get("/i/{shortID}.{ext}", imgHandler.ServeImage)
	r.With(mw.OptionalAuth(authSvc)).Get("/i/{shortID}", imgHandler.ServeImage)
	r.With(mw.OptionalAuth(authSvc)).Head("/i/{shortID}.{ext}", imgHandler.ServeImage)
	r.With(mw.OptionalAuth(authSvc)).Head("/i/{shortID}", imgHandler.ServeImage)
	r.With(mw.OptionalAuth(authSvc)).Get("/t/{shortID}.{ext}", imgHandler.ServeThumbnail)
	r.With(mw.OptionalAuth(authSvc)).Get("/t/{shortID}", imgHandler.ServeThumbnail)
	r.With(mw.OptionalAuth(authSvc)).Head("/t/{shortID}.{ext}", imgHandler.ServeThumbnail)
	r.With(mw.OptionalAuth(authSvc)).Head("/t/{shortID}", imgHandler.ServeThumbnail)

	// Static files (favicon, etc.) — served from embedded filesystem.
	staticSub, _ := fs.Sub(web.StaticFiles, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Web UI with optional auth
	r.Group(func(r chi.Router) {
		r.Use(mw.OptionalAuth(authSvc))
		r.Get("/", webHandler.UploadPage)
		r.Get("/v/{shortID}", webHandler.ViewPage)
		r.With(mw.Auth(authSvc)).Get("/gallery", webHandler.GalleryPage)
		r.Get("/login", webHandler.LoginPage)
		r.With(mw.Auth(authSvc)).Get("/account", webHandler.AccountPage)
		r.With(mw.Auth(authSvc), mw.RequireAdmin).Get("/admin", webHandler.AdminPage)
		r.Get("/a/{shortID}", webHandler.AlbumPage)
	})

	// API v1
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(apiLimiter.Handler)
		// Public auth endpoints with strict rate limiting
		r.Group(func(r chi.Router) {
			r.Use(loginLimiter.Handler)
			r.Post("/auth/login", authHandler.Login)
			r.Post("/auth/browser-login", authHandler.BrowserLogin)
			r.Post("/auth/browser-register", authHandler.BrowserRegister)
			r.Post("/auth/browser-refresh", authHandler.BrowserRefresh)
			r.Post("/auth/register", authHandler.PublicRegister)
			r.Post("/auth/refresh", authHandler.Refresh)
		})

		// Upload — optional auth (anonymous if allowed)
		r.Group(func(r chi.Router) {
			r.Use(mw.OptionalAuth(authSvc))
			r.Use(mw.MaxBody(uploadRequestLimit(cfg.Upload.MaxSizeBytes, cfg.Upload.MaxFiles)))
			r.Post("/upload", func(w http.ResponseWriter, req *http.Request) {
				// Check if anonymous upload is allowed
				_, hasUser := mw.GetUserID(req.Context())
				if !hasUser && !cfg.Auth.AllowAnonymous {
					http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
					return
				}
				imgHandler.Upload(w, req)
			})
		})

		// Public image info
		r.With(mw.OptionalAuth(authSvc)).Get("/images/{shortID}", imgHandler.GetInfo)
		r.With(mw.OptionalAuth(authSvc)).Get("/albums/{shortID}", albumHandler.Get)
		r.Delete("/images/{shortID}", func(w http.ResponseWriter, req *http.Request) {
			// Allow delete by token header without auth. Never accept secrets in URLs.
			if req.Header.Get("X-Delete-Token") != "" {
				imgHandler.Delete(w, req)
				return
			}
			// Otherwise require auth
			mw.Auth(authSvc)(http.HandlerFunc(imgHandler.Delete)).ServeHTTP(w, req)
		})

		// Authenticated endpoints
		r.Group(func(r chi.Router) {
			r.Use(mw.Auth(authSvc))

			r.Get("/images", imgHandler.ListMyImages)
			r.Get("/auth/me", authHandler.Me)
			r.With(mw.SessionOnly).Post("/auth/apikey", authHandler.GenerateAPIKey)
			r.With(mw.SessionOnly).Post("/auth/password", authHandler.ChangePassword)
			r.With(mw.SessionOnly).Post("/auth/logout", authHandler.Logout)

			// Albums
			r.Post("/albums", albumHandler.Create)
			r.Get("/albums", albumHandler.List)
			r.Delete("/albums/{shortID}", albumHandler.Delete)
			r.Post("/albums/{shortID}/images", albumHandler.AddImage)
			r.Delete("/albums/{shortID}/images/{imageShortID}", albumHandler.RemoveImage)

			// Admin only
			r.Group(func(r chi.Router) {
				r.Use(mw.SessionOnly)
				r.Use(mw.RequireAdmin)

				// User management
				r.Get("/admin/users", authHandler.AdminListUsers)
				r.Post("/admin/users", authHandler.Register)
				r.Get("/admin/users/{userID}", authHandler.AdminGetUser)
				r.Patch("/admin/users/{userID}", authHandler.AdminUpdateUser)
				r.Delete("/admin/users/{userID}", authHandler.AdminDeleteUser)

				// Stats & images
				r.Get("/admin/stats", adminHandler.Stats)
				r.Get("/admin/images", adminHandler.ListAllImages)
				r.Delete("/admin/images/{shortID}", adminHandler.ForceDeleteImage)

				// Audit logs
				r.Get("/admin/audit", adminHandler.AuditLogs)
			})
		})
	})

	// ── Metrics server (A04: graceful shutdown) ──
	var metricsSrv *http.Server
	if cfg.Metrics.Enabled {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		addr := fmt.Sprintf("127.0.0.1:%d", cfg.Metrics.Port)
		metricsSrv = &http.Server{Addr: addr, Handler: mux, ReadTimeout: 5 * time.Second}
		go func() {
			logger.Info("metrics server starting", slog.String("addr", addr))
			if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("metrics server error", slog.String("error", err.Error()))
			}
		}()
	}

	// ── HTTP Server ──
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		MaxHeaderBytes:    cfg.Server.MaxHeaderBytes,
	}

	// ── Graceful shutdown ──
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("PXL server starting", slog.String("addr", addr), slog.String("base_url", cfg.Server.BaseURL))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	<-done
	logger.Info("shutdown signal received")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutdownCancel()

	cancel() // Stop thumbnail workers and cleanup ticker
	cleanupTicker.Stop()

	if metricsSrv != nil {
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("metrics shutdown error", slog.String("error", err.Error()))
		}
	}

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", slog.String("error", err.Error()))
	}
	imgHandler.Stop()
	logger.Info("PXL server stopped")
}

func uploadRequestLimit(perFile int64, maxFiles int) int64 {
	const overhead = int64(1 << 20)
	if maxFiles < 1 || perFile <= 0 {
		return overhead
	}
	maxInt64 := int64(^uint64(0) >> 1)
	if perFile > (maxInt64-overhead)/int64(maxFiles) {
		return maxInt64
	}
	return perFile*int64(maxFiles) + overhead
}

func initStorage(cfg *config.Config) (storage.Backend, error) {
	switch cfg.Storage.Backend {
	case "s3":
		return storage.NewS3Backend(
			cfg.Storage.S3Endpoint,
			cfg.Storage.S3Region,
			cfg.Storage.S3Bucket,
			cfg.Storage.S3AccessKey,
			cfg.Storage.S3SecretKey,
			cfg.Storage.S3UseSSL,
			cfg.Storage.S3ForcePathStyle,
		)
	default:
		return storage.NewLocalBackend(cfg.Storage.LocalPath)
	}
}

func init() {
	// Ensure time is always UTC
	time.Local = time.UTC
}
