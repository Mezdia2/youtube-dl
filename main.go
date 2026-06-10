package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type BotAPI struct {
	token     string
	client    *http.Client
	cfg       *Config
	store     *Store
	localizer *Localizer
	stickers  *stickerManager
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      Chat   `json:"chat"`
	From      *User  `json:"from"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
	From    User     `json:"from"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

func main() {
	filePath := flag.String("file", "", "path to file to send")
	chatID := flag.Int64("chat-id", 0, "target telegram chat id")
	username := flag.String("username", "", "target telegram username (without @)")
	formatType := flag.String("format", "video", "format: video, audio, document")
	caption := flag.String("caption", "", "message caption")
	doSetupSession := flag.Bool("setup-session", false, "run interactive session setup: authenticate via phone number and output TG_SESSION value")
	sessionPath := flag.String("session", "session.json", "session file path")
	flag.Parse()

	cfg := LoadConfig()

	if *doSetupSession {
		if err := runSessionSetup(cfg, *sessionPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *filePath != "" || *chatID != 0 {
		if err := runUploader(cfg, *filePath, *chatID, *username, *formatType, *caption, *sessionPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := runBot(); err != nil {
		log.Fatalf("bot stopped: %v", err)
	}
}

func runBot() error {
	cfg := LoadConfig()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	startupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	store, err := NewStore(startupCtx, cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer store.Close()

	localizer, err := LoadLocalizer("locales")
	if err != nil {
		return err
	}

	bot := &BotAPI{
		token:     cfg.BotToken,
		client:    &http.Client{Timeout: 35 * time.Second},
		cfg:       cfg,
		store:     store,
		localizer: localizer,
		stickers:  newStickerManager(),
	}

	webhookURL, err := cfg.WebhookURL()
	if err != nil {
		return err
	}
	if err := bot.SetWebhook(context.Background(), webhookURL, cfg.TelegramWebhookSecret()); err != nil {
		return fmt.Errorf("set webhook: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc(cfg.WebhookPath, bot.WebhookHandler(cfg.TelegramWebhookSecret()))

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	log.Printf("bot webhook server started on :%s path=%s", cfg.Port, cfg.WebhookPath)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

func (c *Config) WebhookURL() (string, error) {
	domain := strings.TrimSpace(c.WebhookDomain)
	if domain == "" {
		return "", fmt.Errorf("WEBHOOK_DOMAIN env is required")
	}
	if !strings.HasPrefix(domain, "https://") && !strings.HasPrefix(domain, "http://") {
		domain = "https://" + domain
	}

	baseURL, err := url.Parse(domain)
	if err != nil {
		return "", fmt.Errorf("parse WEBHOOK_DOMAIN: %w", err)
	}
	if baseURL.Scheme != "https" {
		return "", fmt.Errorf("WEBHOOK_DOMAIN must use https for Telegram webhooks")
	}
	if baseURL.Host == "" {
		return "", fmt.Errorf("WEBHOOK_DOMAIN must include a host")
	}

	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + c.WebhookPath
	baseURL.RawQuery = ""
	baseURL.Fragment = ""

	return baseURL.String(), nil
}

func (b *BotAPI) SetWebhook(ctx context.Context, webhookURL, secretToken string) error {
	payload := map[string]any{
		"url":                  webhookURL,
		"secret_token":         secretToken,
		"drop_pending_updates": true,
		"allowed_updates":      []string{"message", "callback_query"},
	}

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := b.Call(ctx, "setWebhook", payload, &result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("telegram setWebhook failed: %s", result.Description)
	}
	return nil
}

func (b *BotAPI) WebhookHandler(secretToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != secretToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var update Update
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := b.HandleUpdate(ctx, update); err != nil {
				log.Printf("handle webhook update failed: %v", err)
			}
		}()
	}
}

func (b *BotAPI) HandleUpdate(ctx context.Context, update Update) error {
	if update.CallbackQuery != nil {
		if strings.HasPrefix(update.CallbackQuery.Data, "lang:") {
			return onLanguageSelect(ctx, b, update.CallbackQuery)
		}
		return onQualitySelect(ctx, b, update.CallbackQuery)
	}
	if update.Message == nil {
		return nil
	}

	// In groups the bot only engages when a YouTube link is present; other
	// chatter (including commands and the language prompt) is ignored to avoid
	// spamming the group. Private chats always engage.
	if update.Message.Chat.Type != "private" && !isYouTubeURL(update.Message.Text) {
		return nil
	}

	userID := update.Message.Chat.ID
	if update.Message.From != nil {
		userID = update.Message.From.ID
	}
	_, ok, err := b.store.GetUserLanguage(ctx, userID)
	if err != nil {
		return err
	}
	if !ok {
		SetPendingRequest(userID, update.Message)
		return b.SendMessage(ctx, update.Message.Chat.ID, b.localizer.T(defaultLanguage, "choose_language"), b.localizer.LanguageKeyboard())
	}

	return b.handleMessage(ctx, update.Message)
}

func (b *BotAPI) handleMessage(ctx context.Context, msg *Message) error {
	text := msg.Text
	switch text {
	case "/start":
		return onStart(ctx, b, msg)
	case "/help":
		return onHelp(ctx, b, msg)
	case "/cancel":
		return onCancel(ctx, b, msg)
	default:
		return onText(ctx, b, msg)
	}
}

func (b *BotAPI) SendMessage(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) error {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return b.Call(ctx, "sendMessage", payload, nil)
}

func (b *BotAPI) SendPhoto(ctx context.Context, chatID int64, photo, caption string, markup *InlineKeyboardMarkup) error {
	payload := map[string]any{
		"chat_id": chatID,
		"photo":   photo,
		"caption": caption,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return b.Call(ctx, "sendPhoto", payload, nil)
}

func (b *BotAPI) SendSticker(ctx context.Context, chatID int64, fileID string) error {
	return b.Call(ctx, "sendSticker", map[string]any{
		"chat_id": chatID,
		"sticker": fileID,
	}, nil)
}

// SetMessageReaction places a single emoji reaction on a message. Only emojis
// from Telegram's fixed reaction set are accepted (e.g. 👀); others are rejected
// by the API, so callers should use it only for known-valid reaction emojis.
func (b *BotAPI) SetMessageReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	return b.Call(ctx, "setMessageReaction", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"reaction": []map[string]any{
			{"type": "emoji", "emoji": emoji},
		},
	}, nil)
}

func (b *BotAPI) AnswerCallback(ctx context.Context, callbackID string) error {
	return b.Call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
	}, nil)
}

func (b *BotAPI) Call(ctx context.Context, method string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read telegram response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram status %d: %s", resp.StatusCode, string(respBody))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode telegram response: %w", err)
	}
	return nil
}

func (b *BotAPI) URL(method string) string {
	return fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.token, method)
}
