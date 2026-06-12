package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	BotToken       string
	GitHubToken    string
	RepoOwner      string
	RepoName       string
	WorkflowFile   string
	DefaultBranch  string
	TGAppID        string
	TGAppHash      string
	TGSession      string
	TGPhone        string
	YTCookiesB64   string
	YTDLPPath      string
	FFmpegLocation string
	WebhookDomain  string
	WebhookPath    string
	WebhookSecret  string
	Port           string
	MySQLDSN       string
}

func LoadConfig() *Config {
	_ = loadDotEnv(".env")

	return &Config{
		BotToken:       os.Getenv("BOT_TOKEN"),
		GitHubToken:    os.Getenv("GH_TOKEN"),
		RepoOwner:      os.Getenv("GH_REPO_OWNER"),
		RepoName:       os.Getenv("GH_REPO_NAME"),
		WorkflowFile:   envOr("GH_WORKFLOW_FILE", "download.yml"),
		DefaultBranch:  envOr("GH_DEFAULT_BRANCH", "main"),
		TGAppID:        os.Getenv("TG_APP_ID"),
		TGAppHash:      os.Getenv("TG_APP_HASH"),
		TGSession:      os.Getenv("TG_SESSION"),
		TGPhone:        os.Getenv("TG_PHONE"),
		YTCookiesB64:   os.Getenv("YT_COOKIES_B64"),
		YTDLPPath:      os.Getenv("YT_DLP_PATH"),
		FFmpegLocation: os.Getenv("FFMPEG_LOCATION"),
		WebhookDomain:  os.Getenv("WEBHOOK_DOMAIN"),
		WebhookPath:    envOr("WEBHOOK_PATH", "/telegram/webhook"),
		WebhookSecret:  os.Getenv("WEBHOOK_SECRET_TOKEN"),
		Port:           envOr("PORT", "8080"),
		MySQLDSN:       resolveMySQLDSN(),
	}
}

func (c *Config) Validate() error {
	if c.BotToken == "" {
		return fmt.Errorf("BOT_TOKEN env is required")
	}
	if c.GitHubToken == "" {
		return fmt.Errorf("GH_TOKEN env is required")
	}
	if c.RepoOwner == "" {
		return fmt.Errorf("GH_REPO_OWNER env is required")
	}
	if c.RepoName == "" {
		return fmt.Errorf("GH_REPO_NAME env is required")
	}
	if c.WorkflowFile == "" {
		return fmt.Errorf("GH_WORKFLOW_FILE env is required")
	}
	if c.WebhookDomain == "" {
		return fmt.Errorf("WEBHOOK_DOMAIN env is required")
	}
	if c.WebhookPath == "" || !strings.HasPrefix(c.WebhookPath, "/") {
		return fmt.Errorf("WEBHOOK_PATH must start with /")
	}
	if c.Port == "" {
		return fmt.Errorf("PORT env is required")
	}
	if c.MySQLDSN == "" {
		return fmt.Errorf("MYSQL_URL or MYSQL_DSN env is required")
	}
	return nil
}

func (c *Config) TelegramWebhookSecret() string {
	if c.WebhookSecret != "" {
		return c.WebhookSecret
	}
	sum := sha256.Sum256([]byte(c.BotToken))
	return hex.EncodeToString(sum[:])
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func resolveMySQLDSN() string {
	mysqlURL := os.Getenv("MYSQL_URL")
	if mysqlURL != "" {
		dsn, err := mysqlURLToDSN(mysqlURL)
		if err == nil {
			return dsn
		}
		return mysqlURL
	}
	return os.Getenv("MYSQL_DSN")
}

func mysqlURLToDSN(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" {
		return "", fmt.Errorf("no scheme")
	}

	user := ""
	if u.User != nil {
		username := u.User.Username()
		password, hasPass := u.User.Password()
		if hasPass {
			user = username + ":" + password
		} else {
			user = username
		}
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "3306"
	}

	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		db = u.Opaque
	}

	addr := host + ":" + port

	q := u.Query()
	q.Set("parseTime", "true")
	q.Set("charset", "utf8mb4")

	dsn := user + "@tcp(" + addr + ")/" + db + "?" + q.Encode()
	return dsn, nil
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(stripInlineComment(value))
		value = strings.Trim(value, `"'`)

		if key == "" || os.Getenv(key) != "" {
			continue
		}
		_ = os.Setenv(key, value)
	}
	return scanner.Err()
}

func stripInlineComment(value string) string {
	inSingle := false
	inDouble := false

	for i, r := range value {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && (i == 0 || value[i-1] == ' ' || value[i-1] == '\t') {
				return strings.TrimSpace(value[:i])
			}
		}
	}
	return value
}
