// Package chatbot turns a chat message into transactions (0.4.2-4): text → LLM → JSON → checked
// items → a card with [✓ ยืนยัน] [✗ ยกเลิก]. Every word the bot sends is fixed here, a name from
// the user's own lists, or the user's own words copied by the model; never text the model wrote.
package chatbot

import (
	"github.com/ppChub722/chubi-pocket-be/internal/modules/telegram"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
)

// Texts is every fixed word of the bot in one language. The language: the app setting, else
// Telegram's, else English (owner 2026-11-04; telegram.Lang).
type Texts struct {
	NoAmount, NotMoney, TransferWallet, TooLong, TooFast, Handled string
	FailAI, FailServer, FailRefused, FailTrunc, Off, Thinking     string
	Saved, Cancelled, TrialMark, ButtonOK, ButtonCancel, Edited   string
	Items                                                         string // "%d รายการ"
	Expense, Income, Transfer, Today, Yesterday, None             string
	Category, Wallet                                              string // line labels, with a trailing space
}

// Thai texts as constants (tests read them).
const (
	TextNoAmount       = "ไม่เห็นยอดเงินครับ ลองพิมพ์ใหม่ เช่น ข้าว 60"
	TextNotMoney       = "จดได้แค่รายรับ รายจ่าย และการโอนครับ"
	TextTransferWallet = "บอกกระเป๋าต้นทางและปลายทางด้วยครับ"
	TextTooLong        = "ข้อความยาวเกินไปครับ"
	TextTooFast        = "ส่งเร็วไปนิดครับ รอสักครู่แล้วลองใหม่"
	TextHandled        = "รายการนี้จัดการไปแล้วครับ"
	TextFailAI         = "🤖 ระบบ AI มีปัญหาชั่วคราว ลองใหม่อีกครั้งนะครับ"
	TextFailServer     = "🛠️ ระบบของเรามีปัญหา ลองใหม่ภายหลังนะครับ"
	TextFailRefused    = "🙅 ข้อความนี้จดไม่ได้ ลองพิมพ์ใหม่นะครับ"
	TextFailTrunc      = "📏 รายการเยอะเกินไป ลองแบ่งส่งทีละน้อยนะครับ"
	TextOff            = "🛠️ ยังไม่เปิดจดรายการครับ"
	TextThinking       = "⏳ กำลังอ่านรายการ…"
	TextSaved          = "บันทึกแล้ว ✓"
	TextCancelled      = "ยกเลิกแล้ว"
	TextTrialMark      = " (ทดลอง ยังไม่ลงแอป)"
	TextFakeMark       = " (fake)"
	TextEdited         = "✏️ แก้แล้ว ดูการ์ดใหม่ด้านล่าง"
	ButtonOK           = "✓ ยืนยัน"
	ButtonCancel       = "✗ ยกเลิก"

	sep = " | "
)

var textsTH = Texts{
	NoAmount: TextNoAmount, NotMoney: TextNotMoney, TransferWallet: TextTransferWallet,
	TooLong: TextTooLong, TooFast: TextTooFast, Handled: TextHandled,
	FailAI: TextFailAI, FailServer: TextFailServer, FailRefused: TextFailRefused, FailTrunc: TextFailTrunc,
	Off: TextOff, Thinking: TextThinking, Saved: TextSaved, Cancelled: TextCancelled, TrialMark: TextTrialMark,
	ButtonOK: ButtonOK, ButtonCancel: ButtonCancel, Edited: TextEdited,
	Items:   "%d รายการ",
	Expense: "รายจ่าย", Income: "รายรับ", Transfer: "โอน", Today: "วันนี้", Yesterday: "เมื่อวาน", None: "ไม่ระบุ",
	Category: "หมวด: ", Wallet: "กระเป๋า: ",
}

var textsEN = Texts{
	NoAmount:       "No amount found. Try again, e.g. lunch 60",
	NotMoney:       "I can only log income, expenses and transfers.",
	TransferWallet: "Tell me both wallets, from and to.",
	TooLong:        "That message is too long.",
	TooFast:        "A bit too fast. Wait a moment and try again.",
	Handled:        "This card is already done.",
	FailAI:         "🤖 The AI service has a problem. Please try again.",
	FailServer:     "🛠️ Our server has a problem. Please try again later.",
	FailRefused:    "🙅 I can't log this message. Try writing it differently.",
	FailTrunc:      "📏 Too many items. Send them in smaller parts.",
	Off:            "🛠️ Chat logging isn't on yet.",
	Thinking:       "⏳ Reading your items…",
	Saved:          "Saved ✓",
	Cancelled:      "Cancelled",
	TrialMark:      " (trial, not in the app yet)",
	ButtonOK:       "✓ Confirm",
	ButtonCancel:   "✗ Cancel",
	Edited:         "✏️ Edited, see the new card below",
	Items:          "%d items",
	Expense:        "Expense", Income: "Income", Transfer: "Transfer", Today: "today", Yesterday: "yesterday",
	None:     "none",
	Category: "Category: ", Wallet: "Wallet: ",
}

func textsFor(lang string) Texts {
	if lang == telegram.LangThai {
		return textsTH
	}
	return textsEN
}

// Error codes of the chatbot itself; provider codes come from llm.ErrorCode.
const (
	CodeDB        = "E-DB"
	CodeRefused   = "E-REFUSED"
	CodeTruncated = "E-TRUNC"
	CodeBadJSON   = "E-JSON"
)

// fail: the line for a code, by whose side it is.
func (t Texts) fail(code string) string {
	switch code {
	case CodeRefused:
		return t.FailRefused
	case CodeTruncated:
		return t.FailTrunc
	case llm.Code5xx, llm.Code429, llm.CodeTimeout, CodeBadJSON:
		return t.FailAI
	}
	return t.FailServer
}

// Hints the model may give when nothing is bookable; anything else reads as not_money.
const (
	HintNoAmount       = "no_amount"
	HintNotMoney       = "not_money"
	HintTransferWallet = "transfer_wallet_missing"
	HintUnknown        = "unknown" // items present; with no items it reads as not_money
)

func (t Texts) hint(h string) string {
	switch h {
	case HintNoAmount:
		return t.NoAmount
	case HintTransferWallet:
		return t.TransferWallet
	}
	return t.NotMoney
}
