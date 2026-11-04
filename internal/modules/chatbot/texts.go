// Package chatbot turns a chat message into transactions (0.4.2-4): text → LLM → JSON → checked
// items → a card with [✓ ยืนยัน] [✗ ยกเลิก]. Every word the bot sends is fixed here, a name from
// the user's own lists, or the user's own words copied by the model; never text the model wrote.
package chatbot

import "github.com/ppChub722/chubi-pocket-be/internal/platform/llm"

// Fixed lines.
const (
	TextNoAmount       = "ไม่เห็นยอดเงินครับ ลองพิมพ์ใหม่ เช่น ข้าว 60"
	TextNotMoney       = "จดได้แค่รายรับ รายจ่าย และการโอนครับ"
	TextTransferWallet = "บอกกระเป๋าต้นทางและปลายทางด้วยครับ"
	TextTooLong        = "ข้อความยาวเกินไปครับ"
	TextTooFast        = "ส่งเร็วไปนิดครับ รอสักครู่แล้วลองใหม่"
	TextDown           = "ตอนนี้จดอัตโนมัติไม่ได้ ลองใหม่อีกครั้งนะครับ"
	TextHandled        = "รายการนี้จัดการไปแล้วครับ"

	// Failures: the user is told whose side it is (owner 2026-11-04); the code line follows.
	TextFailAI      = "🤖 ระบบ AI มีปัญหาชั่วคราว ลองใหม่อีกครั้งนะครับ"
	TextFailServer  = "🛠️ ระบบของเรามีปัญหา ลองใหม่ภายหลังนะครับ"
	TextFailRefused = "🙅 ข้อความนี้จดไม่ได้ ลองพิมพ์ใหม่นะครับ"
	TextFailTrunc   = "📏 รายการเยอะเกินไป ลองแบ่งส่งทีละน้อยนะครับ"
	TextOff         = "🛠️ ยังไม่เปิดจดรายการครับ"
	TextThinking    = "⏳ กำลังอ่านรายการ…"
	TextSaved          = "บันทึกแล้ว ✓"
	TextCancelled      = "ยกเลิกแล้ว"
	TextFakeMark       = " (fake)"
	TextTrialMark      = " (ทดลอง ยังไม่ลงแอป)"

	ButtonOK     = "✓ ยืนยัน"
	ButtonCancel = "✗ ยกเลิก"

	labelExpense  = "รายจ่าย"
	labelIncome   = "รายรับ"
	labelTransfer = "โอน"
	labelToday    = "วันนี้"
	labelYday     = "เมื่อวาน"
	labelNone     = "ไม่ระบุ"
	sep           = " | "
)

// Error codes of the chatbot itself; provider codes come from llm.ErrorCode.
const (
	CodeDB        = "E-DB"
	CodeRefused   = "E-REFUSED"
	CodeTruncated = "E-TRUNC"
	CodeBadJSON   = "E-JSON"
)

// failText: the line for a code, by whose side it is.
func failText(code string) string {
	switch code {
	case CodeRefused:
		return TextFailRefused
	case CodeTruncated:
		return TextFailTrunc
	case llm.Code5xx, llm.Code429, llm.CodeTimeout, CodeBadJSON:
		return TextFailAI
	}
	return TextFailServer
}

// Hints the model may give when nothing is bookable; anything else reads as not_money.
const (
	HintNoAmount       = "no_amount"
	HintNotMoney       = "not_money"
	HintTransferWallet = "transfer_wallet_missing"
	HintUnknown        = "unknown" // items present; with no items it reads as not_money
)

func hintText(h string) string {
	switch h {
	case HintNoAmount:
		return TextNoAmount
	case HintTransferWallet:
		return TextTransferWallet
	}
	return TextNotMoney
}
