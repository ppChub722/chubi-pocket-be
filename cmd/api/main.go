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
	"github.com/ppChub722/finna-bbear-be/internal/modules/budget"
	"github.com/ppChub722/finna-bbear-be/internal/modules/project"
	"github.com/ppChub722/finna-bbear-be/internal/modules/recurring"
	"github.com/ppChub722/finna-bbear-be/internal/modules/shared"
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

	log.Info("🐻 FinaBBear Backend Starting...")
	log.Info("------------------------------------------------")
	log.Info("Configuration Loaded",
		"App Name", cfg.App.Name,
		"Environment", cfg.App.Env,
		"Port", cfg.App.Port,
	)
	log.Info("------------------------------------------------")

	// 3. Connect to Database
	dbPool, err := database.New(cfg.GetDatabaseURL(), log)
	if err != nil {
		log.Error("❌ Failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	// 4. Initialize Modules
	// Auth
	authStore := auth.NewStore(dbPool)
	authService := auth.NewService(authStore, cfg)
	authHandler := auth.NewHandler(authService)

	// Accounting (Accounts, Categories, Tags, Transactions)
	acctStore := accounting.NewStore(dbPool)
	acctService := accounting.NewService(acctStore)
	acctHandler := accounting.NewHandler(acctService)

	// Shared Expenses
	sharedStore := shared.NewStore(dbPool)
	sharedService := shared.NewService(sharedStore)
	sharedHandler := shared.NewHandler(sharedService)

	// Budgets & Saving Goals
	budgetStore := budget.NewStore(dbPool)
	budgetService := budget.NewService(budgetStore)
	budgetHandler := budget.NewHandler(budgetService)

	// Projects
	projectStore := project.NewStore(dbPool)
	projectService := project.NewService(projectStore)
	projectHandler := project.NewHandler(projectService)

	// Recurring & Installments
	recurringStore := recurring.NewStore(dbPool)
	recurringService := recurring.NewService(recurringStore)
	recurringHandler := recurring.NewHandler(recurringService)

	// 5. Setup Router
	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(logger.GinLoggerMiddleware(log))
	r.Use(gin.Recovery())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "up", "db": "connected"})
	})

	api := r.Group("/api/v1")
	{
		// =============================================
		// PUBLIC ROUTES (No Auth Required)
		// =============================================
		authRoutes := api.Group("/auth")
		{
			authRoutes.POST("/register", authHandler.Register)
			authRoutes.POST("/login", authHandler.Login)
		}

		// =============================================
		// PROTECTED ROUTES (JWT Required)
		// =============================================
		protected := api.Group("/")
		protected.Use(auth.Middleware(cfg))
		{
			// --- Auth & Profile ---
			protected.POST("/auth/logout", authHandler.Logout)
			protected.GET("/users/me", authHandler.GetProfile)
			protected.PUT("/users/me", authHandler.UpdateProfile)
			protected.PUT("/users/me/password", authHandler.ChangePassword)

			// --- Accounts ---
			protected.POST("/accounts", acctHandler.CreateAccount)
			protected.GET("/accounts", acctHandler.GetAccounts)
			protected.GET("/accounts/:id", acctHandler.GetAccount)
			protected.PUT("/accounts/:id", acctHandler.UpdateAccount)
			protected.DELETE("/accounts/:id", acctHandler.DeleteAccount)
			protected.GET("/accounts/:id/summary", acctHandler.GetAccountSummary)

			// --- Categories ---
			protected.POST("/categories", acctHandler.CreateCategory)
			protected.GET("/categories", acctHandler.GetCategories)
			protected.PUT("/categories/:id", acctHandler.UpdateCategory)
			protected.DELETE("/categories/:id", acctHandler.DeleteCategory)

			// --- Tags ---
			protected.POST("/tags", acctHandler.CreateTag)
			protected.GET("/tags", acctHandler.GetTags)
			protected.PUT("/tags/:id", acctHandler.UpdateTag)
			protected.DELETE("/tags/:id", acctHandler.DeleteTag)

			// --- Transactions ---
			protected.POST("/transactions", acctHandler.CreateTransaction)
			protected.GET("/transactions", acctHandler.GetTransactions)
			protected.GET("/transactions/summary", acctHandler.GetTransactionSummary)
			protected.GET("/transactions/:id", acctHandler.GetTransaction)
			protected.PUT("/transactions/:id", acctHandler.UpdateTransaction)
			protected.DELETE("/transactions/:id", acctHandler.DeleteTransaction)
			protected.POST("/transactions/:id/tags", acctHandler.AddTransactionTags)
			protected.DELETE("/transactions/:id/tags/:tag_id", acctHandler.RemoveTransactionTag)

			// --- Shared Expenses ---
			protected.GET("/shared-expenses/summary", sharedHandler.GetSummary)
			protected.POST("/shared-expenses", sharedHandler.CreateSharedExpense)
			protected.GET("/shared-expenses", sharedHandler.GetSharedExpenses)
			protected.GET("/shared-expenses/:id", sharedHandler.GetSharedExpense)
			protected.PUT("/shared-expenses/:id", sharedHandler.UpdateSharedExpense)
			protected.DELETE("/shared-expenses/:id", sharedHandler.DeleteSharedExpense)
			protected.GET("/shared-expenses/:id/splits", sharedHandler.GetSplits)
			protected.POST("/shared-expenses/:id/splits", sharedHandler.AddSplit)

			// Splits (top-level routes)
			protected.PUT("/splits/:id", sharedHandler.UpdateSplit)
			protected.DELETE("/splits/:id", sharedHandler.DeleteSplit)
			protected.GET("/splits/:id/settlements", sharedHandler.GetSettlements)
			protected.POST("/splits/:id/settlements", sharedHandler.CreateSettlement)

			// Settlements (top-level routes)
			protected.DELETE("/settlements/:id", sharedHandler.DeleteSettlement)

			// --- Budgets ---
			protected.GET("/budgets/overview", budgetHandler.GetBudgetOverview)
			protected.POST("/budgets", budgetHandler.CreateBudget)
			protected.GET("/budgets", budgetHandler.GetBudgets)
			protected.GET("/budgets/:id", budgetHandler.GetBudget)
			protected.PUT("/budgets/:id", budgetHandler.UpdateBudget)
			protected.DELETE("/budgets/:id", budgetHandler.DeleteBudget)

			// --- Saving Goals ---
			protected.POST("/saving-goals", budgetHandler.CreateSavingGoal)
			protected.GET("/saving-goals", budgetHandler.GetSavingGoals)
			protected.GET("/saving-goals/:id", budgetHandler.GetSavingGoal)
			protected.PUT("/saving-goals/:id", budgetHandler.UpdateSavingGoal)
			protected.DELETE("/saving-goals/:id", budgetHandler.DeleteSavingGoal)

			// --- Projects ---
			protected.POST("/projects", projectHandler.CreateProject)
			protected.GET("/projects", projectHandler.GetProjects)
			protected.GET("/projects/:id", projectHandler.GetProject)
			protected.PUT("/projects/:id", projectHandler.UpdateProject)
			protected.DELETE("/projects/:id", projectHandler.DeleteProject)
			protected.GET("/projects/:id/summary", projectHandler.GetProjectSummary)
			protected.GET("/projects/:id/members", projectHandler.GetMembers)
			protected.POST("/projects/:id/members", projectHandler.AddMember)
			protected.PUT("/projects/:id/members/:user_id", projectHandler.UpdateMemberRole)
			protected.DELETE("/projects/:id/members/:user_id", projectHandler.RemoveMember)
			protected.POST("/projects/:id/leave", projectHandler.LeaveProject)

			// --- Recurring & Installments ---
			protected.GET("/recurrings/upcoming", recurringHandler.GetUpcoming)
			protected.POST("/recurrings", recurringHandler.CreateRecurring)
			protected.GET("/recurrings", recurringHandler.GetRecurrings)
			protected.GET("/recurrings/:id", recurringHandler.GetRecurring)
			protected.PUT("/recurrings/:id", recurringHandler.UpdateRecurring)
			protected.DELETE("/recurrings/:id", recurringHandler.DeleteRecurring)
			protected.POST("/recurrings/:id/pause", recurringHandler.Pause)
			protected.POST("/recurrings/:id/resume", recurringHandler.Resume)
			protected.POST("/recurrings/:id/cancel", recurringHandler.Cancel)
		}
	}

	// 6. Start Recurring Scheduler (runs every hour)
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				processed, err := recurringService.ProcessDueRecurrings(context.Background())
				if err != nil {
					log.Error("Scheduler error", "error", err)
				} else if processed > 0 {
					log.Info("Scheduler processed recurring entries", "count", processed)
				}
			}
		}
	}()

	// 7. Start Server
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
