package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// MTProto user uploads top out around 2 GiB; reject anything that big and
	// ask the user for a smaller quality, just as the previous pipeline did.
	mtprotoMaxBytes = 2 * 1024 * 1024 * 1024
	// The Bot API multipart fallback only accepts files up to 50 MiB.
	botAPIMaxBytes = 50 * 1024 * 1024
	// Generous ceiling for a single download+upload job.
	downloadJobTimeout = 30 * time.Minute
)

// startDownload runs the full download/upload pipeline for one request in the
// background so the webhook handler returns immediately. reactMsgID is the id of
// the user's link message; the job updates its reaction (👀 → ✅/👎) so the
// reaction stays in step with the sticker/status lifecycle.
func (b *BotAPI) startDownload(chatID, statusMsgID, reactMsgID int64, username, url, formatType, quality, lang string) {
	go b.runDownloadJob(chatID, statusMsgID, reactMsgID, username, url, formatType, quality, lang)
}

func (b *BotAPI) runDownloadJob(chatID, statusMsgID, reactMsgID int64, username, url, formatType, quality, lang string) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadJobTimeout)
	defer cancel()

	log.Printf("job chat=%d: starting download format=%s quality=%s url=%s", chatID, formatType, quality, url)
	result, err := DownloadMedia(ctx, b.cfg, MediaRequest{URL: url, FormatType: formatType, Quality: quality})
	if err != nil {
		log.Printf("job chat=%d: download failed (format=%s quality=%s url=%s): %v", chatID, formatType, quality, url, err)
		b.finishWithError(chatID, statusMsgID, reactMsgID, lang, "download_failed")
		return
	}
	defer result.Cleanup()

	info, err := os.Stat(result.FilePath)
	if err != nil {
		log.Printf("job chat=%d: stat downloaded file %q failed: %v", chatID, result.FilePath, err)
		b.finishWithError(chatID, statusMsgID, reactMsgID, lang, "download_failed")
		return
	}
	size := info.Size()
	log.Printf("job chat=%d: downloaded %q (%s) for %s/%s", chatID, result.Title, formatBytes(size), formatType, quality)

	if size >= mtprotoMaxBytes {
		gb := size / (1024 * 1024 * 1024)
		log.Printf("job chat=%d: file too large to deliver (%s >= 2 GB limit), asking for a lower quality", chatID, formatBytes(size))
		b.removeStatus(chatID, statusMsgID)
		b.signalFailure(chatID, reactMsgID)
		b.notify(chatID, b.localizer.T(lang, "file_too_large", strconv.FormatInt(gb, 10)))
		return
	}

	caption := "✅ " + result.Title

	if err := b.deliverMedia(ctx, result, chatID, username, formatType, caption, size); err != nil {
		log.Printf("job chat=%d: delivery failed for %q (%s): %v", chatID, result.Title, formatBytes(size), err)
		b.reportUploadFailure(chatID, statusMsgID, reactMsgID, lang, size)
		return
	}

	b.removeStatus(chatID, statusMsgID)
	b.signalSuccess(chatID, reactMsgID)
	log.Printf("job chat=%d: delivered %q (%s) successfully", chatID, result.Title, formatBytes(size))
}

// deliverMedia sends the downloaded file to the user. For files within the Bot
// API limit it prefers the Bot API, which can always reach a user who has
// started the bot — including the many users without a public @username that the
// MTProto user-account uploader cannot resolve. Larger files go over MTProto,
// which the Bot API cannot handle. Each path falls back to the other so a
// failure in one still has a chance to deliver.
func (b *BotAPI) deliverMedia(ctx context.Context, result *MediaResult, chatID int64, username, formatType, caption string, size int64) error {
	if size <= botAPIMaxBytes {
		log.Printf("job chat=%d: delivering %s via Bot API (%s within 50 MB limit)", chatID, formatType, formatBytes(size))
		if err := b.sendLocalMedia(ctx, chatID, formatType, result.FilePath, caption); err == nil {
			return nil
		} else {
			log.Printf("job chat=%d: Bot API delivery failed, falling back to MTProto: %v", chatID, err)
		}
	} else {
		log.Printf("job chat=%d: delivering %s via MTProto (%s exceeds 50 MB Bot API limit)", chatID, formatType, formatBytes(size))
	}

	if err := UploadFile(ctx, b.cfg, result, chatID, username, formatType, caption); err != nil {
		return fmt.Errorf("mtproto upload: %w", err)
	}
	return nil
}

func (b *BotAPI) reportUploadFailure(chatID, statusMsgID, reactMsgID int64, lang string, size int64) {
	mb := size / (1024 * 1024)
	b.removeStatus(chatID, statusMsgID)
	b.signalFailure(chatID, reactMsgID)
	b.notify(chatID, b.localizer.T(lang, "upload_failed", strconv.FormatInt(mb, 10)))
}

func (b *BotAPI) finishWithError(chatID, statusMsgID, reactMsgID int64, lang, key string) {
	b.removeStatus(chatID, statusMsgID)
	b.signalFailure(chatID, reactMsgID)
	b.notify(chatID, b.localizer.T(lang, key))
}

// signalSuccess flips the link-message reaction to ✅ once the file has been
// delivered. It uses its own context so it survives the job context.
func (b *BotAPI) signalSuccess(chatID, reactMsgID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b.setReactionBestEffort(ctx, chatID, reactMsgID, emojiCheck)
}

// signalFailure flips the link-message reaction to 👎 and sends the documented
// error sticker. It uses its own context so it survives a cancelled job context.
func (b *BotAPI) signalFailure(chatID, reactMsgID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b.setReactionBestEffort(ctx, chatID, reactMsgID, emojiThumbDown)
	b.sendStickerOrEmoji(ctx, chatID, stickerPackFull, emojiProhibit)
}

// removeStatus deletes the transient "downloading..." message once the job
// reaches a terminal state. It uses its own context so it survives a cancelled
// or expired job context.
func (b *BotAPI) removeStatus(chatID, statusMsgID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b.deleteMessageBestEffort(ctx, chatID, statusMsgID)
}

func (b *BotAPI) notify(chatID int64, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := b.SendMessage(ctx, chatID, text, nil); err != nil {
		log.Printf("notify chat %d failed: %v", chatID, err)
	}
}

// sendLocalMedia uploads a local file through the Bot API as a last resort when
// MTProto delivery fails. It is only viable for files up to 50 MiB.
func (b *BotAPI) sendLocalMedia(ctx context.Context, chatID int64, formatType, filePath, caption string) error {
	method, field := "sendDocument", "document"
	switch formatType {
	case "video":
		method, field = "sendVideo", "video"
	case "audio":
		method, field = "sendAudio", "audio"
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	_ = writer.WriteField("caption", caption)
	if formatType == "video" {
		_ = writer.WriteField("supports_streaming", "true")
	}
	part, err := writer.CreateFormFile(field, filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("copy file into request: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finalize multipart body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL(method), &body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("bot api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bot api status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
