package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

// UploadFile sends a local file to a Telegram chat over MTProto using the
// configured user session. It runs entirely in-process: the session is
// materialised into a private temp file for the lifetime of the call and
// removed afterwards, so concurrent uploads never share session state.
func UploadFile(ctx context.Context, cfg *Config, result *MediaResult, chatID int64, username, formatType, caption string) error {
	if result == nil || result.FilePath == "" || chatID == 0 {
		return fmt.Errorf("file path and chat id are required for sending")
	}

	appIDInt, err := strconv.Atoi(cfg.TGAppID)
	if err != nil {
		return fmt.Errorf("TG_APP_ID must be a valid integer")
	}
	if strings.TrimSpace(cfg.TGSession) == "" {
		return fmt.Errorf("TG_SESSION is required for uploads")
	}

	data, err := base64.StdEncoding.DecodeString(cfg.TGSession)
	if err != nil {
		return fmt.Errorf("session base64 decode failed: %w", err)
	}

	sessionFile, err := os.CreateTemp("", "tg-session-*.json")
	if err != nil {
		return fmt.Errorf("create session file: %w", err)
	}
	sessionPath := sessionFile.Name()
	_ = sessionFile.Close()
	defer os.Remove(sessionPath)

	if err := os.WriteFile(sessionPath, data, 0600); err != nil {
		return fmt.Errorf("session file write failed: %w", err)
	}

	client := telegram.NewClient(appIDInt, cfg.TGAppHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
	})

	return client.Run(ctx, func(ctx context.Context) error {
		me, err := client.Self(ctx)
		if err != nil {
			return fmt.Errorf("mtproto: not authenticated (run --setup-session first): %w", err)
		}
		log.Printf("mtproto: authenticated as user %d (%s), delivering %s to chat %d (username %q)",
			me.ID, me.FirstName, formatType, chatID, username)

		peer, err := resolvePeer(ctx, client, chatID, username)
		if err != nil {
			return fmt.Errorf("mtproto: resolve peer for chat %d (username %q): %w", chatID, username, err)
		}

		return sendFile(ctx, client, peer, result, formatType, caption)
	})
}

type terminalAuth struct {
	phone string
}

func (t terminalAuth) Phone(ctx context.Context) (string, error) {
	if t.phone != "" {
		return t.phone, nil
	}
	fmt.Print("Enter phone number (international format, e.g. +989123456789): ")
	var input string
	_, _ = fmt.Scan(&input)
	return input, nil
}

func (t terminalAuth) Code(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
	fmt.Print("Enter verification code sent to your Telegram: ")
	var code string
	_, _ = fmt.Scan(&code)
	return code, nil
}

func (t terminalAuth) Password(ctx context.Context) (string, error) {
	fmt.Print("Enter 2FA password (press Enter if you don't have one): ")
	var password string
	_, _ = fmt.Scan(&password)
	return password, nil
}

func (t terminalAuth) AcceptTermsOfService(ctx context.Context, tos tg.HelpTermsOfService) error {
	fmt.Println("Telegram terms of service must be accepted to continue.")
	return nil
}

func (t terminalAuth) SignUp(ctx context.Context) (auth.UserInfo, error) {
	fmt.Print("Enter first name: ")
	var first string
	_, _ = fmt.Scan(&first)
	fmt.Print("Enter last name: ")
	var last string
	_, _ = fmt.Scan(&last)
	return auth.UserInfo{FirstName: first, LastName: last}, nil
}

