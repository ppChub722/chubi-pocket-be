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

// SetWebhook points Telegram at url; every call then carries secret in
// X-Telegram-Bot-Api-Secret-Token. Only "message" updates are asked for.
func (c *Client) SetWebhook(ctx context.Context, webhookURL, secret string) error {
	return c.call(ctx, "setWebhook", map[string]any{
		"url":             webhookURL,
		"secret_token":    secret,
		"allowed_updates": []string{"message"},
	}, nil)
}

func (c *Client) GetWebhookInfo(ctx context.Context) (WebhookInfo, error) {
	var info WebhookInfo
	err := c.call(ctx, "getWebhookInfo", nil, &info)
	return info, err
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var me User
	err := c.call(ctx, "getMe", nil, &me)
	return me, err
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
