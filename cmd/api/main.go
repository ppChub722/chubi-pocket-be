package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"
	// Embedded zoneinfo: the alpine runtime image has no tzdata, and without it every
	// time.LoadLocation (X-Timezone, Asia/Bangkok) fails → UTC "today".
	_ "time/tzdata"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/appversion"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/contacts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/dashboard"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/me"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/providers"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/recurring"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	datasync "github.com/ppChub722/chubi-pocket-be/internal/modules/sync"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/tags"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/user_pack_permissions"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/users"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/database"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/logger"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(fmt.Errorf("failed to load config: %w", err))
	}

	log := logger.New(cfg.App.Env, cfg.App.LogLevel)
	log.Info("ChubiPocket Backend starting",
		"env", cfg.App.Env,
		"port", cfg.App.Port,
		"log_bodies", cfg.App.LogBodies,
		"min_app_build", cfg.Version.MinBuild,
	)

	dbPool, err := database.New(cfg.GetDatabaseURL(), log)
	if err != nil {
		log.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

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

	categoriesService.WithTransactionCounter(transactionsService.CountByCategory)

	accountsStore := accounts.NewStore(dbPool)
	accountsService := accounts.NewService(accountsStore, transactionsService, categoriesService)
	accountsHandler := accounts.NewHandler(accountsService)

	contactsStore := contacts.NewStore(dbPool)
	contactsService := contacts.NewService(contactsStore)
	contactsHandler := contacts.NewHandler(contactsService)

	personalDebtsStore := personal_debts.NewStore(dbPool)
	personalDebtsService := personal_debts.NewService(personalDebtsStore, transactionsService)
	personalDebtsHandler := personal_debts.NewHandler(personalDebtsService)

	notificationsStore := notifications.NewStore(dbPool)
	notificationsService := notifications.NewService(notificationsStore)
	notificationsHandler := notifications.NewHandler(notificationsService)
	meService := me.NewService(dbPool)

	projectsStore := projects.NewStore(dbPool)
	projectsService := projects.NewService(projectsStore)
	projectsHandler := projects.NewHandler(projectsService)

	savingGoalsStore := saving_goals.NewStore(dbPool)
	savingGoalsService := saving_goals.NewService(savingGoalsStore)
	savingGoalsHandler := saving_goals.NewHandler(savingGoalsService)

	budgetsStore := budgets.NewStore(dbPool)
	budgetsService := budgets.NewService(budgetsStore)
	budgetsHandler := budgets.NewHandler(budgetsService)
	meHandler := me.NewHandler(meService.WithBudgets(budgetsService).WithDebts(personalDebtsService))

	pendingService := pending.NewService(pending.NewStore(dbPool),
		transactionsService, tagsService, personalDebtsService, projectsService)
	pendingHandler := pending.NewHandler(pendingService)

	recurringService := recurring.NewService(dbPool, transactionsService, pendingService, notificationsService)
	recurringHandler := recurring.NewHandler(recurringService)

	ocrReader := ocr.NewTesseract(ocr.Config{
		Bin:     cfg.OCR.Bin,
		Lang:    cfg.OCR.Lang,
		Timeout: cfg.OCR.Timeout,
		Slots:   cfg.OCR.Concurrency,
	})
	if err := ocrReader.Check(); err != nil {
		log.Warn("ocr: tesseract not found — scan-slip will answer 503", "err", err)
	}
	importsHandler := imports.NewHandler(log, ocrReader)
	// Payment providers (migration 49) + import logs (migration 50): codes the import meets but
	// doesn't know, and slips it can't read yet.
	providersStore := providers.NewStore(dbPool)
	providersHandler := providers.NewHandler(providersStore)
	importsHandler.WithImportLogs(imports.NewLogStore(dbPool).Write, providersStore.Known)
	importsHandler.WithResolver(pending.NewResolver(dbPool).Resolve)
	if cfg.OCR.Dump && cfg.App.Env != "production" {
		importsHandler.EnableOCRDump(filepath.Join(os.TempDir(), "ocr-dump"),
			ocr.NewTesseract(ocr.Config{
				Bin: cfg.OCR.Bin, Lang: cfg.OCR.Lang, Timeout: 3 * time.Minute, Slots: 4,
			}))
	}

	dashboardService := dashboard.NewService(dashboard.NewStore(dbPool),
		transactionsService, accountsService, budgetsService, savingGoalsService, personalDebtsService)
	dashboardHandler := dashboard.NewHandler(dashboardService)

	transactionsService.WithDebtsCreator(personalDebtsService.CreateForTransactionTx)
	transactionsService.WithSplitsEditor(personalDebtsService.EditForTransactionTx)
	transactionsService.WithDebtLinks(personalDebtsService)
	personalDebtsService.WithNotifications(notificationsService)

	contactsService.WithNotificationService(notificationsService)

	contactsService.WithUnlinkedNamesProvider(personalDebtsService.UnlinkedNames)
	contactsService.WithSplitsAbsorber(personalDebtsService.AbsorbForContact)
	contactsService.WithSplitsRestorer(personalDebtsService.RestoreOnContactDeleteTx)

	projectsService.WithNotificationService(notificationsService)

	projectsService.WithTransactionsService(transactionsService)
	transactionsService.WithEventBillSyncer(projectsService.SyncBillFromTxTx)
	transactionsService.WithEventBillDeleter(projectsService.DeleteBillOfTxTx)
	transactionsService.WithEventLinks(projectsService)

	transactionsService.WithRecurringLinks(recurringService)
	pendingService.WithSubmitHook(recurringService.OnDraftSubmitted)
	pendingService.WithSavings(savingGoalsService)
	recurringService.WithSavings(savingGoalsService)

	budgetsService.WithExpected(recurringService)
	dashboardService.WithExpected(recurringService)
	meService.WithExpected(recurringService)

	accountsService.WithNotificationService(notificationsService)

	// Auth registration hooks: seed 6 system + starter user categories AND the user's notification
	// settings row, atomic with the user insert.
	authService := auth.NewService(authStore, cfg,
		categoriesService.SeedForUser,
		notificationsService.SeedSettings,
	)
	authHandler := auth.NewHandler(authService, log)

	usersStore := users.NewStore(dbPool)
	usersService := users.NewService(usersStore)
	usersHandler := users.NewHandler(usersService, authService)

	isExpenseCategory := func(ctx context.Context, userID, id uuid.UUID) error {
		c, err := categoriesService.Get(ctx, userID, id)
		if err != nil {
			return err
		}
		if c.Type != "expense" {
			return errors.New("not an expense category")
		}
		return nil
	}
	usersService.WithFeeCategoryCheck(isExpenseCategory)
	importsHandler.WithDraftContext(func(ctx context.Context, userID uuid.UUID) (imports.DraftContext, error) {
		var dc imports.DraftContext
		accs, err := accountsService.List(ctx, userID, accounts.StatusActive, "")
		if err != nil {
			return dc, err
		}
		for _, a := range accs {
			if len(a.Identifiers) > 0 {
				dc.Wallets = append(dc.Wallets, imports.Wallet{ID: a.ID, Identifiers: a.Identifiers})
			}
		}
		if id, err := usersService.FeeCategoryID(ctx, userID); err == nil && id != nil &&
			isExpenseCategory(ctx, userID, *id) == nil {
			dc.FeeCategoryID = id
		}
		return dc, nil
	})

	syncHandler := datasync.NewHandler(dbPool, log, datasync.Services{
		Accounts: accountsService, Categories: categoriesService, Tags: tagsService, Contacts: contactsService,
		Debts: personalDebtsService, Projects: projectsService, Recurring: recurringService,
		Transactions: transactionsService, Providers: providersStore,
	})

	packPermsStore := user_pack_permissions.NewStore(dbPool)
	packPermsHandler := user_pack_permissions.NewHandler(packPermsStore)

	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(logger.RequestIDMiddleware())
	r.Use(logger.GinLoggerMiddleware(log, cfg.App.LogBodies))
	// Same semantics as gin.Recovery() (500 + abort), but the panic is logged through slog with the
	// request_id instead of gin's raw writer.
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, err any) {
		logger.FromCtx(c, log).Error("panic recovered",
			"error", fmt.Sprint(err),
			"stack", string(debug.Stack()),
		)
		c.AbortWithStatus(http.StatusInternalServerError)
	}))
	r.Use(cors.New(cors.Config{
		AllowAllOrigins: true,
		AllowMethods:    []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders: []string{
			"Origin",
			"Content-Type",
			"Authorization",
			"X-Request-ID",
			appversion.Header,
			"X-Timezone",
		},
		ExposeHeaders:    []string{"X-Request-ID", datasync.VersionsHeader},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	versionHandler := appversion.NewHandler(appversion.Info{
		MinBuild:    cfg.Version.MinBuild,
		LatestBuild: cfg.Version.LatestBuild,
		DownloadURL: cfg.Version.DownloadURL,
		MessageTH:   cfg.Version.MessageTH,
		MessageEN:   cfg.Version.MessageEN,
	})

	openapiHandlers := []gin.HandlerFunc{apidoc.NewHandler(r).Serve}
	if cfg.App.Env == "production" {
		openapiHandlers = append([]gin.HandlerFunc{auth.Middleware(cfg, authStore)}, openapiHandlers...)
	}
	r.GET("/api/v1/openapi.json", openapiHandlers...)

	api := r.Group("/api/v1")
	{
		api.Use(versionHandler.Middleware("/api/v1/app/version"))
		api.GET("/app/version", versionHandler.Version)

		api.POST("/auth/register", authHandler.Register)
		api.POST("/auth/login", authHandler.Login)

		protected := api.Group("/")
		protected.Use(auth.Middleware(cfg, authStore))
		// the caller's X-Timezone rides in the request context: every service that works
		// out "today" (recurring, me, dashboard, budgets) reads it from there
		// and the caller's stored timezone / language are read once per request
		protected.Use(func(c *gin.Context) {
			ctx := shared.WithUserPrefs(c.Request.Context())
			if tz := c.GetHeader("X-Timezone"); tz != "" {
				ctx = shared.WithHeaderTZ(ctx, tz)
			}
			c.Request = c.Request.WithContext(ctx)
			c.Next()
		})
		protected.Use(datasync.Middleware(dbPool, log))
		{
			protected.POST("/auth/logout", authHandler.Logout)
			protected.PUT("/auth/password", authHandler.ChangePassword)

			protected.POST("/sync", syncHandler.Sync)

			protected.GET("/me/counts", meHandler.Counts)
			protected.GET("/me/archived", meHandler.Archived)
			protected.GET("/users/me", usersHandler.GetMe)
			protected.PUT("/users/me", usersHandler.UpdateMe)
			protected.POST("/users/me/deactivate", usersHandler.Deactivate)
			protected.POST("/users/me/reactivate", usersHandler.Reactivate)
			protected.GET("/users/me/packs", packPermsHandler.ListMyPacks)

			protected.POST("/categories", categoriesHandler.Create)
			protected.GET("/categories", categoriesHandler.List)
			protected.PATCH("/categories/reorder", categoriesHandler.Reorder)
			protected.GET("/categories/:id", categoriesHandler.Get)
			protected.PUT("/categories/:id", categoriesHandler.Update)
			protected.DELETE("/categories/:id", categoriesHandler.Delete)

			protected.POST("/tags", tagsHandler.Create)
			protected.GET("/tags", tagsHandler.List)
			protected.PUT("/tags/:id", tagsHandler.Update)
			protected.DELETE("/tags/:id", tagsHandler.Delete)

			protected.POST("/accounts", accountsHandler.Create)
			protected.GET("/accounts", accountsHandler.List)
			protected.PATCH("/accounts/reorder", accountsHandler.Reorder)
			protected.POST("/accounts/invites/:notification_id/accept", accountsHandler.AcceptInvite)
			protected.POST("/accounts/invites/:notification_id/reject", accountsHandler.RejectInvite)
			protected.GET("/accounts/:id", accountsHandler.Get)
			protected.POST("/accounts/:id/archive", accountsHandler.Archive)
			protected.POST("/accounts/:id/restore", accountsHandler.Restore)
			protected.PUT("/accounts/:id", accountsHandler.Update)
			protected.DELETE("/accounts/:id", accountsHandler.Delete)
			protected.POST("/accounts/:id/adjust-balance", accountsHandler.AdjustBalance)
			protected.POST("/accounts/:id/identifiers", accountsHandler.AddIdentifier)
			protected.POST("/accounts/:id/members", accountsHandler.InviteMember)
			protected.DELETE("/accounts/:id/members/:member_id", accountsHandler.RemoveMember)
			protected.POST("/accounts/:id/transfer-ownership", accountsHandler.TransferOwnership)
			protected.PUT("/accounts/:id/report-scope", accountsHandler.UpdateReportScope)
			// Saving-goals helper lives under /accounts so the FE can fetch the per-account allocation pie
			// without an extra round-trip.

			protected.POST("/transactions", transactionsHandler.Create)
			protected.GET("/transactions", transactionsHandler.List)
			protected.GET("/transactions/summary", transactionsHandler.Summary)
			protected.GET("/transactions/form-options", transactionsHandler.FormOptions)
			protected.GET("/transactions/:id", transactionsHandler.Get)
			protected.PUT("/transactions/:id", transactionsHandler.Update)
			protected.PUT("/transactions/:id/splits", transactionsHandler.EditSplits)
			protected.PUT("/transactions/:id/tags", transactionsHandler.SetTags)
			protected.POST("/transactions/:id/apply-event", projectsHandler.ApplyEventToTx)
			protected.DELETE("/transactions/:id", transactionsHandler.Delete)

			protected.POST("/contacts", contactsHandler.Create)
			protected.GET("/contacts", contactsHandler.List)
			// Static-path-before-param: `/unlinked-names` and `/link-requests/...` MUST come before `/:id`
			// so Gin's tree doesn't match them as a UUID parameter.
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

			protected.GET("/personal-debts/people", personalDebtsHandler.People)
			protected.GET("/personal-debts/people/:key", personalDebtsHandler.Person)
			protected.GET("/personal-debts/people/:key/entries", personalDebtsHandler.Entries)
			protected.POST("/personal-debts/people/:key/entries", personalDebtsHandler.CreateEntry)
			protected.POST("/personal-debts/people/:key/repayments", personalDebtsHandler.CreateRepayment)
			protected.POST("/personal-debts/split-requests/:notification_id/accept", personalDebtsHandler.AcceptSplitRequest)
			protected.POST("/personal-debts/split-changes/:notification_id/apply", personalDebtsHandler.ApplySplitChange)
			protected.GET("/personal-debts/:id", personalDebtsHandler.Get)
			protected.PUT("/personal-debts/:id", personalDebtsHandler.Update)
			protected.DELETE("/personal-debts/:id", personalDebtsHandler.Delete)

			protected.GET("/notifications", notificationsHandler.List)
			protected.GET("/notifications/unread-count", notificationsHandler.UnreadCount)
			protected.POST("/notifications/read-all", notificationsHandler.ReadAll)
			protected.GET("/notifications/settings", notificationsHandler.GetSettings)
			protected.PUT("/notifications/settings", notificationsHandler.UpdateSettings)
			protected.POST("/notifications/:id/read", notificationsHandler.MarkRead)
			protected.POST("/notifications/:id/actioned", notificationsHandler.MarkActioned)
			protected.POST("/notifications/:id/dismiss", notificationsHandler.MarkDismissed)
			protected.POST("/notifications/:id/restore", notificationsHandler.Restore)
			protected.DELETE("/notifications/:id", notificationsHandler.Delete)

			protected.POST("/projects", projectsHandler.Create)
			protected.GET("/projects", projectsHandler.List)
			protected.POST("/projects/quick", projectsHandler.QuickCreate)
			protected.POST("/projects/link-requests/:notification_id/accept", projectsHandler.AcceptLinkRequest)
			protected.POST("/projects/link-requests/:notification_id/reject", projectsHandler.RejectLinkRequest)
			protected.GET("/projects/:id", projectsHandler.Get)
			protected.GET("/projects/:id/detail", projectsHandler.Detail)
			protected.PUT("/projects/:id", projectsHandler.Update)
			protected.DELETE("/projects/:id", projectsHandler.Delete)
			protected.POST("/projects/:id/archive", projectsHandler.Archive)
			protected.POST("/projects/:id/restore", projectsHandler.Restore)
			protected.GET("/projects/:id/members", projectsHandler.ListMembers)
			protected.POST("/projects/:id/members", projectsHandler.AddMember)
			protected.PUT("/projects/:id/members/:member_id", projectsHandler.UpdateMember)
			protected.DELETE("/projects/:id/members/:member_id", projectsHandler.RemoveMember)
			protected.POST("/projects/:id/members/:member_id/request-link", projectsHandler.RequestLink)
			protected.POST("/projects/:id/transfer-ownership", projectsHandler.TransferOwnership)
			protected.POST("/projects/:id/leave", projectsHandler.Leave)
			protected.GET("/projects/:id/transactions", projectsHandler.ListPT)
			protected.POST("/projects/:id/project-transactions", projectsHandler.CreatePT)
			protected.POST("/projects/:id/bills", projectsHandler.AddBills)
			protected.DELETE("/projects/:id/bills/:transaction_id", projectsHandler.RemoveBill)
			protected.PUT("/projects/:id/project-transactions/:pt_id", projectsHandler.UpdatePT)
			protected.DELETE("/projects/:id/project-transactions/:pt_id", projectsHandler.DeletePT)
			protected.PUT("/projects/:id/project-transactions/:pt_id/mark", projectsHandler.ToggleMark)
			protected.POST("/projects/:id/project-transactions/:pt_id/copy", projectsHandler.CopyToPersonal)
			protected.PUT("/projects/:id/project-transactions/:pt_id/my-books", projectsHandler.SetMyBooks)
			protected.GET("/projects/:id/summary", projectsHandler.Summary)
			protected.GET("/projects/:id/settle", projectsHandler.Settle)
			protected.POST("/projects/:id/people", projectsHandler.ResolvePeople)
			protected.GET("/projects/:id/tags", projectsHandler.ListTags)
			protected.POST("/projects/:id/tags", projectsHandler.CreateTag)
			protected.POST("/projects/:id/tags/import", projectsHandler.ImportTags)
			protected.PUT("/projects/:id/tags/:tag_id", projectsHandler.UpdateTag)
			protected.DELETE("/projects/:id/tags/:tag_id", projectsHandler.DeleteTag)

			protected.POST("/saving-goals", savingGoalsHandler.Create)
			protected.GET("/saving-goals", savingGoalsHandler.List)
			protected.GET("/saving-goals/spare", savingGoalsHandler.Spare)
			protected.GET("/saving-goals/:id", savingGoalsHandler.Get)
			protected.POST("/saving-goals/:id/close", savingGoalsHandler.Close)
			protected.GET("/saving-entries", savingGoalsHandler.Entries)
			protected.POST("/saving-entries", savingGoalsHandler.CreateEntry)
			protected.PUT("/saving-entries/:id", savingGoalsHandler.UpdateEntry)
			protected.DELETE("/saving-entries/:id", savingGoalsHandler.DeleteEntry)
			protected.PUT("/saving-goals/:id", savingGoalsHandler.Update)
			protected.DELETE("/saving-goals/:id", savingGoalsHandler.Delete)
			protected.PUT("/saving-goals/:id/auto-saves", savingGoalsHandler.PutAutoSaves)
			protected.PUT("/saving-goals/spare/auto-saves", savingGoalsHandler.PutAutoSavesSpare)
			protected.PUT("/saving-goals/:id/auto-save", savingGoalsHandler.PutAutoSave)
			protected.DELETE("/saving-goals/:id/auto-save", savingGoalsHandler.DeleteAutoSave)
			protected.PUT("/saving-goals/spare/auto-save", savingGoalsHandler.PutAutoSaveSpare)
			protected.DELETE("/saving-goals/spare/auto-save", savingGoalsHandler.DeleteAutoSaveSpare)
			protected.POST("/saving-goals/:id/archive", savingGoalsHandler.Archive)
			protected.POST("/saving-goals/:id/restore", savingGoalsHandler.Restore)

			protected.POST("/budgets", budgetsHandler.Create)
			protected.GET("/budgets", budgetsHandler.List)
			protected.GET("/budgets/:id/periods", budgetsHandler.Periods)
			protected.PUT("/budgets/:id/periods/:period_id", budgetsHandler.UpdatePeriod)
			protected.GET("/budgets/:id/items", budgetsHandler.Items)
			protected.POST("/budgets/:id/close", budgetsHandler.Close)
			protected.GET("/budgets/:id", budgetsHandler.Get)
			protected.PUT("/budgets/:id", budgetsHandler.Update)
			protected.DELETE("/budgets/:id", budgetsHandler.Delete)
			protected.POST("/budgets/:id/archive", budgetsHandler.Archive)
			protected.POST("/budgets/:id/restore", budgetsHandler.Restore)

			protected.GET("/recurring", recurringHandler.List)
			protected.POST("/recurring", recurringHandler.Create)
			protected.GET("/recurring/:id", recurringHandler.Get)
			protected.PUT("/recurring/:id", recurringHandler.Update)
			protected.DELETE("/recurring/:id", recurringHandler.Delete)
			protected.GET("/recurring/:id/items", recurringHandler.RuleItems)
			protected.POST("/recurring/:id/pause", recurringHandler.Pause)
			protected.POST("/recurring/:id/resume", recurringHandler.Resume)
			protected.POST("/recurring/:id/archive", recurringHandler.Archive)
			protected.POST("/recurring/:id/restore", recurringHandler.Restore)
			protected.POST("/recurring/:id/extend", recurringHandler.Extend)
			protected.POST("/recurring/:id/duplicate", recurringHandler.Duplicate)
			protected.GET("/recurring/:id/occurrences", recurringHandler.Occurrences)
			protected.POST("/recurring/:id/occurrences/confirm-ready", recurringHandler.ConfirmReady)
			protected.POST("/recurring/:id/occurrences/mark-paid-ready", recurringHandler.MarkPaidReady)
			protected.POST("/recurring/:id/occurrences/skip-ready", recurringHandler.SkipReady)
			protected.PUT("/recurring/:id/occurrences/:slot", recurringHandler.EditOcc)
			protected.POST("/recurring/:id/occurrences/:slot/confirm", recurringHandler.ConfirmOcc)
			protected.POST("/recurring/:id/occurrences/:slot/skip", recurringHandler.SkipOcc)
			protected.POST("/recurring/:id/occurrences/:slot/restore", recurringHandler.RestoreOcc)
			protected.POST("/recurring/:id/occurrences/:slot/mark-paid", recurringHandler.MarkPaidOcc)
			protected.POST("/recurring/:id/occurrences/:slot/unmark-paid", recurringHandler.UnmarkPaidOcc)
			protected.POST("/recurring/:id/occurrences/:slot/link", recurringHandler.LinkOcc)
			protected.POST("/recurring/:id/occurrences/:slot/unlink", recurringHandler.UnlinkOcc)
			protected.GET("/recurring-items", recurringHandler.Items)
			protected.POST("/recurring-items", recurringHandler.CreateItem)
			protected.GET("/recurring-items/:id", recurringHandler.GetItem)
			protected.PUT("/recurring-items/:id", recurringHandler.UpdateItem)
			protected.DELETE("/recurring-items/:id", recurringHandler.DeleteItem)
			protected.POST("/recurring-items/:id/link", recurringHandler.Link)
			protected.POST("/recurring-items/:id/unlink", recurringHandler.Unlink)
			protected.POST("/recurring-items/:id/archive", recurringHandler.ArchiveItem)
			protected.POST("/recurring-items/:id/restore", recurringHandler.RestoreItem)

			protected.GET("/dashboard", dashboardHandler.Get)

			protected.GET("/pending-transactions", pendingHandler.List)
			protected.POST("/pending-transactions", pendingHandler.Create)
			protected.POST("/pending-transactions/submit", pendingHandler.Submit)
			protected.POST("/pending-transactions/submit-ready", pendingHandler.SubmitReady)
			protected.POST("/pending-transactions/discard", pendingHandler.Discard)
			protected.POST("/pending-transactions/scan-slip", importsHandler.ScanSlip)
			protected.POST("/pending-transactions/parse-text", importsHandler.ParseText)
			protected.POST("/slip-imports/check", importsHandler.CheckSlips)
			protected.GET("/payment-providers", providersHandler.List)
			protected.DELETE("/slip-imports", importsHandler.ResetSlips)
			protected.POST("/pending-transactions/:id/submit", pendingHandler.SubmitOne)
			protected.PUT("/pending-transactions/:id", pendingHandler.Update)
			protected.DELETE("/pending-transactions/:id", pendingHandler.Delete)
		}
	}

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
