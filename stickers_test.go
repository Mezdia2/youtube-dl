package main

import "testing"

func TestCanonicalEmoji(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain wave", "👋", "👋"},
		{"thumbs down with skin tone", "👎🏻", "👎"},
		{"snowflake with variation selector", "❄️", "❄"},
		{"snowflake without variation selector", "❄", "❄"},
		{"right magnifier folds to left", "🔎", "🔍"},
		{"left magnifier unchanged", "🔍", "🔍"},
		{"eyes", "👀", "👀"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canonicalEmoji(c.in); got != c.want {
				t.Fatalf("canonicalEmoji(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCanonicalEmojiMatchesAcrossVariants(t *testing.T) {
	// A sticker tagged "🔎" must be found when looking up "🔍" and vice versa.
	if canonicalEmoji("🔎") != canonicalEmoji("🔍") {
		t.Fatal("magnifier variants should canonicalize to the same key")
	}
	// Skin-tone thumbs-down must match the base thumbs-down used for lookups.
	if canonicalEmoji("👎🏻") != canonicalEmoji(emojiThumbDown) {
		t.Fatal("skin-tone thumbs-down should match base thumbs-down")
	}
	// Snowflake with/without the emoji-presentation selector must match.
	if canonicalEmoji("❄") != canonicalEmoji(emojiSnowflake) {
		t.Fatal("snowflake variants should canonicalize to the same key")
	}
}
