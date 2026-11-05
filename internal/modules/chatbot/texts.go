// Package chatbot turns a chat message into transactions (0.4.2-4): text → LLM → JSON → checked
// items → a card with [✓ ยืนยัน] [✗ ยกเลิก]. Every word the bot sends is fixed here, a name from
// the user's own lists, or the user's own words copied by the model; never text the model wrote.
package chatbot

import (
	"fmt"
	"strings"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/telegram"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
)

// Texts is every fixed word of the bot in one language. The language: the app setting, else
// Telegram's, else English (owner 2026-11-04; telegram.Lang).
type Texts struct {
	NoAmount, NotMoney, TransferWallet, TooLong, TooFast, Handled string
	FailAI, FailServer, FailRefused, FailTrunc, Off, Thinking     string
	Saved, Cancelled, TrialMark, ButtonOK, ButtonCancel, Edited   string
	TierFree, CapReached                                          string
	Items, PartialBooked, Queued                                                string // "%d รายการ"; "บันทึก %d จาก %d รายการ …"
	MissingFmt                                                  string
	MissingNames                                                map[string]string
	Expense, Income, Transfer, Today, Yesterday, None             string
	Category, Wallet                                              string // line labels, with a trailing space
	GoalMissing, EventDone, Unlinked, InsufficientSaving          string
	Card                                                          CardWords
}

// CardWords are the fixed words of the cards of the saving, bill, recurring and debt rows.
type CardWords struct {
	Save, Allocate, Move, Withdraw             string // titles of a saving row
	Goal, Spare, BackTo                        string // "🐷 เป้า: ", "ยังไม่จัดสรร", "👛 กลับเข้า: "
	Event, NewEvent, People, Me                string // "🏝️ อีเวนต์: ", "🆕 อีเวนต์ใหม่: ", "👥 ", "ฉัน"
	PaidMe, PaidBy, Equal                      string // "💳 ฉันจ่ายเอง", "💳 %sจ่าย", "หารเท่ากัน %d คน"
	MyShare, Notify, NobodyYet                 string // "🧮 ส่วนของฉัน %s", "📨 แจ้ง: ", "[ใคร ?]"
	Recurring, Slot                            string // "🔄 รายการประจำ: ", "งวด %s"
	Debt, IOwe, OwesMe, Everyone               string // "🤝 รายการหนี้: ", "เราติด%s %s", "%sติดเรา %s", "ทุกคนติดเราคนละ %s"
	Lend, Borrow, OwesMeTitle, IOweTitle       string // debt titles with a name
	RepayIn, RepayOut                          string // "%sคืนเงิน", "คืนเงิน%s"
	MoneyOut, MoneyIn, NoOut, NoIn, NotOutOfWallet string // "💸 รายจ่าย: ออกจาก %s %s", … "➖ ไม่มีรายจ่าย"
	Months                                     [12]string
}


// Thai texts as constants (tests read them).
const (
	TextNoAmount       = "ไม่เห็นยอดเงินครับ ลองพิมพ์ใหม่ เช่น ข้าว 60"
	TextNotMoney       = "จดได้แค่รายรับ รายจ่าย และการโอนครับ"
	TextGoalMissing    = "บอกเป้าหมายด้วยครับ"
	TextEventDone      = "อีเวนต์นี้จบแล้วครับ"
	TextUnlinked       = "บันทึกแล้ว แต่ผูกกับรายการประจำไม่ได้"
	TextNotEnoughSaving = "เงินออมในที่ที่จะย้ายออกไม่พอครับ"
	TextTransferWallet = "บอกกระเป๋าด้วยครับ และว่าเงินออกหรือเข้า เช่น ถอน 500 จากกสิกร"
	TextTooLong        = "ข้อความยาวเกินไปครับ"
	TextTooFast        = "ส่งเร็วไปนิดครับ รอสักครู่แล้วลองใหม่"
	TextTierFree       = "🔒 ฟีเจอร์นี้ยังไม่เปิดให้ใช้ครับ"
	TextCapReached     = "📊 เดือนนี้ใช้ครบโควตาแล้วครับ ใช้ได้อีกวันที่ 1"
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
	TextQueued         = "ส่งไปรอยืนยันในแอปแล้ว ✓"
	TextPartialBooked  = "⚠️ บันทึกได้ %d จาก %d รายการ รายการที่เหลือบันทึกไม่สำเร็จ"
	TextMissing        = "ยังขาด: %s · Reply บนการ์ดเพื่อเติม"
	TextFakeMark       = " (fake)"
	TextEdited         = "✏️ แก้แล้ว ดูการ์ดใหม่ด้านล่าง"
	ButtonOK           = "✓ ยืนยัน"
	ButtonCancel       = "✗ ยกเลิก"

	sep       = " | "
	separator = "──────────" // under the head line and between items
)

