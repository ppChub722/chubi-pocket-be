// Package telegram is the Telegram bot (0.4.2): Telegram calls POST /api/v1/telegram/webhook with
// each update and the bot answers in the chat. Bot API over plain net/http, webhook only.
// 1a = hello: a text from an allowed private chat is kept and answered "ได้รับข้อความแล้ว ✓".
package telegram

import "encoding/json"

// Update is one incoming update; only the fields the bot reads.
// https://core.telegram.org/bots/api#update
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	EditedMessage *Message       `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// CallbackQuery — a tap on an inline button (1b-2 login). Message = the bot's message with it.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private | group | supergroup | channel
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// Message: the media fields are kept raw; only their presence matters (a non-text message).
type Message struct {
	MessageID int64           `json:"message_id"`
	Chat      Chat            `json:"chat"`
	From      *User           `json:"from,omitempty"`
	Date      int64           `json:"date"` // unix seconds
	Text      string          `json:"text"`
	Photo     json.RawMessage `json:"photo,omitempty"`
	Document  json.RawMessage `json:"document,omitempty"`
	Voice     json.RawMessage `json:"voice,omitempty"`
	Sticker   json.RawMessage `json:"sticker,omitempty"`
}

// Kind names what a message holds, for the log line (never the text itself).
func (m *Message) Kind() string {
	switch {
	case m.Text != "":
		return "text"
	case len(m.Photo) > 0:
		return "photo"
	case len(m.Document) > 0:
		return "document"
	case len(m.Voice) > 0:
		return "voice"
	case len(m.Sticker) > 0:
		return "sticker"
	}
	return "other"
}

// PhotoSize — one size of a profile photo (getUserProfilePhotos).
type PhotoSize struct {
	FileID string `json:"file_id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// WebhookInfo: the getWebhookInfo fields the start log shows.
type WebhookInfo struct {
	URL                string   `json:"url"`
	PendingUpdateCount int      `json:"pending_update_count"`
	LastErrorDate      int64    `json:"last_error_date"`
	LastErrorMessage   string   `json:"last_error_message"`
	AllowedUpdates     []string `json:"allowed_updates"`
}

// apiResponse is the Bot API envelope.
type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}
