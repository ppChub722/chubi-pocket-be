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
	"github.com/ppChub722/chubi-pocket-be/internal/modules/chatbot"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/connections"
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
	"github.com/ppChub722/chubi-pocket-be/internal/modules/telegram"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/user_pack_permissions"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/users"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/database"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/idempotency"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/logger"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
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

	recurringService := recurring.NewService(dbPool, transactionsService, pendingService, notificationsService).
		WithDebts(personalDebtsService) // 0.4.1-2 K: the "หนี้" kind books person-book entries
	personalDebtsService.WithRecurring(recurringService.PersonRecurring) // K13: the person page lists them
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

	transactionsService.WithDebtLinks(personalDebtsService)
	personalDebtsService.WithNotifications(notificationsService)

	contactsService.WithNotificationService(notificationsService)

	contactsService.WithUnlinkedNamesProvider(personalDebtsService.UnlinkedNames)
	contactsService.WithSplitsAbsorber(personalDebtsService.AbsorbForContact)
	contactsService.WithSplitsRestorer(personalDebtsService.RestoreOnContactDeleteTx)

	projectsService.WithNotificationService(notificationsService)

	projectsService.WithTransactionsService(transactionsService)
	projectsService.WithDebts(personalDebtsService) // claims and settles write debts and repayments

	transactionsService.WithRecurringLinks(recurringService)
	transactionsService.WithPaperReader(projectsService) // GET /transactions/:id carries its bill
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
		Transactions: transactionsService, Providers: providersStore, Users: usersService,
	})

	packPermsStore := user_pack_permissions.NewStore(dbPool)
	packPermsHandler := user_pack_permissions.NewHandler(packPermsStore)

	// 0.4.2 Telegram bot, webhook only; nil (no route) when TELEGRAM_BOT_TOKEN is empty.
	telegramWebhook := telegram.New(cfg.Telegram, dbPool, log)
	// 0.4.2 1b: linked apps + profile pictures; the bot answers linked chats only.
	var bot connections.Bot // stays a nil interface while the bot is off (Telegram unavailable)
	if telegramWebhook != nil {
		bot = telegramWebhook.Client()
	}
	connectionsService := connections.NewService(connections.NewStore(dbPool), bot)
	connectionsService.WithAuth(authService) // 1b-2: sign up / log in with Telegram
	connectionsHandler := connections.NewHandler(connectionsService)
	if telegramWebhook != nil {
		telegramWebhook.WithAccounts(connectionsService)
		// 0.4.2-4 chatbot: text → LLM → card. A bad LLM_* setting stops the start.
		llmRouter, err := llm.NewRouter(cfg.LLM)
		if err != nil {
			log.Error("llm config", "error", err)
			os.Exit(1)
		}
		chat := chatbot.New(telegramWebhook.Client(), chatbot.NewDBLists(dbPool), llmRouter,
			cfg.LLM.PerMinute, time.Duration(cfg.LLM.TimeoutSec)*time.Second, log)
		chat.LogBodies = cfg.LLM.LogBodies
		telegramWebhook.WithChat(chat)
		log.Info("chatbot: on", "provider", cfg.LLM.Provider, "log_bodies", cfg.LLM.LogBodies)
		// 0.4.2-4: Log In With Telegram (OIDC); the bot id is the client_id
		client := telegramWebhook.Client()
		connectionsService.WithTelegramLogin(connections.NewIDTokenVerifier(cfg.Telegram.BotID()),
			func(ctx context.Context, chatID int64) {
				if err := client.SendMessage(ctx, chatID, telegram.TextWelcome, 0); err != nil {
					log.Warn("telegram: welcome not sent", "chat_id", chatID, "error", err)
				}
			})
	}

	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(logger.RequestIDMiddleware())
	// bodies never logged: message texts (webhook) and Telegram id_tokens
	r.Use(logger.GinLoggerMiddleware(log, cfg.App.LogBodies, telegram.Path,
		"/api/v1/auth/telegram/token", "/api/v1/connections/telegram"))
	// Same semantics as gin.Recovery() (500 + abort), but the panic is logged through slog with the
	// request_id instead of gin's raw writer.
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, err any) {
		logger.FromCtx(c, log).Error("panic recovered",
			"error", fmt.Sprint(err),
			"stack", string(debug.Stack()),
		)
		c.AbortWithStatus(http.StatusInternalServerError)
	}))
	r.NoRoute(response.NoRoute)
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
			idempotency.Header,
		},
		ExposeHeaders:    []string{"X-Request-ID", datasync.VersionsHeader, idempotency.ReplayHeader},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}))

	health := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
	r.GET("/health", health)

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
		api.GET("/health", health) // before the build check: an outdated build still learns it is online
		if telegramWebhook != nil {
			// Telegram's calls: the secret header instead of a JWT; no build check, no sync middleware
			api.POST("/telegram/webhook", telegramWebhook.Serve)
		}
		api.GET("/brand-logos/:file", connections.BrandLogo) // 0.4.2 1b: public, embedded PNGs
		api.Use(versionHandler.Middleware("/api/v1/app/version"))
		api.GET("/app/version", versionHandler.Version)

		api.POST("/auth/register", authHandler.Register)
		api.POST("/auth/login", authHandler.Login)
		// log in / sign up with Telegram: an OIDC id_token (public, rate-limited per IP)
		api.POST("/auth/telegram/token", connectionsHandler.LoginToken)

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
		// 0.4.2-3: a save sent again with the same Idempotency-Key gets its first answer back
		protected.Use(idempotency.Middleware(dbPool, log))
		{
			protected.POST("/auth/logout", authHandler.Logout)
			protected.PUT("/auth/password", authHandler.ChangePassword)

			protected.POST("/sync", syncHandler.Sync)

			protected.GET("/me/counts", meHandler.Counts)
			protected.GET("/me/archived", meHandler.Archived)
			protected.GET("/users/me", usersHandler.GetMe)
			protected.PUT("/users/me", usersHandler.UpdateMe)

			// 0.4.2 1b: connections + profile pictures (GET /avatars/:id = a picture by id,
			// DELETE /avatars/:id = my picture of that source)
			protected.GET("/connections", connectionsHandler.List)
			protected.GET("/connections/:provider", connectionsHandler.Get)
			protected.DELETE("/connections/:provider", connectionsHandler.Unlink)
			protected.POST("/connections/:provider", connectionsHandler.Link) // 0.4.2-4 OIDC id_token
			protected.PATCH("/connections/:provider/permissions", connectionsHandler.UpdatePermissions)
			protected.POST("/connections/:provider/photo", connectionsHandler.ImportPhoto)
			protected.GET("/avatars/:id", connectionsHandler.Image)
			protected.DELETE("/avatars/:id", usersHandler.DeleteAvatar)
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
			protected.GET("/accounts/:id/summary", accountsHandler.Summary)
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
			protected.PUT("/transactions/:id/tags", transactionsHandler.SetTags)
			protected.POST("/transactions/:id/paper-sync", projectsHandler.SyncTransaction)
			protected.POST("/transactions/:id/unlink-paper", projectsHandler.UnlinkTransaction)
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
			protected.GET("/personal-debts/:id", personalDebtsHandler.Get)
			protected.PUT("/personal-debts/:id", personalDebtsHandler.Update)
			protected.DELETE("/personal-debts/:id", personalDebtsHandler.Delete)
			protected.POST("/personal-debts/:id/link", personalDebtsHandler.LinkTx)
			protected.POST("/personal-debts/:id/unlink", personalDebtsHandler.UnlinkTx)
			protected.POST("/personal-debts/:id/disconnect", personalDebtsHandler.Disconnect)
			protected.POST("/personal-debts/:id/paper-sync", projectsHandler.SyncDebt)
			protected.POST("/personal-debts/:id/unlink-paper", projectsHandler.UnlinkDebt)

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
			protected.POST("/projects/link-requests/:notification_id/accept", projectsHandler.AcceptLinkRequest)
			protected.POST("/projects/link-requests/:notification_id/reject", projectsHandler.RejectLinkRequest)
			protected.GET("/projects/:id", projectsHandler.Get)
			protected.GET("/projects/:id/detail", projectsHandler.Detail)
			protected.PUT("/projects/:id", projectsHandler.Update)
			protected.DELETE("/projects/:id", projectsHandler.Delete)
			protected.POST("/projects/:id/archive", projectsHandler.Archive)
			protected.POST("/projects/:id/restore", projectsHandler.Restore)
			protected.POST("/projects/:id/complete", projectsHandler.Complete)
			protected.GET("/projects/:id/settle", projectsHandler.Settle)
			protected.POST("/projects/:id/settle/book", projectsHandler.BookSettle)
			protected.PUT("/projects/:id/my-defaults", projectsHandler.SetMyDefaults)
			protected.GET("/projects/:id/members", projectsHandler.ListMembers)
			protected.POST("/projects/:id/members", projectsHandler.AddMember)
			protected.PUT("/projects/:id/members/:member_id", projectsHandler.UpdateMember)
			protected.DELETE("/projects/:id/members/:member_id", projectsHandler.RemoveMember)
			protected.POST("/projects/:id/members/:member_id/merge", projectsHandler.MergeMember)
			protected.POST("/projects/:id/members/:member_id/invite", projectsHandler.InviteMember)
			protected.PUT("/projects/:id/members/:member_id/my-contact", projectsHandler.BindMyContact)
			protected.DELETE("/projects/:id/members/:member_id/my-contact", projectsHandler.UnbindMyContact)
			protected.POST("/projects/:id/transfer-ownership", projectsHandler.TransferOwnership)
			protected.POST("/projects/:id/leave", projectsHandler.Leave)
			// Event bills: papers of the event.
			protected.GET("/projects/:id/bills", projectsHandler.ListEventBills)
			protected.POST("/projects/:id/bills", projectsHandler.CreateEventBill)
			protected.GET("/projects/:id/bills/:bid", projectsHandler.GetPaper(true))
			protected.PUT("/projects/:id/bills/:bid", projectsHandler.UpdatePaper(true))
			protected.POST("/projects/:id/bills/:bid/claim", projectsHandler.ClaimPaper(true))
			protected.POST("/projects/:id/bills/:bid/leave", projectsHandler.LeavePaper(true))
			protected.POST("/projects/:id/bills/:bid/move", projectsHandler.MoveEventBill)
			protected.DELETE("/projects/:id/bills/:bid/people/:pid", projectsHandler.RemovePaperPerson(true))
			protected.PATCH("/projects/:id/bills/:bid/people/:pid", projectsHandler.MarkPaperPerson(true))
			// Split bills: papers with no event.
			protected.GET("/split-bills", projectsHandler.ListSplitBills)
			protected.POST("/split-bills", projectsHandler.CreateSplitBill)
			protected.GET("/split-bills/:id", projectsHandler.GetPaper(false))
			protected.PUT("/split-bills/:id", projectsHandler.UpdatePaper(false))
			protected.POST("/split-bills/:id/join", projectsHandler.JoinSplitBill)
			protected.POST("/split-bills/:id/decline", projectsHandler.DeclineSplitBill)
			protected.POST("/split-bills/:id/claim", projectsHandler.ClaimPaper(false))
			protected.POST("/split-bills/:id/leave", projectsHandler.LeavePaper(false))
			protected.POST("/split-bills/:id/move-to-event", projectsHandler.MoveSplitBill)
			protected.DELETE("/split-bills/:id/people/:pid", projectsHandler.RemovePaperPerson(false))
			protected.PATCH("/split-bills/:id/people/:pid", projectsHandler.MarkPaperPerson(false))
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
			protected.POST("/recurring/occurrences/bulk", recurringHandler.SettleOccurrences)
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
	if telegramWebhook != nil {
		telegramWebhook.Start() // getMe + setWebhook in the background; failures are logged only
	}

	<-shutdownCtx.Done()
	log.Info("Shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Error("Forced shutdown", "error", err)
	}
	log.Info("Server exited")
}