var textsTH = Texts{
	NoAmount: TextNoAmount, NotMoney: TextNotMoney, TransferWallet: TextTransferWallet,
	TooLong: TextTooLong, TooFast: TextTooFast, Handled: TextHandled, TierFree: TextTierFree, CapReached: TextCapReached,
	FailAI: TextFailAI, FailServer: TextFailServer, FailRefused: TextFailRefused, FailTrunc: TextFailTrunc,
	Off: TextOff, Thinking: TextThinking, Saved: TextSaved, Cancelled: TextCancelled, TrialMark: TextTrialMark, PartialBooked: TextPartialBooked, Queued: TextQueued, MissingFmt: TextMissing, MissingNames: missingTH,
	ButtonOK: ButtonOK, ButtonCancel: ButtonCancel, Edited: TextEdited,
	Items:   "%d รายการ",
	Expense: "รายจ่าย", Income: "รายรับ", Transfer: "โอน", Today: "วันนี้", Yesterday: "เมื่อวาน", None: "ไม่ระบุ",
	Category: "หมวด: ", Wallet: "กระเป๋า: ",
	GoalMissing: TextGoalMissing, EventDone: TextEventDone, Unlinked: TextUnlinked, InsufficientSaving: TextNotEnoughSaving,
	Card: CardWords{
		Save: "ออมเงิน", Allocate: "จัดสรร", Move: "ย้ายเป้า", Withdraw: "ถอนเงินออม",
		Goal: "🐷 เป้า: ", Spare: "ยังไม่จัดสรร", BackTo: "👛 กลับเข้า: ",
		Event: "🏝️ อีเวนต์: ", NewEvent: "🆕 อีเวนต์ใหม่: ", People: "👥 ", Me: "ฉัน",
		PaidMe: "💳 ฉันจ่ายเอง", PaidBy: "💳 %sจ่าย", Equal: "หารเท่ากัน %d คน",
		MyShare: "🧮 ส่วนของฉัน %s", Notify: "📨 แจ้ง: ", NobodyYet: "[ใคร ?]",
		Recurring: "🔄 รายการประจำ: ", Slot: "งวด %s",
		Debt: "🤝 รายการหนี้: ", IOwe: "เราติด%s %s", OwesMe: "%sติดเรา %s", Everyone: "ทุกคนติดเราคนละ %s",
		Lend: "ให้%sยืม", Borrow: "ยืม%s", OwesMeTitle: "%sค้างฉัน", IOweTitle: "ฉันค้าง%s",
		RepayIn: "%sคืนเงิน", RepayOut: "คืนเงิน%s",
		MoneyOut: "💸 รายจ่าย: ออกจาก %s %s", MoneyIn: "💸 รายรับ: เข้า %s %s", NoOut: "➖ ไม่มีรายจ่าย", NoIn: "➖ ไม่มีรายรับ",
		NotOutOfWallet: "💸 รายจ่าย: ไม่ออกจากกระเป๋า",
		Months: [12]string{"ม.ค.", "ก.พ.", "มี.ค.", "เม.ย.", "พ.ค.", "มิ.ย.", "ก.ค.", "ส.ค.", "ก.ย.", "ต.ค.", "พ.ย.", "ธ.ค."},
	},
}

