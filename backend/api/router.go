package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/rs/zerolog"

	"cantus/backend/api/handlers"
	"cantus/backend/logger"
	"cantus/backend/services"
)

// NewRouter builds and returns the chi router with all middleware and routes.
func NewRouter(allowedOrigins []string, log zerolog.Logger, svc services.YouTubeService, signer *services.Signer, storage services.Storage, processor services.ProcessorClient, jobRunner services.JobSubmitter, jobStore *services.JobStore) *chi.Mux {
	mux := chi.NewRouter()

	mux.Use(middleware.RequestID)
	mux.Use(logger.Middleware(log))
	mux.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	mux.Get("/health", handlers.Health)
	mux.Get("/api/songs/search", handlers.Search(svc))
	mux.Get("/api/preview/{videoId}", handlers.Preview(signer, storage, svc))
	mux.Post("/api/preview-shift", handlers.PreviewShift(signer, storage, svc, processor))
	mux.Get("/api/audio/{videoId}/{semitones}", handlers.Audio(signer, storage))
	mux.Get("/api/melody/{videoId}/{semitones}", handlers.Melody(signer, storage))
	mux.Post("/api/generate", handlers.Generate(signer, jobRunner))
	mux.Get("/api/status/{jobId}", handlers.Status(jobStore))
	return mux
}
