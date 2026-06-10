package main

import (
	"context"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// Sticker pack short names (the part after t.me/addstickers/).
const (
	stickerPackMain = "UtyaDuck"
	stickerPackFull = "UtyaDuckFull"
)

// Emoji used to look up stickers and as text fallbacks when a pack has no match.
const (
	emojiWave      = "👋"
	emojiSnowflake = "❄️"
	emojiEyes      = "👀"
	emojiSearch    = "🔍"
	emojiProhibit  = "🚫"
	emojiThumbDown = "👎"
)

const stickerCacheTTL = 6 * time.Hour

// stickerSet maps a canonical emoji to the file_ids of every sticker tagged with it.
type stickerSet struct {
	byEmoji   map[string][]string
	fetchedAt time.Time
}

// stickerManager caches fetched sticker sets so we only hit getStickerSet rarely.
type stickerManager struct {
	mu   sync.Mutex
	sets map[string]*stickerSet
}

func newStickerManager() *stickerManager {
	return &stickerManager{sets: make(map[string]*stickerSet)}
}

type tgStickerSet struct {
	Name     string      `json:"name"`
	Stickers []tgSticker `json:"stickers"`
}

type tgSticker struct {
	FileID string `json:"file_id"`
	Emoji  string `json:"emoji"`
}

// stickerChoice is one (pack, emoji) candidate for a random pick.
type stickerChoice struct {
	pack  string
	emoji string
}

// canonicalEmoji strips variation selectors, skin-tone modifiers and ZWJ, and
// folds the two magnifier glyphs together, so lookups match regardless of the
// exact codepoints the pack author used (e.g. "👎🏻" matches "👎", "🔎" matches "🔍").
func canonicalEmoji(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 0xFE0F || r == 0xFE0E: // variation selectors
			continue
		case r >= 0x1F3FB && r <= 0x1F3FF: // skin-tone modifiers
			continue
		case r == 0x200D: // zero-width joiner
			continue
		case r == 0x1F50E: // right-pointing magnifier -> left-pointing
			b.WriteRune(0x1F50D)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// loadStickerSet returns the cached set for name, fetching it via getStickerSet
// when missing or stale. A failed fetch is cached as an empty set so we retry
// only after the TTL instead of on every message.
func (b *BotAPI) loadStickerSet(ctx context.Context, name string) *stickerSet {
	b.stickers.mu.Lock()
	defer b.stickers.mu.Unlock()

	if s, ok := b.stickers.sets[name]; ok && time.Since(s.fetchedAt) < stickerCacheTTL {
		return s
	}

	set := &stickerSet{byEmoji: make(map[string][]string), fetchedAt: time.Now()}

	var resp struct {
		Result tgStickerSet `json:"result"`
	}
	if err := b.Call(ctx, "getStickerSet", map[string]any{"name": name}, &resp); err != nil {
		log.Printf("getStickerSet %s failed: %v", name, err)
		b.stickers.sets[name] = set
		return set
	}

	for _, st := range resp.Result.Stickers {
		if st.FileID == "" || st.Emoji == "" {
			continue
		}
		key := canonicalEmoji(st.Emoji)
		set.byEmoji[key] = append(set.byEmoji[key], st.FileID)
	}
	b.stickers.sets[name] = set
	return set
}

// StickerFor returns a random file_id of a sticker in pack tagged with emoji.
func (b *BotAPI) StickerFor(ctx context.Context, pack, emoji string) (string, bool) {
	if b == nil || b.stickers == nil {
		return "", false
	}
	set := b.loadStickerSet(ctx, pack)
	ids := set.byEmoji[canonicalEmoji(emoji)]
	if len(ids) == 0 {
		return "", false
	}
	return ids[rand.Intn(len(ids))], true
}

// sendStickerOrEmoji sends a matching sticker from pack; if the pack has none,
// it sends the emoji itself as a text message. Failures are logged, never fatal.
func (b *BotAPI) sendStickerOrEmoji(ctx context.Context, chatID int64, pack, emoji string) {
	b.sendStickerOrEmojiReturning(ctx, chatID, pack, emoji)
}

// sendStickerOrEmojiReturning behaves like sendStickerOrEmoji but returns the
// id of the message it sent (sticker or emoji fallback) so callers can later
// delete it. It returns 0 if nothing could be sent.
func (b *BotAPI) sendStickerOrEmojiReturning(ctx context.Context, chatID int64, pack, emoji string) int64 {
	if fileID, ok := b.StickerFor(ctx, pack, emoji); ok {
		if id, err := b.SendStickerReturning(ctx, chatID, fileID); err == nil {
			return id
		} else {
			log.Printf("send sticker failed: %v", err)
		}
	}
	if id, err := b.SendMessageReturning(ctx, chatID, emoji, nil); err == nil {
		return id
	} else {
		log.Printf("send emoji fallback failed: %v", err)
	}
	return 0
}

// sendRandomSticker picks one of the choices at random and sends it.
func (b *BotAPI) sendRandomSticker(ctx context.Context, chatID int64, choices ...stickerChoice) {
	if len(choices) == 0 {
		return
	}
	c := choices[rand.Intn(len(choices))]
	b.sendStickerOrEmoji(ctx, chatID, c.pack, c.emoji)
}
