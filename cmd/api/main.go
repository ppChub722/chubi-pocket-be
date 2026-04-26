package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/users"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/database"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/logger"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(fmt.Errorf("failed to load config: %w", err))
	}

	log := logger.New(cfg.App.Env)
	log.Info("ChubiPocket Backend starting",
		"env", cfg.App.Env,
		"port", cfg.App.Port,
	)

	dbPool, err := database.New(cfg.GetDatabaseURL(), log)
	if err != nil {
		log.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	// --- Modules ---
	authStore := auth.NewStore(dbPool)
	authService := auth.NewService(authStore, cfg)
	authHandler := auth.NewHandler(authService)

	usersStore := users.NewStore(dbPool)
	usersService := users.NewService(usersStore)
	usersHandler := users.NewHandler(usersService, authService)

	// --- Router ---
	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(logger.GinLoggerMiddleware(log))
	r.Use(gin.Recovery())
	// Phase 0/1: permissive — Flutter web + Android emulator + LAN devices all allowed.
	// Phase 3: restrict AllowAllOrigins → AllowOrigins with the prod web/app hostnames.
	r.Use(cors.New(cors.Config{
		AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api/v1")
	{
		// Public
		api.POST("/auth/register", authHandler.Register)
		api.POST("/auth/login", authHandler.Login)

		// Protected
		protected := api.Group("/")
		protected.Use(auth.Middleware(cfg, authStore))
		{
			protected.POST("/auth/logout", authHandler.Logout)
			protected.PUT("/auth/password", authHandler.ChangePassword)

			protected.GET("/users/me", usersHandler.GetMe)
			protected.PUT("/users/me", usersHandler.UpdateMe)
			protected.PUT("/users/me/password", usersHandler.ChangePassword)
			protected.POST("/users/me/deactivate", usersHandler.Deactivate)
			protected.POST("/users/me/reactivate", usersHandler.Reactivate)
		}
	}

	// --- HTTP server ---
	server := &http.Server{
		Addr:         ":" + cfg.App.Port,
		Handler:      r,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("Server ready", "url", "http://localhost:"+cfg.App.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("Server startup failed", "error", err)
			os.Exit(1)
		}
	}()

	<-shutdownCtx.Done()
	log.Info("Shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Error("Forced shutdown", "error", err)
	}
	log.Info("Server exited")
}
