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
	Help, About, AboutShort                                            string
	Commands                                                           map[string]string // command menu: name → description
}

// CommandOrder is the order of the command menu.
var CommandOrder = []string{"help", "start", "unlink"}

// TextHelp is /help: what to type, with examples (no HTML here: it is sent as plain text).
const TextHelp = "จดรายการด้วยการพิมพ์ได้เลยครับ เช่น\n" +
	"ข้าวมันไก่ 60\n" +
	"เงินเดือน 30000\n" +
	"โอน 5000 จากกสิกรไปเงินสด\n" +
	"ออมเที่ยวญี่ปุ่น 2000\n" +
	"บอยยืม 500\n\n" +
	"บอทจะส่งการ์ดมาให้ กด ✓ เพื่อยืนยัน ✏️ เพื่อแก้ หรือ ✗ เพื่อยกเลิก\n" +
	"ส่งหลายรายการในข้อความเดียวได้ แยกด้วยบรรทัดใหม่\n\n" +
	"/unlink ยกเลิกการเชื่อมบัญชี"

var textsTH = Texts{
	Received: TextReceived, NotText: TextNotText, LinkFirst: TextLinkFirst, ChatOff: TextChatOff,
	Unlinked: TextUnlinked, LastLogin: TextLastLogin, Welcome: TextWelcome, Help: TextHelp,
	About:      "จดรายรับ รายจ่าย การโอน การออม และหนี้ใน chubiPocket แค่พิมพ์ข้อความมาในแชทนี้\n\nต้องเชื่อมบัญชีก่อน: แอป → โปรไฟล์ → Telegram",
	AboutShort: "จดรายการ chubiPocket ด้วยการพิมพ์",
	Commands:   map[string]string{"help": "วิธีใช้", "start": "เริ่มต้น", "unlink": "ยกเลิกการเชื่อมบัญชี"},
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
	Help: "Just type what you want to log, for example\n" +
		"chicken rice 60\n" +
		"salary 30000\n" +
		"transfer 5000 from Kbank to cash\n" +
		"save for Japan 2000\n" +
		"Boy borrowed 500\n\n" +
		"I'll send a card: tap ✓ to confirm, ✏️ to edit, or ✗ to cancel.\n" +
		"You can send several items in one message, one per line.\n\n" +
		"/unlink unlinks your account",
	About: "Log income, expenses, transfers, savings and debts in chubiPocket by typing in this chat.\n\n" +
		"Link your account first: app → Profile → Telegram",
	AboutShort: "Log chubiPocket entries by typing",
	Commands:   map[string]string{"help": "How to use", "start": "Start", "unlink": "Unlink your account"},
}

// TextsFor returns the texts of a language from Lang.
func TextsFor(lang string) Texts {
	if lang == LangThai {
		return textsTH
	}
	return textsEN
}
