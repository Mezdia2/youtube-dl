package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

func onStart(ctx context.Context, bot *BotAPI, msg *Message) error {
	// The greeting is a sticker only (no text), picked at random from either pack.
	bot.sendRandomSticker(ctx, msg.Chat.ID,
		stickerChoice{pack: stickerPackMain, emoji: emojiWave},
		stickerChoice{pack: stickerPackFull, emoji: emojiSnowflake},
	)
	return nil
}

func onHelp(ctx context.Context, bot *BotAPI, msg *Message) error {
	lang := bot.userLanguage(ctx, msg)
	return bot.SendMessage(ctx, msg.Chat.ID, bot.localizer.T(lang, "help"), nil)
}

func onCancel(ctx context.Context, bot *BotAPI, msg *Message) error {
	lang := bot.userLanguage(ctx, msg)
	DelSession(msg.Chat.ID)
	return bot.SendMessage(ctx, msg.Chat.ID, bot.localizer.T(lang, "cancel"), nil)
}

func onLanguageSelect(ctx context.Context, bot *BotAPI, callback *CallbackQuery) error {
	_ = bot.AnswerCallback(ctx, callback.ID)
	rawLang := strings.TrimPrefix(callback.Data, "lang:")
	if !IsSupportedLanguage(rawLang) {
		if callback.Message == nil {
			return nil
		}
		return bot.SendMessage(ctx, callback.Message.Chat.ID, bot.localizer.T(defaultLanguage, "invalid_language"), nil)
	}
	lang := NormalizeLanguage(rawLang)

	userID := callback.From.ID
	if err := bot.store.SetUserLanguage(ctx, userID, lang); err != nil {
		return err
	}

	chatID := callback.From.ID
	if callback.Message != nil {
		chatID = callback.Message.Chat.ID
	}

	// Grab any pending request before we touch it; we re-run it below.
	pending := GetPendingRequest(userID)

	// The message carrying the language keyboard is the "choose language"
	// prompt itself, so delete it now that a choice has been made.
	if callback.Message != nil {
		bot.deleteMessageBestEffort(ctx, chatID, callback.Message.MessageID)
	}

	// Confirm in the chosen language, then auto-remove the confirmation so it
	// does not linger above the processed request.
	confirmID, err := bot.SendMessageReturning(ctx, chatID, bot.localizer.T(lang, "language_saved"), nil)
	if err != nil {
		return err
	}
	bot.deleteMessageAfter(chatID, confirmID, 5*time.Second)

	if pending == nil {
		return nil
	}
	DelPendingRequest(userID)
	return bot.handleMessage(ctx, &Message{
		Text: pending.Text,
		Chat: Chat{
			ID:   pending.ChatID,
			Type: pending.ChatType,
		},
		From: &User{ID: pending.UserID},
	})
}

func (bot *BotAPI) userLanguage(ctx context.Context, msg *Message) string {
	userID := msg.Chat.ID
	if msg.From != nil {
		userID = msg.From.ID
	}
	lang, ok, err := bot.store.GetUserLanguage(ctx, userID)
	if err != nil {
		log.Printf("get user language failed: %v", err)
		return defaultLanguage
	}
	if !ok {
		return defaultLanguage
	}
	return NormalizeLanguage(lang)
}

