package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/handler"
	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

// BuildDate is set at build time via -ldflags "-X main.BuildDate=..."
// When not set (go run / go build without flags), it stays "dev".
var BuildDate = "dev"

func main() {
	// Load .env file if exists
	godotenv.Load()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Connect to database
	database, err := sql.Open("sqlite", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	// Enable foreign keys for SQLite
	if _, err := database.Exec("PRAGMA foreign_keys = ON"); err != nil {
		log.Fatalf("Failed to enable foreign keys: %v", err)
	}

	// Run database migrations
	if err := migrate.Run(database, migrations.FS); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	// Initialize queries
	ctx := context.Background()
	queries := db.New(database)

	// Initialize authenticator
	authenticator, err := auth.New(ctx, cfg, queries)
	if err != nil {
		log.Fatalf("Failed to create authenticator: %v", err)
	}

	// Initialize handlers
	h, err := handler.New(authenticator, database, cfg, BuildDate)
	if err != nil {
		log.Fatalf("Failed to create handler: %v", err)
	}

	// Setup router
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// Static files
	fileServer := http.FileServer(http.Dir(filepath.Join(cfg.WebRoot, "static")))
	r.Handle("/static/*", http.StripPrefix("/static/", fileServer))

	// Public routes
	r.Get("/", h.HomeHandler)
	r.Get("/api/qr", h.QRPaymentHandler)

	// Auth routes
	r.Route("/auth", func(r chi.Router) {
		r.Get("/login", authenticator.LoginHandler)
		r.Get("/callback", authenticator.CallbackHandler)
		r.Get("/logout", authenticator.LogoutHandler)
	})

	// Protected routes (all authenticated members)
	r.Group(func(r chi.Router) {
		r.Use(authenticator.RequireAuth)
		r.Get("/profile", h.ProfileHandler)
		r.Post("/profile", h.ProfileHandler)
		r.Get("/finance", h.MemberFinanceHandler)
		r.Get("/payments/recent", h.MemberRecentPaymentsHandler)
		r.Get("/projects", h.MemberProjectsHandler)
	})

	// Member API routes (read-only, requires auth)
	r.Route("/api/member", func(r chi.Router) {
		r.Use(authenticator.RequireAuth)
		r.Get("/projects", h.MemberProjectsAPIHandler)
		r.Get("/projects/payments", h.MemberProjectPaymentsHandler)
	})

	// Admin routes (requires memberportal_admin role)
	r.Route("/admin", func(r chi.Router) {
		r.Use(authenticator.RequireAuth)
		r.Get("/users", h.RequireAdmin(h.AdminUsersHandler))
		r.Get("/users/{id}", h.RequireAdmin(h.AdminUserProfileHandler))
		r.Get("/finance", h.RequireAdmin(h.AdminFinanceHandler))
		r.Get("/payments/unmatched", h.RequireAdmin(h.AdminUnmatchedPaymentsHandler))
		r.Get("/payments/recent", h.RequireAdmin(h.AdminRecentPaymentsHandler))
		r.Get("/projects", h.RequireAdmin(h.AdminProjectsHandler))
		r.Get("/logs", h.RequireAdmin(h.AdminLogsHandler))
		r.Get("/levels", h.RequireAdmin(h.AdminLevelsHandler))
		r.Get("/settings", h.RequireAdmin(h.AdminSettingsHandler))
	})

	// Admin API routes (requires memberportal_admin role)
	r.Route("/api/admin", func(r chi.Router) {
		r.Use(authenticator.RequireAuth)
		r.Get("/users", h.RequireAdmin(h.AdminUsersAPIHandler))
		r.Post("/roles/assign", h.RequireAdmin(h.AdminAssignRoleHandler))
		r.Post("/roles/remove", h.RequireAdmin(h.AdminRemoveRoleHandler))
		r.Get("/users/roles", h.RequireAdmin(h.AdminGetUserRolesHandler))
		r.Post("/test-email", h.RequireAdmin(h.AdminTestEmailHandler))
		r.Post("/users/{id}/state", h.RequireAdmin(h.AdminUpdateUserStateHandler))
		r.Post("/users/{id}/locale", h.RequireAdmin(h.AdminUpdateUserLocaleHandler))
		r.Post("/users/{id}/level", h.RequireAdmin(h.AdminUpdateUserLevelHandler))
		r.Post("/users/{id}/vs", h.RequireAdmin(h.AdminAllocateUserVSHandler))
		r.Post("/users/{id}/email", h.RequireAdmin(h.AdminUpdateUserEmailHandler))
		r.Post("/levels", h.RequireAdmin(h.AdminCreateLevelHandler))
		r.Post("/levels/update", h.RequireAdmin(h.AdminUpdateLevelHandler))
		r.Post("/levels/toggle", h.RequireAdmin(h.AdminToggleLevelActiveHandler))
		r.Delete("/levels", h.RequireAdmin(h.AdminDeleteLevelHandler))
		r.Get("/email/templates", h.RequireAdmin(h.AdminGetTemplateContentHandler))
		r.Post("/email/templates", h.RequireAdmin(h.AdminSaveTemplateContentHandler))
		r.Post("/email/retry", h.RequireAdmin(h.AdminRetryEmailHandler))
		r.Post("/email/cancel", h.RequireAdmin(h.AdminCancelEmailHandler))
		r.Post("/email/send-now", h.RequireAdmin(h.AdminSendNowEmailHandler))
		r.Post("/email/preview", h.RequireAdmin(h.AdminPreviewEmailHandler))
		r.Post("/banner", h.RequireAdmin(h.AdminSaveBannerHandler))
		r.Post("/awaiting-message", h.RequireAdmin(h.AdminSaveAwaitingMessageHandler))
		r.Post("/payments/assign", h.RequireAdmin(h.AdminAssignPaymentHandler))
		r.Post("/payments/update", h.RequireAdmin(h.AdminUpdatePaymentHandler))
		r.Post("/payments/dismiss", h.RequireAdmin(h.AdminDismissPaymentHandler))
		r.Post("/payments/undismiss", h.RequireAdmin(h.AdminUndismissPaymentHandler))
		r.Get("/projects", h.RequireAdmin(h.AdminProjectsAPIHandler))
		r.Post("/projects", h.RequireAdmin(h.AdminCreateProjectHandler))
		r.Delete("/projects", h.RequireAdmin(h.AdminDeleteProjectHandler))
		r.Get("/projects/payments", h.RequireAdmin(h.AdminProjectPaymentsHandler))
		r.Post("/projects/vs", h.RequireAdmin(h.AdminAddProjectVSHandler))
		r.Delete("/projects/vs", h.RequireAdmin(h.AdminRemoveProjectVSHandler))
	})

	// Create server
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 65 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Printf("Starting server on port %s", cfg.Port)
		log.Printf("Base URL: %s", cfg.BaseURL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	fmt.Println("Server stopped")
}
