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

	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/contacts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/scheduled_transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/tags"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/user_pack_permissions"
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

	categoriesStore := categories.NewStore(dbPool)
	categoriesService := categories.NewService(categoriesStore)
	categoriesHandler := categories.NewHandler(categoriesService)

	tagsStore := tags.NewStore(dbPool)
	tagsService := tags.NewService(tagsStore)
	tagsHandler := tags.NewHandler(tagsService)

	transactionsStore := transactions.NewStore(dbPool)
	transactionsService := transactions.NewService(transactionsStore, categoriesService)
	transactionsHandler := transactions.NewHandler(transactionsService)

	// Wire categories <-> transactions cross-module count callback. Categories
	// uses this to decide DELETE = archive vs hard delete.
	categoriesService.WithTransactionCounter(transactionsService.CountByCategory)

	accountsStore := accounts.NewStore(dbPool)
	accountsService := accounts.NewService(accountsStore, transactionsService, categoriesService)
	accountsHandler := accounts.NewHandler(accountsService)

	contactsStore := contacts.NewStore(dbPool)
	contactsService := contacts.NewService(contactsStore)
	contactsHandler := contacts.NewHandler(contactsService)

	// Personal debts — unified bidirectional table (replaces shared_expense_splits).
	personalDebtsStore := personal_debts.NewStore(dbPool)
	personalDebtsService := personal_debts.NewService(personalDebtsStore, transactionsService)
	personalDebtsHandler := personal_debts.NewHandler(personalDebtsService)

	// Notifications — fan-in target.
	notificationsStore := notifications.NewStore(dbPool)
	notificationsService := notifications.NewService(notificationsStore)
	notificationsHandler := notifications.NewHandler(notificationsService)

	// Projects. Project transactions support splits (parent + child rows in
	// project_transactions). Personal-book mirror creation is FE-side via
	// the regular POST /transactions endpoint — no cross-module claim hook.
	projectsStore := projects.NewStore(dbPool)
	projectsService := projects.NewService(projectsStore)
	projectsHandler := projects.NewHandler(projectsService)

	// Saving goals (Phase 1c).
	savingGoalsStore := saving_goals.NewStore(dbPool)
	savingGoalsService := saving_goals.NewService(savingGoalsStore)
	savingGoalsHandler := saving_goals.NewHandler(savingGoalsService)

	// Budgets (Phase 1c).
	budgetsStore := budgets.NewStore(dbPool)
	budgetsService := budgets.NewService(budgetsStore)
	budgetsHandler := budgets.NewHandler(budgetsService)

	// Scheduled transactions (Phase 1c). generate-now is the dev/dogfood
	// trigger; Phase 3 wires the same logic into an hourly cron.
	scheduledStore := scheduled_transactions.NewStore(dbPool)
	scheduledService := scheduled_transactions.NewService(scheduledStore)
	scheduledHandler := scheduled_transactions.NewHandler(scheduledService)

	// --- Cross-module wiring (breaks cycles). Same pattern as 1a's
	// categoriesService.WithTransactionCounter.

	// transactions ↔ personal_debts.
	transactionsService.WithDebtsCreator(personalDebtsService.CreateForTransactionTx)
	transactionsService.WithDebtValidator(personalDebtsService.ValidateOwnership)
	transactionsService.WithDebtAutoBumper(personalDebtsService.AutoBumpInTx)

	// contacts ↔ notifications (link-request flow).
	contactsService.WithNotificationService(notificationsService)

	// contacts ↔ personal_debts (re-pointing post-1b.2 — the original splits
	// module that owned these hooks was retired; personal_debts now backs
	// the same surfaces: unlinked-names list on contact detail, absorb on
	// wire-up, snapshot-on-contact-delete).
	contactsService.WithUnlinkedNamesProvider(personalDebtsService.UnlinkedNames)
	contactsService.WithSplitsAbsorber(personalDebtsService.AbsorbForContact)
	contactsService.WithSplitsRestorer(personalDebtsService.RestoreOnContactDeleteTx)

	// projects ↔ notifications (project_invite, project_tx_recorded_for_you,
	// project_tx_changed). No claim hook — resolve flow uses /transactions
	// directly with source_project_transaction_id set client-side.
	projectsService.WithNotificationService(notificationsService)

	// Auth registration hooks: seed 6 system + starter user categories AND
	// the user's notification settings row, atomic with the user insert.
	authService := auth.NewService(authStore, cfg,
		categoriesService.SeedForUser,
		notificationsService.SeedSettings,
	)
	authHandler := auth.NewHandler(authService)

	usersStore := users.NewStore(dbPool)
	usersService := users.NewService(usersStore)
	usersHandler := users.NewHandler(usersService, authService)

	packPermsStore := user_pack_permissions.NewStore(dbPool)
	packPermsHandler := user_pack_permissions.NewHandler(packPermsStore)

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
		AllowAllOrigins: true,
		AllowMethods:    []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders: []string{
			"Origin",
			"Content-Type",
			"Authorization",
			// FE generates a UUID per outbound request and sends it as
			// X-Request-ID for log correlation. Browsers preflight any
			// non-CORS-safelisted request header — must be explicitly
			// allowed here or the OPTIONS response 403s and the actual
			// request never fires.
			"X-Request-ID",
		},
		// Echo X-Request-ID back so the FE can read it from the response
		// (ApiException pulls it from this header for error UIs).
		ExposeHeaders:    []string{"X-Request-ID"},
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
			protected.GET("/users/me/packs", packPermsHandler.ListMyPacks)

			// Categories (Phase 1a)
			protected.POST("/categories", categoriesHandler.Create)
			protected.GET("/categories", categoriesHandler.List)
			protected.PATCH("/categories/reorder", categoriesHandler.Reorder)
			protected.GET("/categories/:id", categoriesHandler.Get)
			protected.PUT("/categories/:id", categoriesHandler.Update)
			protected.DELETE("/categories/:id", categoriesHandler.Delete)
			protected.POST("/categories/:id/restore", categoriesHandler.Restore)
			protected.DELETE("/categories/:id/permanent", categoriesHandler.PermanentDelete)

			// Tags (Phase 1a)
			protected.POST("/tags", tagsHandler.Create)
			protected.GET("/tags", tagsHandler.List)
			protected.GET("/tags/:id", tagsHandler.Get)
			protected.PUT("/tags/:id", tagsHandler.Update)
			protected.DELETE("/tags/:id", tagsHandler.Delete)

			// Accounts (Phase 1a.2)
			protected.POST("/accounts", accountsHandler.Create)
			protected.GET("/accounts", accountsHandler.List)
			protected.GET("/accounts/:id", accountsHandler.Get)
			protected.PUT("/accounts/:id", accountsHandler.Update)
			protected.DELETE("/accounts/:id", accountsHandler.Delete)
			protected.POST("/accounts/:id/adjust-balance", accountsHandler.AdjustBalance)
			protected.GET("/accounts/:id/summary", accountsHandler.Summary)
			// Saving-goals helper lives under /accounts so the FE can fetch
			// the per-account allocation pie without an extra round-trip.
			protected.GET("/accounts/:id/saving-allocations", savingGoalsHandler.AccountAllocations)

			// Transactions (Phase 1a.3). /summary registered before /:id so the
			// literal path doesn't get swallowed by the param.
			protected.POST("/transactions", transactionsHandler.Create)
			protected.GET("/transactions", transactionsHandler.List)
			protected.GET("/transactions/summary", transactionsHandler.Summary)
			protected.GET("/transactions/:id", transactionsHandler.Get)
			protected.PUT("/transactions/:id", transactionsHandler.Update)
			protected.DELETE("/transactions/:id", transactionsHandler.Delete)
			protected.POST("/transactions/:id/tags", tagsHandler.Attach)
			protected.DELETE("/transactions/:id/tags/:tag_id", tagsHandler.Detach)

			// Contacts (Phase 1b.1 + 1b.2 link-request endpoints).
			// Static-path-before-param: /unlinked-names AND /link-requests/...
			// must come before /:id to avoid path collision.
			protected.POST("/contacts", contactsHandler.Create)
			protected.GET("/contacts", contactsHandler.List)
			// Static-path-before-param: `/unlinked-names` and
			// `/link-requests/...` MUST come before `/:id` so Gin's tree
			// doesn't match them as a UUID parameter.
			protected.GET("/contacts/unlinked-names", contactsHandler.UnlinkedNames)
			protected.GET("/contacts/link-requests/:notification_id/sender-profile", contactsHandler.SenderProfile)
			protected.POST("/contacts/link-requests/:notification_id/accept", contactsHandler.AcceptLinkRequest)
			protected.POST("/contacts/link-requests/:notification_id/reject", contactsHandler.RejectLinkRequest)
			protected.POST("/contacts/link-requests/:notification_id/create-linked-contact", contactsHandler.CreateLinkedContact)
			protected.POST("/contacts/link-requests/:notification_id/link-existing-contact/:contact_id", contactsHandler.LinkExistingContact)
			protected.GET("/contacts/:id", contactsHandler.Get)
			protected.PUT("/contacts/:id", contactsHandler.Update)
			protected.POST("/contacts/:id/archive", contactsHandler.Archive)
			protected.POST("/contacts/:id/restore", contactsHandler.Restore)
			protected.POST("/contacts/:id/request-link", contactsHandler.RequestLink)
			protected.POST("/contacts/:id/unlink", contactsHandler.Unlink)
			protected.POST("/contacts/:id/absorb", contactsHandler.Absorb)
			protected.DELETE("/contacts/:id", contactsHandler.Delete)

			// Personal debts (bidirectional, replaces splits + old debts).
			// /people view aggregates by counterparty with net positions.
			protected.GET("/personal-debts/people", personalDebtsHandler.People)
			protected.GET("/personal-debts", personalDebtsHandler.List)
			protected.POST("/personal-debts", personalDebtsHandler.Create)
			protected.GET("/personal-debts/:id", personalDebtsHandler.Get)
			protected.PUT("/personal-debts/:id", personalDebtsHandler.Update)
			protected.DELETE("/personal-debts/:id", personalDebtsHandler.Delete)
			protected.POST("/personal-debts/:id/cancel", personalDebtsHandler.Cancel)
			protected.POST("/personal-debts/:id/settle", personalDebtsHandler.Settle)

			// Notifications (Phase 1b.2). Static paths first.
			protected.GET("/notifications", notificationsHandler.List)
			protected.POST("/notifications/read-all", notificationsHandler.ReadAll)
			protected.GET("/notifications/settings", notificationsHandler.GetSettings)
			protected.PUT("/notifications/settings", notificationsHandler.UpdateSettings)
			protected.POST("/notifications/:id/read", notificationsHandler.MarkRead)
			protected.POST("/notifications/:id/actioned", notificationsHandler.MarkActioned)
			protected.POST("/notifications/:id/dismiss", notificationsHandler.MarkDismissed)
			protected.DELETE("/notifications/:id", notificationsHandler.Delete)

			// Projects (Phase 1b.2). Static-path-before-param applies:
			// /link-requests/... must come before /:id.
			protected.POST("/projects", projectsHandler.Create)
			protected.GET("/projects", projectsHandler.List)
			protected.POST("/projects/link-requests/:notification_id/accept", projectsHandler.AcceptLinkRequest)
			protected.POST("/projects/link-requests/:notification_id/reject", projectsHandler.RejectLinkRequest)
			protected.GET("/projects/:id", projectsHandler.Get)
			protected.PUT("/projects/:id", projectsHandler.Update)
			protected.DELETE("/projects/:id", projectsHandler.Delete)
			protected.GET("/projects/:id/members", projectsHandler.ListMembers)
			protected.POST("/projects/:id/members", projectsHandler.AddMember)
			protected.PUT("/projects/:id/members/:member_id", projectsHandler.UpdateMember)
			protected.DELETE("/projects/:id/members/:member_id", projectsHandler.RemoveMember)
			protected.POST("/projects/:id/members/:member_id/request-link", projectsHandler.RequestLink)
			protected.POST("/projects/:id/transfer-ownership", projectsHandler.TransferOwnership)
			protected.POST("/projects/:id/leave", projectsHandler.Leave)
			protected.GET("/projects/:id/transactions", projectsHandler.ListPT)
			protected.POST("/projects/:id/project-transactions", projectsHandler.CreatePT)
			protected.PUT("/projects/:id/project-transactions/:pt_id", projectsHandler.UpdatePT)
			protected.DELETE("/projects/:id/project-transactions/:pt_id", projectsHandler.DeletePT)
			protected.PUT("/projects/:id/project-transactions/:pt_id/mark", projectsHandler.ToggleMark)
			protected.GET("/projects/:id/summary", projectsHandler.Summary)

			// Saving goals (Phase 1c). All paths under /saving-goals are
			// param-only — no static-vs-param ordering hazard.
			protected.POST("/saving-goals", savingGoalsHandler.Create)
			protected.GET("/saving-goals", savingGoalsHandler.List)
			protected.GET("/saving-goals/:id", savingGoalsHandler.Get)
			protected.PUT("/saving-goals/:id", savingGoalsHandler.Update)
			protected.DELETE("/saving-goals/:id", savingGoalsHandler.Delete)
			protected.POST("/saving-goals/:id/archive", savingGoalsHandler.Archive)
			protected.POST("/saving-goals/:id/restore", savingGoalsHandler.Restore)

			// Budgets (Phase 1c). Static path /overview registered before
			// /:id so Gin's tree doesn't match it as a UUID param.
			protected.POST("/budgets", budgetsHandler.Create)
			protected.GET("/budgets", budgetsHandler.List)
			protected.GET("/budgets/overview", budgetsHandler.Overview)
			protected.GET("/budgets/:id", budgetsHandler.Get)
			protected.PUT("/budgets/:id", budgetsHandler.Update)
			protected.DELETE("/budgets/:id", budgetsHandler.Delete)
			protected.POST("/budgets/:id/archive", budgetsHandler.Archive)
			protected.POST("/budgets/:id/restore", budgetsHandler.Restore)

			// Scheduled transactions (Phase 1c). /upcoming registered
			// before /:id so Gin doesn't match it as a UUID param.
			protected.POST("/scheduled-transactions", scheduledHandler.Create)
			protected.GET("/scheduled-transactions", scheduledHandler.List)
			protected.GET("/scheduled-transactions/upcoming", scheduledHandler.Upcoming)
			protected.GET("/scheduled-transactions/:id", scheduledHandler.Get)
			protected.PUT("/scheduled-transactions/:id", scheduledHandler.Update)
			protected.DELETE("/scheduled-transactions/:id", scheduledHandler.Delete)
			protected.POST("/scheduled-transactions/:id/pause", scheduledHandler.Pause)
			protected.POST("/scheduled-transactions/:id/resume", scheduledHandler.Resume)
			protected.POST("/scheduled-transactions/:id/cancel", scheduledHandler.Cancel)
			protected.POST("/scheduled-transactions/:id/generate-now", scheduledHandler.GenerateNow)
			protected.GET("/scheduled-transactions/:id/history", scheduledHandler.History)
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