func isYouTubeURL(text string) bool {
	lower := strings.ToLower(text)
	patterns := []string{
		"youtube.com/watch",
		"youtu.be/",
		"youtube.com/shorts/",
		"youtube.com/v/",
		"youtube.com/embed/",
		"m.youtube.com/watch",
		"youtube.com/live/",
		"music.youtube.com/",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func onText(ctx context.Context, bot *BotAPI, msg *Message) error {
	lang := bot.userLanguage(ctx, msg)
	text := strings.TrimSpace(msg.Text)

	if !isYouTubeURL(text) {
		// Only reply in private chats; groups stay quiet (also enforced upstream).
		if msg.Chat.Type == "private" {
			bot.sendStickerOrEmoji(ctx, msg.Chat.ID, stickerPackFull, emojiSearch)
			return bot.SendMessage(ctx, msg.Chat.ID, bot.localizer.T(lang, "not_a_command"), nil)
		}
		return nil
	}

	// Acknowledge the link: react 👀 on the user's message, then show a
	// "searching" sticker that stays up until the result is ready.
	if msg.MessageID != 0 {
		if err := bot.SetMessageReaction(ctx, msg.Chat.ID, msg.MessageID, emojiEyes); err != nil {
			log.Printf("set reaction failed: %v", err)
		}
	}
	searchStickerID := bot.sendStickerOrEmojiReturning(ctx, msg.Chat.ID, stickerPackFull, emojiSearch)

	info, err := FetchVideoInfo(ctx, bot.cfg, text)
	if err != nil {
		log.Printf("fetch video info failed: %v", err)
		bot.deleteMessageBestEffort(ctx, msg.Chat.ID, searchStickerID)
		return bot.SendMessage(ctx, msg.Chat.ID, bot.localizer.T(lang, "metadata_failed"), nil)
	}

	SetSession(msg.Chat.ID, text)
	markup := qualityKeyboard(bot.localizer, lang, info.Formats)
	caption := videoCaption(bot.localizer, lang, info)

	if info.Thumbnail != "" {
		if err := bot.SendPhoto(ctx, msg.Chat.ID, info.Thumbnail, caption, markup); err == nil {
			bot.deleteMessageBestEffort(ctx, msg.Chat.ID, searchStickerID)
			return nil
		}
		log.Printf("send photo metadata failed, falling back to message")
	}

	sendErr := bot.SendMessage(ctx, msg.Chat.ID, caption+"\n\n"+bot.localizer.T(lang, "quality_prompt"), markup)
	bot.deleteMessageBestEffort(ctx, msg.Chat.ID, searchStickerID)
	return sendErr
}

func qualityKeyboard(l *Localizer, lang string, options []DownloadOption) *InlineKeyboardMarkup {
	rows := make([][]InlineKeyboardButton, 0, 4)
	row := make([]InlineKeyboardButton, 0, 2)
	for _, option := range options {
		textKey := "video_button"
		qualityLabel := option.Label
		if option.Quality == "best" {
			qualityLabel = l.T(lang, "best_quality")
		}
		if option.FormatType == "audio" {
			textKey = "audio_button"
		}
		row = append(row, InlineKeyboardButton{
			Text:         l.T(lang, textKey, qualityLabel, formatBytes(option.SizeBytes)),
			CallbackData: callbackData(option.FormatType, option.Quality),
		})
		if len(row) == 2 {
			rows = append(rows, row)
			row = make([]InlineKeyboardButton, 0, 2)
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = [][]InlineKeyboardButton{
			{
				{Text: l.T(lang, "video_button", l.T(lang, "best_quality"), "-"), CallbackData: "v_best"},
				{Text: l.T(lang, "audio_button", "MP3", "-"), CallbackData: "a_mp3"},
			},
		}
	}
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

func videoCaption(l *Localizer, lang string, info *VideoInfo) string {
	description := info.Description
	if description == "" {
		description = l.T(lang, "no_description")
	}
	return l.T(lang, "video_info", info.Title, formatDuration(info.Duration), description)
}

func callbackData(formatType, quality string) string {
	if formatType == "audio" {
		return "a_" + quality
	}
	return "v_" + quality
}

func parseCallbackData(data string) (formatType, quality string) {
	if strings.HasPrefix(data, "v_") {
		return "video", strings.TrimPrefix(data, "v_")
	}
	if strings.HasPrefix(data, "a_") {
		return "audio", strings.TrimPrefix(data, "a_")
	}
	return "", ""
}

func onQualitySelect(ctx context.Context, bot *BotAPI, callback *CallbackQuery) error {
	_ = bot.AnswerCallback(ctx, callback.ID)
	if callback.Message == nil {
		return nil
	}

	chatID := callback.Message.Chat.ID
	lang := defaultLanguage
	if storedLang, ok, err := bot.store.GetUserLanguage(ctx, callback.From.ID); err == nil && ok {
		lang = storedLang
	}

	session := GetSession(chatID)
	if session == nil {
		return bot.SendMessage(ctx, chatID, bot.localizer.T(lang, "expired_session"), nil)
	}

	formatType, quality := parseCallbackData(callback.Data)
	if formatType == "" {
		return bot.SendMessage(ctx, chatID, bot.localizer.T(lang, "invalid_selection"), nil)
	}

	chatIDStr := fmt.Sprintf("%d", chatID)
	username := callback.From.Username

	if err := TriggerWorkflow(bot.cfg, session.URL, formatType, quality, chatIDStr, username, lang); err != nil {
		log.Printf("workflow trigger failed: %v (format=%s quality=%s chatID=%s)",
			err, formatType, quality, chatIDStr)
		bot.sendRandomSticker(ctx, chatID,
			stickerChoice{pack: stickerPackFull, emoji: emojiProhibit},
			stickerChoice{pack: stickerPackMain, emoji: emojiThumbDown},
		)
		return bot.SendMessage(ctx, chatID, bot.localizer.T(lang, "workflow_failed"), nil)
	}

	DelSession(chatID)

	// The quality menu has served its purpose; remove it so only the download
	// status remains for this step.
	bot.deleteMessageBestEffort(ctx, chatID, callback.Message.MessageID)

	log.Printf("workflow triggered: format=%s quality=%s chatID=%s username=%s",
		formatType, quality, chatIDStr, username)

	return bot.SendMessage(ctx, chatID, bot.localizer.T(lang, "download_started"), nil)
}
