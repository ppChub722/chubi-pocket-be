package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/finna-bbear-be/internal/modules/accounting"
	"github.com/ppChub722/finna-bbear-be/internal/modules/auth"
	"github.com/ppChub722/finna-bbear-be/internal/platform/config"
	"github.com/ppChub722/finna-bbear-be/internal/platform/database"
	"github.com/ppChub722/finna-bbear-be/internal/platform/logger"
)

func main() {
	// 1. Load Config
	cfg, err := config.Load()
	if err != nil {
		panic(fmt.Errorf("failed to load config: %w", err))
	}

	// 2. Initialize Logger
	log := logger.New(cfg.App.Env)

	// ---------------------------------------------------------
	// ✨ PRETTY STARTUP BANNER
	// ---------------------------------------------------------
	log.Info("🐻 FinaBBear Backend Starting...")
	log.Info("------------------------------------------------")
	log.Info("Configuration Loaded",
		"App Name", cfg.App.Name,
		"Environment", cfg.App.Env,
		"Port", cfg.App.Port,
	)
	log.Info("------------------------------------------------")

	// 3. Connect to Database (Logs are handled inside database.New)
	dbPool, err := database.New(cfg.GetDatabaseURL(), log)
	if err != nil {
		log.Error("❌ Failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	// 4. Initialize Modules
	authStore := auth.NewStore(dbPool)
	authService := auth.NewService(authStore, cfg)
	authHandler := auth.NewHandler(authService)
	// Accounting Module
	acctStore := accounting.NewStore(dbPool)
	acctService := accounting.NewService(acctStore)
	acctHandler := accounting.NewHandler(acctService)

	// 5. Setup Router
	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	} else {
		// Optional: Hide the noisy [GIN-debug] startup logs if you want
		// gin.SetMode(gin.ReleaseMode) 
	}

	r := gin.New()
	
	// ✅ USE OUR CUSTOM LOGGER instead of gin.Logger()
	r.Use(logger.GinLoggerMiddleware(log)) 
	r.Use(gin.Recovery())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "up", "db": "connected"})
	})

	api := r.Group("/api/v1")
	{
		authRoutes := api.Group("/auth")
		{
			authRoutes.POST("/register", authHandler.Register)
			authRoutes.POST("/login", authHandler.Login)
		}

		protected := api.Group("/")
		protected.Use(auth.Middleware(cfg))
		{
			protected.GET("/me", func(c *gin.Context) {
				userID, _ := c.Get("userID")
				username, _ := c.Get("username")
				c.JSON(http.StatusOK, gin.H{"message": "Authorized", "user_id": userID, "username": username})
			})
			// Accounts
			protected.POST("/accounts", acctHandler.CreateAccount)
			protected.GET("/accounts", acctHandler.GetAccounts)

			// Categories
			protected.POST("/categories", acctHandler.CreateCategory)
			protected.GET("/categories", acctHandler.GetCategories)

			// Transactions
			protected.POST("/transactions", acctHandler.CreateTransaction)
			protected.GET("/transactions", acctHandler.GetTransactions)
		}
	}

	// 6. Start Server
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
		log.Info("🚀 Server is ready to handle requests", "url", "http://localhost:"+cfg.App.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("Server startup failed", "error", err)
			os.Exit(1)
		}
	}()

	<-shutdownCtx.Done()
	log.Info("🛑 Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Error("Server forced to shutdown", "error", err)
	}
	log.Info("✅ Server exited successfully")
}