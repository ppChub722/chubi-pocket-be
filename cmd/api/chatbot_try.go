package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/chatbot"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/recurring"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/telegram"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/database"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/logger"
)

// chatbotTry runs the chatbot eval against the configured model, as the user of one Telegram chat
// (their real wallets and categories, read only). Nothing goes to Telegram, nothing is written.
//
//	./server chatbot-try (--chat <id> | --user <username>) [--set tester|tester1] [--group a,b] [--case 3,7] [--text "ข้าว 60"] [--list]
//
// --user runs as an account with no Telegram chat (the seeded `tester`, `tester1`); --set picks the cases written for
// that account's data (evalcases-tester*.txt) instead of the owner's, and --user tester / tester1 picks it by itself.
func chatbotTry(args []string) int {
	fs := flag.NewFlagSet("chatbot-try", flag.ContinueOnError)
	chat := fs.Int64("chat", 0, "Telegram chat id of the user to run as")
	username := fs.String("user", "", "username of the user to run as (instead of --chat)")
	set := fs.String("set", "", `case set: "" = the owner's account, "tester" / "tester1" = a seeded test user (default: the --user's own)`)
	groups := fs.String("group", "", "groups to run, comma separated (default all)")
	ids := fs.String("case", "", "case numbers to run, comma separated")
	text := fs.String("text", "", `one message to try instead of the file ("\n" = new line)`)
	list := fs.Bool("list", false, "list the groups and cases, run nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *set == "" && slices.Contains(chatbot.EvalSets, *username) {
		*set = *username
	}
	if *set != "" && !slices.Contains(chatbot.EvalSets, *set) {
		fmt.Fprintln(os.Stderr, "--set is one of:", strings.Join(chatbot.EvalSets, ", "))
		return 2
	}
	cases := chatbot.EvalCasesFor(*set, splitList(*groups), splitInts(*ids))
	if *text != "" {
		cases = []chatbot.EvalCase{{ID: 0, Group: "text", Text: strings.ReplaceAll(*text, `\n`, "\n")}}
	}
	if *list {
		for _, c := range chatbot.EvalCasesFor(*set, nil, nil) {
			fmt.Printf("#%d [%s] %s | %s\n", c.ID, c.Group, strings.ReplaceAll(c.Text, "\n", " / "), c.Checks)
		}
		return 0
	}
	if (*chat == 0) == (*username == "") || len(cases) == 0 {
		fmt.Fprintln(os.Stderr, "need --chat or --user (one of them) and at least one case (see --list)")
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	log := logger.New(cfg.App.Env, "error")
	db, err := database.New(cfg.GetDatabaseURL(), log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "database:", err)
		return 1
	}
	defer db.Close()
	router, err := llm.NewRouter(cfg.LLM)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llm:", err)
		return 1
	}
	ctx := context.Background()
	// the recurring rules and the slot a payment would take: read only, so the service needs nothing else
	src := chatbot.NewDBLists(db).WithRules(recurring.NewService(db, nil, nil, nil))
	var userID uuid.UUID
	if *username != "" {
		userID, err = src.UserByName(ctx, *username)
	} else {
		userID, err = src.UserByChat(ctx, *chat)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "no such user (or no user linked to that chat):", err)
		return 1
	}
	lists, err := src.Lists(ctx, userID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lists:", err)
		return 1
	}
	pick := router.For(ctx, userID)
	lang := telegram.Lang(src.Lang(ctx, userID), "")
	n := time.Now().In(time.FixedZone("ICT", 7*3600))
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	fmt.Printf("chatbot-try: %s / %s | %d wallets, %d categories | %d cases\n",
		pick.Provider, pick.Model, len(lists.Wallets), len(lists.Categories), len(cases))
	if chatbot.RunEval(ctx, pick.Client, lists, lang, today, cases, time.Duration(cfg.LLM.TimeoutSec)*time.Second, os.Stdout) > 0 {
		return 1
	}
	return 0
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitInts(s string) []int {
	var out []int
	for _, p := range splitList(s) {
		if n, err := strconv.Atoi(p); err == nil {
			out = append(out, n)
		}
	}
	return out
}
