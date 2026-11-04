// Package chatbot turns a chat message into transactions (0.4.2-4): text → LLM → JSON → checked
// items → a card with [✓ ยืนยัน] [✗ ยกเลิก]. Every word the bot sends is fixed here, a name from
// the user's own lists, or the user's own words copied by the model; never text the model wrote.
package chatbot

// Fixed lines.
const (
	TextNoAmount       = "ไม่เห็นยอดเงินครับ ลองพิมพ์ใหม่ เช่น ข้าว 60"
	TextNotMoney       = "จดได้แค่รายรับ รายจ่าย และการโอนครับ"
	TextTransferWallet = "บอกกระเป๋าต้นทางและปลายทางด้วยครับ"
	TextTooLong        = "ข้อความยาวเกินไปครับ"
	TextTooFast        = "ส่งเร็วไปนิดครับ รอสักครู่แล้วลองใหม่"
	TextDown           = "ตอนนี้จดอัตโนมัติไม่ได้ ลองใหม่อีกครั้งนะครับ"
	TextHandled        = "รายการนี้จัดการไปแล้ว"
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
	unsureMark    = "?"
	sep           = " | "
)

// Hints the model may give when nothing is bookable; anything else reads as not_money.
const (
	HintNoAmount       = "no_amount"
	HintNotMoney       = "not_money"
	HintTransferWallet = "transfer_wallet_missing"
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
