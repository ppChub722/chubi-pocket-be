package telegram

import (
	"context"
	"strings"
)

const (
	LangThai    = "th"
	LangEnglish = "en"
)

// Lang picks the bot's language (owner 2026-11-04): the app setting, else the language of the
// user's Telegram app, else English. A code the bot has no texts for is skipped.
func Lang(appSetting, telegramCode string) string {
	for _, c := range []string{appSetting, telegramCode} {
		switch c = strings.ToLower(strings.TrimSpace(c)); {
		case strings.HasPrefix(c, LangThai):
			return LangThai
		case strings.HasPrefix(c, LangEnglish):
			return LangEnglish
		}
	}
	return LangEnglish
}

// LangSource reads the app language setting of the user a chat is linked to ("" when none).
type LangSource interface {
	ChatLang(ctx context.Context, chatID int64) string
}

// Texts of the bot outside the chatbot (gates, welcome).
type Texts struct {
	Received, NotText, LinkFirst, ChatOff, Unlinked, LastLogin, Welcome string
}

var textsTH = Texts{
	Received: TextReceived, NotText: TextNotText, LinkFirst: TextLinkFirst, ChatOff: TextChatOff,
	Unlinked: TextUnlinked, LastLogin: TextLastLogin, Welcome: TextWelcome,
}

var textsEN = Texts{
	Received:  "Got your message ✓",
	NotText:   "Text messages only for now.",
	LinkFirst: "Link your account first: app → Profile → Telegram",
	ChatOff:   "Chat logging is off. Turn it on in the app.",
	Unlinked:  "Unlinked.",
	LastLogin: "Set a password in the app first, or you can't sign in any more.",
	Welcome: "Hello! Telegram is linked to chubiPocket ✓\n\n" +
		"Just type what you spent, for example\n" +
		"chicken rice 60\n" +
		"coffee 55 credit card\n" +
		"salary 30000\n\n" +
		"Turn chat logging on / off in the app: Profile → Telegram",
}

// TextsFor returns the texts of a language from Lang.
func TextsFor(lang string) Texts {
	if lang == LangThai {
		return textsTH
	}
	return textsEN
}
