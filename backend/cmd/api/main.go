package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"bettercontent/backend/internal/ai"
	"bettercontent/backend/internal/assets"
	"bettercontent/backend/internal/content"
	"bettercontent/backend/internal/dna"
	"bettercontent/backend/internal/httpapi"
	"bettercontent/backend/internal/ideas"
	"bettercontent/backend/internal/identity"
	"bettercontent/backend/internal/platform/clock"
	"bettercontent/backend/internal/platform/config"
	"bettercontent/backend/internal/platform/httpserver"
	"bettercontent/backend/internal/platform/postgres"
)

const (
	startupTimeout  = 5 * time.Second
	shutdownTimeout = 5 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid backend configuration", "error", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid backend configuration", "error", err)
		os.Exit(1)
	}
	if err := cfg.ValidateAuth(); err != nil {
		logger.Error("invalid authentication configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("API stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	db, err := postgres.Open(startupCtx, cfg.DatabaseURL)
	cancelStartup()
	if err != nil {
		return err
	}
	defer db.Close()
	var developmentStorage *assets.DevelopmentFilesystemStorage
	var developmentAssetHandler http.Handler
	var assetService *assets.Service
	switch cfg.AssetStorageDriver {
	case "filesystem":
		developmentStorage, err = assets.OpenDevelopmentFilesystemStorage(cfg.AssetStorageRoot, localBackendBaseURL(cfg.HTTPAddr), []byte(cfg.AuthSecret))
		if err != nil {
			return err
		}
		defer developmentStorage.Close()
		developmentAssetHandler = developmentStorage.Handler()
		assetService = developmentStorage.NewService(db, time.Now, logger)
	case "s3":
		s3Storage, storageErr := assets.OpenS3AssetStorage(assets.S3StorageConfig{
			Endpoint:        cfg.AssetS3Endpoint,
			Region:          cfg.AssetS3Region,
			Bucket:          cfg.AssetS3Bucket,
			AccessKeyID:     cfg.AssetS3AccessKeyID,
			SecretAccessKey: cfg.AssetS3SecretAccessKey,
			ForcePathStyle:  cfg.AssetS3ForcePathStyle,
		})
		if storageErr != nil {
			return storageErr
		}
		assetService = s3Storage.NewService(db, time.Now, logger)
	default:
		return errors.New("unsupported asset storage driver")
	}
	auth := identity.NewService(db, cfg.AuthURL, cfg.AuthSecret, clock.System{}, cfg.Production)
	newAvalAI := func(userID string) (*ai.AvalAI, error) {
		if err := cfg.ValidateAI(); err != nil {
			return nil, err
		}
		return ai.NewAvalAI(userID, cfg.AvalAIAPIKey, cfg.AISafetyIdentifierSecret)
	}
	ideaService := ideas.NewService(db, func(userID string) (ai.GenerateIdeasProvider, error) {
		return newAvalAI(userID)
	}, clock.System{}, logger)
	contentService := content.NewService(db, func(userID string) (ai.GenerateContentProvider, error) {
		return newAvalAI(userID)
	}, clock.System{}, logger)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpserver.NewHandler(db, httpapi.New(auth, dna.NewService(db), ideaService, contentService, content.NewQueueService(db), assetService), developmentAssetHandler),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()
	logger.Info("Go API listening", "address", cfg.HTTPAddr)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serverErrors:
		return err
	}
}

func localBackendBaseURL(address string) string {
	if strings.HasPrefix(address, ":") {
		return "http://localhost" + address
	}
	return "http://" + address
}
