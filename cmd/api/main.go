package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ppChub722/finna-bbear-be/internal/auth"
	"github.com/ppChub722/finna-bbear-be/internal/config"
	"github.com/ppChub722/finna-bbear-be/internal/store"
)

func main() {
	fmt.Println("🐻 FinaBBear is waking up...")

	// 1. Load Configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Failed to load configuration: %v\n", err)
	}
	fmt.Printf("✅ Configuration loaded (Environment: %s)\n", cfg.App.Env)

	// 2. Connect to Database
	dbpool, err := pgxpool.New(context.Background(), cfg.GetDatabaseURL())
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Unable to connect to database: %v\n", err)
		os.Exit(1)
	}
	defer dbpool.Close()

	if err := dbpool.Ping(context.Background()); err != nil {
		log.Fatal("❌ Failed to ping database:", err)
	}
	fmt.Printf("✅ Database connected successfully! (%s:%s/%s)\n", 
		cfg.Database.Host, cfg.Database.Port, cfg.Database.DBName)

	store := store.New(dbpool)
	authHandler := auth.New(store, cfg)

	// 3. Set up Gin Server
	// Set Gin mode based on environment
	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	
	r := gin.Default()

	// Health check endpoint
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong (FinaBBear is ready!)",
			"app":     cfg.App.Name,
			"env":     cfg.App.Env,
		})
	})

	// API info endpoint
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"app":     cfg.App.Name,
			"version": "1.0.0",
			"status":  "running",
		})
	})

	authRoutes := r.Group("/auth")
	{
		authRoutes.POST("/register", authHandler.Register)
		authRoutes.POST("/login", authHandler.Login)
		authRoutes.POST("/logout", authHandler.Logout)
	}

    // (Future API routes will go here)

	// 4. Start Server with configured timeouts
	server := &http.Server{
		Addr:         ":" + cfg.App.Port,
		Handler:      r,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	fmt.Printf("🚀 Server is running on http://localhost:%s\n", cfg.App.Port)
	fmt.Printf("📚 API Documentation: http://localhost:%s/\n", cfg.App.Port)
	
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("❌ Failed to start server:", err)
	}
}