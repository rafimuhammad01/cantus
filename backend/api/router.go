package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"

	"cantus/backend/api/handlers"
)

func NewRouter(allowedOrigins []string) *chi.Mux {
	mux := chi.NewRouter()

	mux.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	mux.Get("/health", handlers.Health)
	return mux
}
