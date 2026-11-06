package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultAPIBase is Telegram's Bot API host; tests point the client at an httptest server.
const DefaultAPIBase = "https://api.telegram.org"

// Client calls the Bot API. The token sits in every request URL, so no error this client returns
// carries a URL or the token (see redact).
type Client struct {
	base  string
	token string
	http  *http.Client

	mu       sync.Mutex
	username string // the bot's @username, cached from getMe
}

func NewClient(token, base string, hc *http.Client) *Client {
	if base == "" {
		base = DefaultAPIBase
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: hc}
}

// SendMessage posts text to a chat, as a reply to replyTo when it is > 0 (still sent when that
// message is gone).
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, replyTo int64) error {
	body := map[string]any{"chat_id": chatID, "text": text}
	if replyTo > 0 {
		body["reply_parameters"] = map[string]any{
			"message_id":                  replyTo,
			"allow_sending_without_reply": true,
		}
	}
	return c.call(ctx, "sendMessage", body, nil)
}

// Button — one inline keyboard button; Data comes back in a callback_query.
type Button struct {
	Text  string `json:"text"`
	Data  string `json:"callback_data"`
	Style string `json:"style,omitempty"` // "success" green | "danger" red | "primary" blue; empty = Telegram's grey
}

// SendHTML posts text in HTML parse mode (the caller escapes anything that is not markup), as a reply
// to replyTo when it is > 0.
func (c *Client) SendHTML(ctx context.Context, chatID int64, text string, replyTo int64) error {
	body := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML"}
	if replyTo > 0 {
		body["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	return c.call(ctx, "sendMessage", body, nil)
}

// SendForceReply posts text that opens the reply box on the user's side, with a placeholder in it; the
// message answers replyTo. It returns the new message's id.
func (c *Client) SendForceReply(ctx context.Context, chatID int64, text string, replyTo int64, placeholder string) (int64, error) {
	body := map[string]any{"chat_id": chatID, "text": text,
		"reply_markup": map[string]any{"force_reply": true, "input_field_placeholder": placeholder}}
	if replyTo > 0 {
		body["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	err := c.call(ctx, "sendMessage", body, &sent)
	return sent.MessageID, err
}

// SendButtons posts text with one row of inline buttons, as a reply to replyTo.
func (c *Client) SendButtons(ctx context.Context, chatID int64, text string, replyTo int64, buttons []Button) error {
	body := map[string]any{"chat_id": chatID, "text": text,
		"reply_markup": map[string]any{"inline_keyboard": [][]Button{buttons}}}
	if replyTo > 0 {
		body["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	return c.call(ctx, "sendMessage", body, nil)
}

// SendCard posts text with one row of inline buttons and returns the new message's id (a chatbot
// card, edited later).
func (c *Client) SendCard(ctx context.Context, chatID int64, text string, replyTo int64, buttons []Button) (int64, error) {
	body := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML",
		"reply_markup": map[string]any{"inline_keyboard": [][]Button{buttons}}}
	if replyTo > 0 {
		body["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	err := c.call(ctx, "sendMessage", body, &sent)
	return sent.MessageID, err
}

// EditCard replaces a message's text (HTML) and its button row; no buttons = the row goes.
func (c *Client) EditCard(ctx context.Context, chatID, messageID int64, text string, buttons []Button) error {
	body := map[string]any{"chat_id": chatID, "message_id": messageID, "text": text, "parse_mode": "HTML"}
	if len(buttons) > 0 {
		body["reply_markup"] = map[string]any{"inline_keyboard": [][]Button{buttons}}
	}
	return c.call(ctx, "editMessageText", body, nil)
}

// AnswerCallback stops the button's spinner; text (optional) shows as a small notice.
func (c *Client) AnswerCallback(ctx context.Context, callbackID, text string) error {
	body := map[string]any{"callback_query_id": callbackID}
	if text != "" {
		body["text"] = text
	}
	return c.call(ctx, "answerCallbackQuery", body, nil)
}

// SetReaction puts one emoji reaction on a message (it must be in Telegram's reaction set);
// "" clears it.
func (c *Client) SetReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	reaction := []map[string]string{}
	if emoji != "" {
		reaction = append(reaction, map[string]string{"type": "emoji", "emoji": emoji})
	}
	return c.call(ctx, "setMessageReaction", map[string]any{"chat_id": chatID, "message_id": messageID,
		"reaction": reaction}, nil)
}

// SendDraft shows a temporary bubble in a private chat while a reply is being made; it goes when
// the bot sends a message, or after about 30 s.
// canStop adds Telegram's stop button (Bot API 10.3); a press comes back as a stopped_message_generation update.
func (c *Client) SendDraft(ctx context.Context, chatID, draftID int64, text string, canStop bool) error {
	body := map[string]any{"chat_id": chatID, "draft_id": draftID, "text": text}
	if canStop {
		body["can_stop"] = true
	}
	return c.call(ctx, "sendMessageDraft", body, nil)
}

// SetCommands sets the command menu for a language ("" = the default for everyone else).
func (c *Client) SetCommands(ctx context.Context, lang string, cmds map[string]string, order []string) error {
	list := make([]map[string]string, 0, len(order))
	for _, name := range order {
		list = append(list, map[string]string{"command": name, "description": cmds[name]})
	}
	body := map[string]any{"commands": list}
	if lang != "" {
		body["language_code"] = lang
	}
	return c.call(ctx, "setMyCommands", body, nil)
}

// SetAbout sets the bot's description (the empty-chat text) and short description (profile line) for a
// language ("" = default).
func (c *Client) SetAbout(ctx context.Context, lang, description, short string) error {
	body := map[string]any{"description": description}
	sbody := map[string]any{"short_description": short}
	if lang != "" {
		body["language_code"], sbody["language_code"] = lang, lang
	}
	if err := c.call(ctx, "setMyDescription", body, nil); err != nil {
		return err
	}
	return c.call(ctx, "setMyShortDescription", sbody, nil)
}

// EditText replaces a message's text (its buttons go).
func (c *Client) EditText(ctx context.Context, chatID, messageID int64, text string) error {
	return c.call(ctx, "editMessageText", map[string]any{"chat_id": chatID, "message_id": messageID, "text": text}, nil)
}

// SetWebhook points Telegram at url; every call then carries secret in
// X-Telegram-Bot-Api-Secret-Token. Messages and button taps (1b-2 login) are asked for.
// The stop button's press (stopped_message_generation) is asked for too; if Telegram does not know that
// update, the webhook is set again without it so the bot never loses its messages.
func (c *Client) SetWebhook(ctx context.Context, webhookURL, secret string) error {
	set := func(updates ...string) error {
		return c.call(ctx, "setWebhook", map[string]any{
			"url":             webhookURL,
			"secret_token":    secret,
			"allowed_updates": updates,
		}, nil)
	}
	if err := set("message", "callback_query", "stopped_message_generation"); err != nil {
		return set("message", "callback_query")
	}
	return nil
}

func (c *Client) GetWebhookInfo(ctx context.Context) (WebhookInfo, error) {
	var info WebhookInfo
	err := c.call(ctx, "getWebhookInfo", nil, &info)
	return info, err
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var me User
	err := c.call(ctx, "getMe", nil, &me)
	if err == nil && me.Username != "" {
		c.mu.Lock()
		c.username = me.Username
		c.mu.Unlock()
	}
	return me, err
}

// Username — the bot's @username (without @) for t.me links: cached from getMe (asked once more
// when the start-up getMe failed).
func (c *Client) Username(ctx context.Context) (string, error) {
	c.mu.Lock()
	u := c.username
	c.mu.Unlock()
	if u != "" {
		return u, nil
	}
	me, err := c.GetMe(ctx)
	if err != nil {
		return "", err
	}
	return me.Username, nil
}

// minPhotoSide — the profile photo size imported (0.4.2 1b: the smallest one at least this wide).
const minPhotoSide = 160

// ProfilePhoto — the user's newest profile picture as JPEG (getUserProfilePhotos → the smallest
// size ≥ 160 px, else the largest → getFile → download). nil, nil = no photo (or the user's privacy
// hides it from bots). The download URL holds the token: it never leaves this client.
func (c *Client) ProfilePhoto(ctx context.Context, userID int64) ([]byte, error) {
	var photos struct {
		Photos [][]PhotoSize `json:"photos"`
	}
	if err := c.call(ctx, "getUserProfilePhotos", map[string]any{"user_id": userID, "limit": 1}, &photos); err != nil {
		return nil, err
	}
	if len(photos.Photos) == 0 || len(photos.Photos[0]) == 0 {
		return nil, nil
	}
	pick := pickPhotoSize(photos.Photos[0])
	var file struct {
		FilePath string `json:"file_path"`
	}
	if err := c.call(ctx, "getFile", map[string]any{"file_id": pick.FileID}, &file); err != nil {
		return nil, err
	}
	if file.FilePath == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/file/bot"+c.token+"/"+file.FilePath, nil)
	if err != nil {
		return nil, c.redact("download", err)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, c.redact("download", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram download: HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, c.redact("download", err)
	}
	return b, nil
}

// pickPhotoSize — the smallest size with width ≥ 160, else the widest.
func pickPhotoSize(sizes []PhotoSize) PhotoSize {
	var best, widest *PhotoSize
	for i := range sizes {
		s := &sizes[i]
		if widest == nil || s.Width > widest.Width {
			widest = s
		}
		if s.Width >= minPhotoSide && (best == nil || s.Width < best.Width) {
			best = s
		}
	}
	if best != nil {
		return *best
	}
	return *widest
}

func (c *Client) call(ctx context.Context, method string, body any, out any) error {
	var payload io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("telegram %s: encode: %w", method, err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, payload)
	if err != nil {
		return c.redact(method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return c.redact(method, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return c.redact(method, err)
	}
	var env apiResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d, not JSON", method, res.StatusCode)
	}
	if !env.OK {
		return c.redact(method, fmt.Errorf("HTTP %d, error %d: %s", res.StatusCode, env.ErrorCode, env.Description))
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	return nil
}

// redact turns err into a message without the request URL and with the token masked. A
// *url.Error prints the whole URL (the token is in its path), so only its cause is kept.
func (c *Client) redact(method string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	if c.token != "" {
		msg = strings.ReplaceAll(msg, c.token, "<token>")
	}
	return fmt.Errorf("telegram %s: %s", method, msg)
}
