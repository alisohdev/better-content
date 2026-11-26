package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bettercontent/backend/internal/assets"
	"bettercontent/backend/internal/platform/config"
	"bettercontent/backend/internal/platform/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if os.Getenv("ASSET_WORKER_RUNTIME") != "go" {
		logger.Error("Go Asset worker requires ASSET_WORKER_RUNTIME=go")
		os.Exit(1)
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid backend configuration", "error", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid backend configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	startupCtx, cancelStartup := context.WithTimeout(ctx, 5*time.Second)
	db, err := postgres.Open(startupCtx, cfg.DatabaseURL)
	cancelStartup()
	if err != nil {
		return err
	}
	defer db.Close()

	var service *assets.Service
	if cfg.AssetStorageDriver == "filesystem" {
		if err := cfg.ValidateAuth(); err != nil {
			return err
		}
		storage, err := assets.OpenDevelopmentFilesystemStorage(cfg.AssetStorageRoot, cfg.AuthURL, []byte(cfg.AuthSecret))
		if err != nil {
			return errors.New("could not open development asset storage")
		}
		defer storage.Close()
		service = storage.NewService(db, time.Now, logger)
	} else {
		if os.Getenv("ASSET_STORAGE_VERSIONING") != "disabled" {
			return errors.New("ASSET_STORAGE_VERSIONING must be disabled for managed media")
		}
		storage, err := assets.OpenS3AssetStorage(assets.S3StorageConfig{
			Endpoint: cfg.AssetS3Endpoint, Region: cfg.AssetS3Region, Bucket: cfg.AssetS3Bucket,
			AccessKeyID: cfg.AssetS3AccessKeyID, SecretAccessKey: cfg.AssetS3SecretAccessKey,
			ForcePathStyle: cfg.AssetS3ForcePathStyle,
		})
		if err != nil {
			return errors.New("could not open S3 asset storage")
		}
		service = storage.NewService(db, time.Now, logger)
	}
	return service.RunAssetWorker(ctx, assets.WorkerConfig{
		FFprobePath: cfg.FFprobePath, FFprobeExpectedVersion: cfg.FFprobeExpectedVersion,
		TempDir: cfg.AssetWorkerTempDir,
	})
}