var textsEN = Texts{
	NoAmount:       "No amount found. Try again, e.g. lunch 60",
	NotMoney:       "I can only log income, expenses and transfers.",
	TransferWallet: "Tell me the wallet, and whether money leaves or arrives, e.g. withdraw 500 from Kbank.",
	TooLong:        "That message is too long.",
	TooFast:        "A bit too fast. Wait a moment and try again.",
	TierFree:       "🔒 This feature isn't open yet.",
	CapReached:     "📊 You've used this month's quota. You can use it again on the 1st.",
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
	PartialBooked:  "⚠️ Booked %d of %d. The rest could not be booked.",
	Queued:         "Sent to the app to confirm ✓",
	MissingFmt:     "Still missing: %s · Reply on the card to fill it",
	MissingNames:   missingEN,
	ButtonOK:       "✓ Confirm",
	ButtonCancel:   "✗ Cancel",
	Edited:         "✏️ Edited, see the new card below",
	Items:          "%d items",
	Expense:        "Expense", Income: "Income", Transfer: "Transfer", Today: "today", Yesterday: "yesterday",
	None:     "none",
	Category: "Category: ", Wallet: "Wallet: ",
	GoalMissing: "Tell me which goal, please.", EventDone: "That event is finished.", Unlinked: "Booked, but it could not be tied to the recurring item", InsufficientSaving: "There is not enough saved to take that out.",
	Card: CardWords{
		Save: "Saving", Allocate: "Allocate", Move: "Move between goals", Withdraw: "Withdraw savings",
		Goal: "🐷 Goal: ", Spare: "Unallocated", BackTo: "👛 Back to: ",
		Event: "🏝️ Event: ", NewEvent: "🆕 New event: ", People: "👥 ", Me: "me",
		PaidMe: "💳 I paid", PaidBy: "💳 %s paid", Equal: "split equally, %d people",
		MyShare: "🧮 My share %s", Notify: "📨 Notify: ", NobodyYet: "[who ?]",
		Recurring: "🔄 Recurring: ", Slot: "period of %s",
		Debt: "🤝 Debt: ", IOwe: "I owe %s %s", OwesMe: "%s owes me %s", Everyone: "everyone owes me %s each",
		Lend: "Lent to %s", Borrow: "Borrowed from %s", OwesMeTitle: "%s owes me", IOweTitle: "I owe %s",
		RepayIn: "%s paid back", RepayOut: "Paid back %s",
		MoneyOut: "💸 Expense: out of %s %s", MoneyIn: "💸 Income: into %s %s", NoOut: "➖ No expense", NoIn: "➖ No income",
		NotOutOfWallet: "💸 Expense: not out of a wallet",
		Months: [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
	},
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
	HintGoalMissing    = "goal_missing"
	HintEventDone      = "event_done"
	HintUnknown        = "unknown" // items present; with no items it reads as not_money
)

func (t Texts) hint(h string) string {
	switch h {
	case HintNoAmount:
		return t.NoAmount
	case HintGoalMissing:
		return t.GoalMissing
	case HintEventDone:
		return t.EventDone
	case HintTransferWallet:
		return t.TransferWallet
	}
	return t.NotMoney
}

// Missing: what a card still lacks, by the pending page's readiness code.
var missingTH = map[string]string{"wallet": "กระเป๋า", "wallet_to": "กระเป๋าปลายทาง", "amount": "ยอด", "date": "วันที่", "type": "ประเภท", "people": "คนในบิล"}
var missingEN = map[string]string{"wallet": "wallet", "wallet_to": "destination wallet", "amount": "amount", "date": "date", "type": "type", "people": "people"}

// missing: the fixed line ✓ answers when the card is not ready.
func (t Texts) missing(codes []string) string {
	names := make([]string, 0, len(codes))
	for _, c := range codes {
		if l, ok := t.MissingNames[c]; ok {
			names = append(names, l)
		} else {
			names = append(names, c)
		}
	}
	return fmt.Sprintf(t.MissingFmt, strings.Join(names, ", "))
}
