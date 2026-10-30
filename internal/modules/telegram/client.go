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
	Text string `json:"text"`
	Data string `json:"callback_data"`
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

// AnswerCallback stops the button's spinner; text (optional) shows as a small notice.
func (c *Client) AnswerCallback(ctx context.Context, callbackID, text string) error {
	body := map[string]any{"callback_query_id": callbackID}
	if text != "" {
		body["text"] = text
	}
	return c.call(ctx, "answerCallbackQuery", body, nil)
}

// EditText replaces a message's text (its buttons go).
func (c *Client) EditText(ctx context.Context, chatID, messageID int64, text string) error {
	return c.call(ctx, "editMessageText", map[string]any{"chat_id": chatID, "message_id": messageID, "text": text}, nil)
}

// SetWebhook points Telegram at url; every call then carries secret in
// X-Telegram-Bot-Api-Secret-Token. Messages and button taps (1b-2 login) are asked for.
func (c *Client) SetWebhook(ctx context.Context, webhookURL, secret string) error {
	return c.call(ctx, "setWebhook", map[string]any{
		"url":             webhookURL,
		"secret_token":    secret,
		"allowed_updates": []string{"message", "callback_query"},
	}, nil)
}

// HasProfilePhoto — whether the user has a profile photo the bot may see (sign-up prefill).
func (c *Client) HasProfilePhoto(ctx context.Context, userID int64) (bool, error) {
	var photos struct {
		TotalCount int `json:"total_count"`
	}
	if err := c.call(ctx, "getUserProfilePhotos", map[string]any{"user_id": userID, "limit": 1}, &photos); err != nil {
		return false, err
	}
	return photos.TotalCount > 0, nil
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