func runSessionSetup(cfg *Config, sessionPath string) error {
	if cfg.TGAppID == "" || cfg.TGAppHash == "" {
		return fmt.Errorf("TG_APP_ID and TG_APP_HASH are required in .env before running --setup-session")
	}

	appIDInt, err := strconv.Atoi(cfg.TGAppID)
	if err != nil {
		return fmt.Errorf("TG_APP_ID must be a valid integer")
	}

	phone := cfg.TGPhone

	fmt.Println("=== Telegram Session Setup ===")
	fmt.Println("This will authenticate your Telegram account and generate a session string")
	fmt.Println("that can be used as TG_SESSION in .env.")
	fmt.Println()

	if phone == "" {
		fmt.Print("Enter phone number (international format, e.g. +989123456789): ")
		_, _ = fmt.Scan(&phone)
	}

	client := telegram.NewClient(appIDInt, cfg.TGAppHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	flow := auth.NewFlow(terminalAuth{phone: phone}, auth.SendCodeOptions{})

	err = client.Run(ctx, func(ctx context.Context) error {
		if err := flow.Run(ctx, client.Auth()); err != nil {
			return fmt.Errorf("phone auth failed: %w", err)
		}

		me, err := client.Self(ctx)
		if err != nil {
			return fmt.Errorf("verify self: %w", err)
		}

		fmt.Printf("\nAuthenticated successfully as %s (ID: %d)\n", me.FirstName, me.ID)
		return nil
	})
	if err != nil {
		return err
	}

	sessionData, err := os.ReadFile(sessionPath)
	if err != nil {
		return fmt.Errorf("read session file: %w", err)
	}

	sessionB64 := base64.StdEncoding.EncodeToString(sessionData)

	fmt.Println()
	fmt.Println("=== Your TG_SESSION value ===")
	fmt.Println()
	fmt.Println(sessionB64)
	fmt.Println()

	fmt.Println("Copy the above value and:")
	fmt.Println("  1. Set TG_SESSION=<value> in your .env file")

	fmt.Print("\nDo you want to automatically update .env with TG_SESSION? (y/n): ")
	var answer string
	_, _ = fmt.Scan(&answer)

	if strings.ToLower(answer) == "y" || strings.ToLower(answer) == "yes" {
		if err := updateEnvFile(".env", "TG_SESSION", sessionB64); err != nil {
			return fmt.Errorf("update .env failed: %w", err)
		}
		fmt.Println("TG_SESSION has been written to .env file successfully.")
	} else {
		fmt.Println("Skipping .env update. Set TG_SESSION manually.")
	}

	return nil
}

func updateEnvFile(path, key, value string) error {
	lines, err := readEnvLines(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	found := false
	for i, line := range lines {
		stripped := strings.TrimSpace(line)
		if strings.HasPrefix(stripped, key+"=") {
			lines[i] = key + "=" + value
			found = true
			break
		}
	}

	if !found {
		lines = append(lines, key+"="+value)
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

func readEnvLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}

func resolvePeer(ctx context.Context, client *telegram.Client, chatID int64, username string) (tg.InputPeerClass, error) {
	if username != "" {
		result, err := client.API().ContactsResolveUsername(ctx, strings.TrimPrefix(username, "@"))
		if err != nil {
			return nil, fmt.Errorf("resolve username '%s': %w", username, err)
		}

		for _, u := range result.Users {
			user, ok := u.(*tg.User)
			if !ok {
				continue
			}
			if user.ID == chatID {
				return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, nil
			}
		}

		for _, u := range result.Users {
			user, ok := u.(*tg.User)
			if !ok {
				continue
			}
			if strings.EqualFold(user.Username, strings.TrimPrefix(username, "@")) {
				return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, nil
			}
		}
	}

	if chatID > 0 {
		result, err := client.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetPeer:    &tg.InputPeerEmpty{},
			Limit:         100,
			ExcludePinned: true,
		})
		if err != nil {
			return nil, fmt.Errorf("get dialogs: %w", err)
		}

		var users []tg.UserClass
		switch r := result.(type) {
		case *tg.MessagesDialogs:
			users = r.Users
		case *tg.MessagesDialogsSlice:
			users = r.Users
		}

		for _, u := range users {
			user, ok := u.(*tg.User)
			if !ok {
				continue
			}
			if user.ID == chatID {
				return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, nil
			}
		}

		return nil, fmt.Errorf("user %d not found in dialogs (provide --username)", chatID)
	}

	if chatID < -1000000000 {
		channelID := -chatID - 1000000000
		chResult, err := client.API().ChannelsGetChannels(ctx, []tg.InputChannelClass{
			&tg.InputChannel{ChannelID: channelID},
		})
		if err != nil {
			return nil, fmt.Errorf("get channel %d: %w", channelID, err)
		}

		for _, c := range chResult.GetChats() {
			ch, ok := c.(*tg.Channel)
			if !ok {
				continue
			}
			if ch.ID == channelID {
				return &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}, nil
			}
		}

		return nil, fmt.Errorf("channel %d not found", channelID)
	}

	return &tg.InputPeerChat{ChatID: -chatID}, nil
}

func sendFile(ctx context.Context, client *telegram.Client, peer tg.InputPeerClass, result *MediaResult, formatType, caption string) error {
	filePath := result.FilePath
	filename := filepath.Base(filePath)
	ext := strings.ToLower(filepath.Ext(filePath))

	up := uploader.NewUploader(client.API())
	uploaded, err := up.FromPath(ctx, filePath)
	if err != nil {
		return fmt.Errorf("mtproto: upload %q: %w", filename, err)
	}

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("mtproto: stat %q: %w", filename, err)
	}
	log.Printf("mtproto: uploaded %s (%d bytes), sending as %s", filename, fileInfo.Size(), formatType)

	var mimeType string
	var attrs []tg.DocumentAttributeClass

	switch formatType {
	case "video":
		mimeType = mimeTypeFromExt(ext, "video/mp4")
		video := &tg.DocumentAttributeVideo{
			Duration:          float64(result.Duration),
			W:                 result.Width,
			H:                 result.Height,
			SupportsStreaming: true,
		}
		attrs = []tg.DocumentAttributeClass{
			video,
			&tg.DocumentAttributeFilename{FileName: filename},
		}
	case "audio":
		mimeType = mimeTypeFromExt(ext, "audio/mpeg")
		attrs = []tg.DocumentAttributeClass{
			&tg.DocumentAttributeAudio{Duration: result.Duration, Title: result.Title},
			&tg.DocumentAttributeFilename{FileName: filename},
		}
	default:
		mimeType = "application/octet-stream"
		attrs = []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: filename},
		}
	}

	media := &tg.InputMediaUploadedDocument{
		File:       uploaded,
		MimeType:   mimeType,
		Attributes: attrs,
	}

	randomID, err := client.RandInt64()
	if err != nil {
		return fmt.Errorf("mtproto: generate random id: %w", err)
	}

	req := &tg.MessagesSendMediaRequest{
		Peer:     peer,
		Media:    media,
		Message:  caption,
		RandomID: randomID,
	}

	_, err = client.API().MessagesSendMedia(ctx, req)
	if err != nil {
		return fmt.Errorf("mtproto: send media %q: %w", filename, err)
	}

	log.Printf("mtproto: sent %s to peer %d", filename, peerID(peer))
	return nil
}

func mimeTypeFromExt(ext, fallback string) string {
	switch ext {
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".wav":
		return "audio/wav"
	default:
		return fallback
	}
}

func peerID(peer tg.InputPeerClass) int64 {
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return int64(p.UserID)
	case *tg.InputPeerChat:
		return -p.ChatID
	case *tg.InputPeerChannel:
		return -(p.ChannelID + 1000000000)
	default:
		return 0
	}
}
