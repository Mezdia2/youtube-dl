package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultLanguage = "fa"
	langPersian     = "fa"
	langEnglish     = "en"
)

var supportedLanguages = map[string]string{
	langPersian: "فارسی",
	langEnglish: "English",
}

type Localizer struct {
	messages map[string]map[string]string
}

func LoadLocalizer(dir string) (*Localizer, error) {
	l := &Localizer{messages: make(map[string]map[string]string)}
	for code := range supportedLanguages {
		path := filepath.Join(dir, code+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read locale %s: %w", code, err)
		}
		var messages map[string]string
		if err := json.Unmarshal(data, &messages); err != nil {
			return nil, fmt.Errorf("decode locale %s: %w", code, err)
		}
		l.messages[code] = messages
	}
	return l, nil
}

func NormalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if _, ok := supportedLanguages[lang]; ok {
		return lang
	}
	return defaultLanguage
}

func IsSupportedLanguage(lang string) bool {
	_, ok := supportedLanguages[strings.ToLower(strings.TrimSpace(lang))]
	return ok
}

func (l *Localizer) T(lang, key string, args ...any) string {
	lang = NormalizeLanguage(lang)
	text := ""
	if l.messages != nil {
		text = l.messages[lang][key]
		if text == "" && lang != defaultLanguage {
			text = l.messages[defaultLanguage][key]
		}
	}
	if text == "" {
		text = key
	}
	if len(args) > 0 {
		return fmt.Sprintf(text, args...)
	}
	return text
}

func (l *Localizer) LanguageKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "🇮🇷 " + supportedLanguages[langPersian], CallbackData: "lang:" + langPersian},
				{Text: "🇺🇸 🇬🇧 " + supportedLanguages[langEnglish], CallbackData: "lang:" + langEnglish},
			},
		},
	}
}
