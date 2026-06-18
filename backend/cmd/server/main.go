// Package main is the entry point for the cantus backend server.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cantus/backend/api"
	"cantus/backend/config"
	"cantus/backend/logger"
	"cantus/backend/services"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.New(os.Stdout, cfg.LogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: %v\n", err)
		os.Exit(1)
	}

	signer, err := services.NewSigner(cfg.VideoIDSigningKey)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create signer")
	}

	var storage services.Storage
	var blobTokener *services.BlobTokener
	switch cfg.StorageBackend {
	case "local":
		blobTokener = services.NewBlobTokener(signer)
		s, err := services.NewLocalDiskStorageWithBlob(cfg.CacheDir, cfg.BlobBaseURL, blobTokener, 10*time.Minute)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to create local storage")
		}
		storage = s
	case "r2":
		s, err := services.NewR2Storage(services.R2Config{
			AccountID:       cfg.R2AccountID,
			AccessKeyID:     cfg.R2AccessKeyID,
			SecretAccessKey: cfg.R2SecretAccessKey,
			Bucket:          cfg.R2Bucket,
			PresignTTL:      time.Duration(cfg.R2PresignTTLSeconds) * time.Second,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("failed to create r2 storage")
		}
		storage = s
	default:
		log.Fatal().Str("backend", cfg.StorageBackend).Msg("unknown STORAGE_BACKEND")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	searchSvc := services.NewYTMusicSearchProd(signer, 600*time.Second, 256)
	svc := services.NewPythonYouTubeService(searchSvc, signer, storage, services.ExecRunner{}, cfg.YTDLPCookiesPath)

	var origins []string
	for _, o := range strings.Split(cfg.AllowedOrigins, ",") {
		if trimmed := strings.TrimSpace(o); trimmed != "" {
			origins = append(origins, trimmed)
		}
	}

	processor := services.NewPythonProcessorClient(
		cfg.ProcessorURL,
		&http.Client{Timeout: time.Duration(cfg.ProcessorTimeoutSeconds) * time.Second},
	)
	jobStore := services.NewJobStore(1 * time.Hour)
	jobStore.StartCleanup(ctx, 5*time.Minute)

	shifter := services.NewCLIShifter(cfg.RubberbandPath, cfg.FFmpegPath, services.ExecRunner{})

	maxJobs := cfg.MaxConcurrentJobs
	jobRunner := services.NewJobRunner(svc, storage, processor, shifter, jobStore, maxJobs)

	lrclibClient := services.NewLRCLibClient("")
	previewFailures := services.NewVideoFailureTracker()

	r := api.NewRouter(origins, log, svc, signer, storage, processor, shifter, jobRunner, jobStore, blobTokener, lrclibClient, previewFailures)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit

		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("graceful shutdown failed")
		}
	}()

	log.Info().Int("port", cfg.Port).Str("cache_dir", cfg.CacheDir).Msg("backend listening")

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal().Err(err).Msg("server failed")
	}
}
