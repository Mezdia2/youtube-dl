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
	doSetupSession := flag.Bool("setup-session", false, "run interactive session setup: authenticate via phone number and output TG_SESSION value")
	sessionPath := flag.String("session", "session.json", "session file path")
	flag.Parse()

	if *doSetupSession {
		cfg := LoadConfig()
		if err := runSessionSetup(cfg, *sessionPath); err != nil {
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

	// The per-user language prompt only makes sense in private chats, where
	// Chat.ID equals the user's id and the user can answer the keyboard
	// privately. In groups (or for anonymous senders where From is nil) we must
	// not push the language keyboard into the chat — doing so spams the group
	// and strands the link in pendingRequests, since the group user cannot
	// complete the flow. Groups simply fall back to the default language.
	if update.Message.Chat.Type == "private" && update.Message.From != nil {
		userID := update.Message.From.ID
		_, ok, err := b.store.GetUserLanguage(ctx, userID)
		if err != nil {
			return err
		}
		if !ok {
			SetPendingRequest(userID, update.Message)
			// The language prompt is shown before we know the user's language, so
			// it is always rendered in English.
			return b.SendMessage(ctx, update.Message.Chat.ID, b.localizer.T(langEnglish, "choose_language"), b.localizer.LanguageKeyboard())
		}
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
	_, err := b.SendMessageReturning(ctx, chatID, text, markup)
	return err
}

// SendMessageReturning sends a text message and returns the id of the sent
// message so callers can later edit or delete it.
func (b *BotAPI) SendMessageReturning(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) (int64, error) {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return b.callReturningMessageID(ctx, "sendMessage", payload)
}

func (b *BotAPI) SendPhoto(ctx context.Context, chatID int64, photo, caption string, markup *InlineKeyboardMarkup) error {
	_, err := b.SendPhotoReturning(ctx, chatID, photo, caption, markup)
	return err
}

// SendPhotoReturning sends a photo and returns the id of the sent message.
func (b *BotAPI) SendPhotoReturning(ctx context.Context, chatID int64, photo, caption string, markup *InlineKeyboardMarkup) (int64, error) {
	payload := map[string]any{
		"chat_id": chatID,
		"photo":   photo,
		"caption": caption,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return b.callReturningMessageID(ctx, "sendPhoto", payload)
}

func (b *BotAPI) SendSticker(ctx context.Context, chatID int64, fileID string) error {
	_, err := b.SendStickerReturning(ctx, chatID, fileID)
	return err
}

// SendStickerReturning sends a sticker and returns the id of the sent message.
func (b *BotAPI) SendStickerReturning(ctx context.Context, chatID int64, fileID string) (int64, error) {
	return b.callReturningMessageID(ctx, "sendSticker", map[string]any{
		"chat_id": chatID,
		"sticker": fileID,
	})
}

// callReturningMessageID performs a send-style Telegram call and extracts the
// message_id of the resulting message from the response.
func (b *BotAPI) callReturningMessageID(ctx context.Context, method string, payload any) (int64, error) {
	var resp struct {
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := b.Call(ctx, method, payload, &resp); err != nil {
		return 0, err
	}
	return resp.Result.MessageID, nil
}

// DeleteMessage removes a previously sent message.
func (b *BotAPI) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return b.Call(ctx, "deleteMessage", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}, nil)
}

// deleteMessageBestEffort deletes a message immediately, logging (but not
// returning) any failure. A zero messageID is a no-op.
func (b *BotAPI) deleteMessageBestEffort(ctx context.Context, chatID, messageID int64) {
	if messageID == 0 {
		return
	}
	if err := b.DeleteMessage(ctx, chatID, messageID); err != nil {
		log.Printf("delete message %d failed: %v", messageID, err)
	}
}

// deleteMessageAfter schedules a message for deletion after delay. It runs in
// its own goroutine with a fresh context so it survives the originating
// request. A zero messageID is a no-op.
func (b *BotAPI) deleteMessageAfter(chatID, messageID int64, delay time.Duration) {
	if messageID == 0 {
		return
	}
	go func() {
		time.Sleep(delay)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		b.deleteMessageBestEffort(ctx, chatID, messageID)
	}()
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

// setReactionBestEffort updates the reaction on a message, logging (but not
// returning) any failure. A zero messageID is a no-op. It is used to keep the
// reaction in step with the job lifecycle (👀 while working → ✅/👎 when done).
func (b *BotAPI) setReactionBestEffort(ctx context.Context, chatID, messageID int64, emoji string) {
	if messageID == 0 {
		return
	}
	if err := b.SetMessageReaction(ctx, chatID, messageID, emoji); err != nil {
		log.Printf("set reaction on message %d failed: %v", messageID, err)
	}
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
