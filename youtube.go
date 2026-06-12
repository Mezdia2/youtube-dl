package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type VideoInfo struct {
	ID          string
	Title       string
	Description string
	Duration    int64
	Thumbnail   string
	Formats     []DownloadOption
}

type DownloadOption struct {
	FormatType string
	Quality    string
	Label      string
	SizeBytes  int64
}

type ytdlpInfo struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Duration    float64       `json:"duration"`
	Thumbnail   string        `json:"thumbnail"`
	Formats     []ytdlpFormat `json:"formats"`
}

type ytdlpFormat struct {
	FormatID   string  `json:"format_id"`
	Ext        string  `json:"ext"`
	Height     int     `json:"height"`
	VCodec     string  `json:"vcodec"`
	ACodec     string  `json:"acodec"`
	Filesize   int64   `json:"filesize"`
	FilesizeAp int64   `json:"filesize_approx"`
	TBR        float64 `json:"tbr"`
	ABR        float64 `json:"abr"`
}

var videoQualityOrder = []string{"best", "1080", "720", "480", "360"}
var audioQualityOrder = []string{"mp3", "m4a"}

const ytdlpMaxCacheAge = 24 * time.Hour

// modernPlayerClients is an explicit set of player clients that often work
// without sign-in. YouTube periodically breaks individual clients, so this is
// only the first thing we try — see fetchAttempts for the default-client
// fallback that relies on yt-dlp's own, continuously-updated client list.
const modernPlayerClients = "youtube:player_client=tv,mweb,web_safari,ios"

type fetchAttempt struct {
	name string
	args []string
}

// fetchAttempts builds the ordered list of yt-dlp invocations to try. The key
// robustness fix over the previous code is the "default clients" attempts,
// which omit --extractor-args entirely so yt-dlp falls back to its built-in
// client list; when YouTube breaks the hardcoded clients above, those forced
// attempts all fail and metadata fetching would otherwise error out.
func fetchAttempts(url, cookiePath string) []fetchAttempt {
	attempts := []fetchAttempt{
		{name: "cookieless/modern-clients", args: buildInfoArgs("--extractor-args", modernPlayerClients, url)},
		{name: "cookieless/default-clients", args: buildInfoArgs(url)},
	}
	if cookiePath != "" {
		attempts = append(attempts,
			fetchAttempt{name: "cookies/modern-clients", args: buildInfoArgs("--extractor-args", modernPlayerClients, "--cookies", cookiePath, url)},
			fetchAttempt{name: "cookies/default-clients", args: buildInfoArgs("--cookies", cookiePath, url)},
		)
	}
	return attempts
}

func FetchVideoInfo(ctx context.Context, cfg *Config, url string) (*VideoInfo, error) {
	ytdlpPath, err := ResolveYTDLP(ctx, cfg)
	if err != nil {
		return nil, err
	}

	cookiePath, cookieCleanup, cookieErr := writeTempCookies(cfg)
	defer cookieCleanup()
	if cookieErr != nil {
		// Bad cookies should not block the cookieless attempts; just log and
		// proceed without them.
		log.Printf("cookies setup failed, continuing without cookies: %v", cookieErr)
	}

	attempts := fetchAttempts(url, cookiePath)

	info, firstErr := runFetchAttempts(ctx, ytdlpPath, attempts)
	if info != nil {
		return info, nil
	}
	log.Printf("yt-dlp metadata attempts failed: %v", firstErr)

	refreshedPath, refreshed, refreshErr := RefreshYTDLP(ctx, cfg)
	if refreshErr != nil {
		return nil, fmt.Errorf("all attempts failed: %v (yt-dlp refresh error: %w)", firstErr, refreshErr)
	}
	if !refreshed {
		return nil, firstErr
	}

	info, err = runFetchAttempts(ctx, refreshedPath, attempts)
	if info != nil {
		return info, nil
	}
	return nil, fmt.Errorf("all attempts failed after yt-dlp refresh: %v", err)
}

// runFetchAttempts tries each attempt in order, returning the first successful
// parse. It returns the last error when every attempt fails.
func runFetchAttempts(ctx context.Context, ytdlpPath string, attempts []fetchAttempt) (*VideoInfo, error) {
	var lastErr error
	for _, a := range attempts {
		stdout, err := runYTDLP(ctx, ytdlpPath, a.args)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", a.name, err)
			log.Printf("yt-dlp attempt %s failed: %v", a.name, err)
			continue
		}
		info, parseErr := parseVideoInfo(stdout)
		if parseErr != nil {
			lastErr = fmt.Errorf("%s: %w", a.name, parseErr)
			log.Printf("yt-dlp attempt %s parse failed: %v", a.name, parseErr)
			continue
		}
		return info, nil
	}
	return nil, lastErr
}

func buildInfoArgs(extras ...string) []string {
	args := []string{
		"--dump-json",
		"--no-playlist",
		"--skip-download",
		// Still emit the metadata JSON (title, duration, thumbnail) when no
		// downloadable format can be resolved — e.g. when YouTube's "n" signature
		// challenge cannot be solved without a JS runtime. Without this, yt-dlp
		// aborts with "Requested format is not available" and we get nothing back
		// even though the preview metadata is available. The actual format
		// selection happens later, in the server-side downloader.
		"--ignore-no-formats-error",
		"--extractor-retries", "3",
		"--retry-sleep", "extractor:3",
		"--socket-timeout", "20",
	}
	return append(args, extras...)
}

func parseVideoInfo(data []byte) (*VideoInfo, error) {
	var raw ytdlpInfo
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode yt-dlp metadata: %w", err)
	}
	if raw.Title == "" {
		return nil, errors.New("yt-dlp metadata missing title")
	}
	info := &VideoInfo{
		ID:          raw.ID,
		Title:       raw.Title,
		Description: trimDescription(raw.Description),
		Duration:    int64(math.Round(raw.Duration)),
		Thumbnail:   raw.Thumbnail,
		Formats:     buildDownloadOptions(raw),
	}
	return info, nil
}

func runYTDLP(ctx context.Context, ytdlpPath string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, ytdlpPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("yt-dlp metadata failed: %s", msg)
	}
	return stdout.Bytes(), nil
}

func ResolveYTDLP(ctx context.Context, cfg *Config) (string, error) {
	if cfg != nil && strings.TrimSpace(cfg.YTDLPPath) != "" {
		if _, err := os.Stat(cfg.YTDLPPath); err != nil {
			return "", fmt.Errorf("YT_DLP_PATH is not usable: %w", err)
		}
		return cfg.YTDLPPath, nil
	}
	if path, err := exec.LookPath("yt-dlp"); err == nil {
		return path, nil
	}
	path, _, err := resolveCachedYTDLP(ctx, false)
	return path, err
}

func RefreshYTDLP(ctx context.Context, cfg *Config) (string, bool, error) {
	if cfg != nil && strings.TrimSpace(cfg.YTDLPPath) != "" {
		return cfg.YTDLPPath, false, nil
	}
	return resolveCachedYTDLP(ctx, true)
}

func resolveCachedYTDLP(ctx context.Context, forceRefresh bool) (string, bool, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", false, fmt.Errorf("find user cache dir for yt-dlp: %w", err)
	}
	targetName, downloadURL, err := ytdlpDownloadTarget()
	if err != nil {
		return "", false, err
	}

	dir := filepath.Join(cacheDir, "youtube-dl-bot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("create yt-dlp cache dir: %w", err)
	}
	path := filepath.Join(dir, targetName)
	if stat, err := os.Stat(path); err == nil && stat.Size() > 0 {
		if !forceRefresh && time.Since(stat.ModTime()) <= ytdlpMaxCacheAge {
			return path, false, nil
		}
		if err := downloadYTDLP(ctx, downloadURL, path); err != nil {
			if !forceRefresh {
				return path, false, nil
			}
			return "", false, err
		}
		return path, true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", false, fmt.Errorf("stat yt-dlp cache file: %w", err)
	}
	if err := downloadYTDLP(ctx, downloadURL, path); err != nil {
		return "", false, err
	}
	return path, true, nil
}

func downloadYTDLP(ctx context.Context, downloadURL, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create yt-dlp download request: %w", err)
	}
	req.Header.Set("User-Agent", "youtube-dl-bot")

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download yt-dlp: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download yt-dlp failed with status %d", resp.StatusCode)
	}

	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create yt-dlp file: %w", err)
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write yt-dlp file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close yt-dlp file: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmp, 0o755); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("chmod yt-dlp file: %w", err)
		}
	}
	_ = os.Remove(path)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("move yt-dlp file into cache: %w", err)
	}
	return nil
}

func ytdlpDownloadTarget() (string, string, error) {
	base := "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"
	switch runtime.GOOS {
	case "linux":
		if runtime.GOARCH != "amd64" {
			return "", "", fmt.Errorf("automatic yt-dlp download is not supported on linux/%s; set YT_DLP_PATH", runtime.GOARCH)
		}
		return "yt-dlp_linux", base + "yt-dlp_linux", nil
	case "windows":
		if runtime.GOARCH != "amd64" {
			return "", "", fmt.Errorf("automatic yt-dlp download is not supported on windows/%s; set YT_DLP_PATH", runtime.GOARCH)
		}
		return "yt-dlp.exe", base + "yt-dlp.exe", nil
	case "darwin":
		if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
			return "", "", fmt.Errorf("automatic yt-dlp download is not supported on darwin/%s; set YT_DLP_PATH", runtime.GOARCH)
		}
		return "yt-dlp_macos", base + "yt-dlp_macos", nil
	default:
		return "", "", fmt.Errorf("automatic yt-dlp download is not supported on %s/%s; set YT_DLP_PATH", runtime.GOOS, runtime.GOARCH)
	}
}

func writeTempCookies(cfg *Config) (string, func(), error) {
	cleanup := func() {}
	if cfg == nil || strings.TrimSpace(cfg.YTCookiesB64) == "" {
		return "", cleanup, nil
	}
	data, err := base64.StdEncoding.DecodeString(cfg.YTCookiesB64)
	if err != nil {
		return "", cleanup, fmt.Errorf("YT_COOKIES_B64 must be valid base64: %w", err)
	}
	file, err := os.CreateTemp("", "yt-cookies-*.txt")
	if err != nil {
		return "", cleanup, fmt.Errorf("create cookies file: %w", err)
	}
	path := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", cleanup, fmt.Errorf("write cookies file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", cleanup, fmt.Errorf("close cookies file: %w", err)
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func buildDownloadOptions(raw ytdlpInfo) []DownloadOption {
	videoSizes := map[string]int64{}
	audioSizes := map[string]int64{}

	bestVideo := int64(0)
	bestAudio := int64(0)
	for _, f := range raw.Formats {
		size := formatSize(f, raw.Duration)
		if size <= 0 {
			continue
		}
		hasVideo := f.VCodec != "" && f.VCodec != "none"
		hasAudio := f.ACodec != "" && f.ACodec != "none"

		if hasVideo {
			if size > bestVideo {
				bestVideo = size
			}
			for _, q := range []int{1080, 720, 480, 360} {
				if f.Height > 0 && f.Height <= q {
					key := strconv.Itoa(q)
					videoSizes[key] = maxInt64(videoSizes[key], size)
				}
			}
		}
		if hasAudio {
			bestAudio = maxInt64(bestAudio, size)
			if strings.EqualFold(f.Ext, "m4a") {
				audioSizes["m4a"] = maxInt64(audioSizes["m4a"], size)
			}
			audioSizes["mp3"] = maxInt64(audioSizes["mp3"], size)
		}
	}
	if bestAudio > 0 {
		for quality, size := range videoSizes {
			videoSizes[quality] = size + bestAudio
		}
	}
	if bestVideo > 0 {
		videoSizes["best"] = bestVideo + bestAudio
	}

	options := make([]DownloadOption, 0, 7)
	for _, q := range videoQualityOrder {
		if size := videoSizes[q]; size > 0 {
			label := q + "p"
			if q == "best" {
				label = "Best"
			}
			options = append(options, DownloadOption{
				FormatType: "video",
				Quality:    q,
				Label:      label,
				SizeBytes:  size,
			})
		}
	}
	for _, q := range audioQualityOrder {
		if size := audioSizes[q]; size > 0 {
			options = append(options, DownloadOption{
				FormatType: "audio",
				Quality:    q,
				Label:      strings.ToUpper(q),
				SizeBytes:  size,
			})
		}
	}

	sort.SliceStable(options, func(i, j int) bool {
		return optionRank(options[i]) < optionRank(options[j])
	})
	return options
}

func formatSize(f ytdlpFormat, duration float64) int64 {
	if f.Filesize > 0 {
		return f.Filesize
	}
	if f.FilesizeAp > 0 {
		return f.FilesizeAp
	}
	bitrate := f.TBR
	if bitrate <= 0 {
		bitrate = f.ABR
	}
	if bitrate > 0 && duration > 0 {
		return int64((bitrate * 1000 / 8) * duration)
	}
	return 0
}

func optionRank(o DownloadOption) int {
	for i, q := range videoQualityOrder {
		if o.FormatType == "video" && o.Quality == q {
			return i
		}
	}
	for i, q := range audioQualityOrder {
		if o.FormatType == "audio" && o.Quality == q {
			return len(videoQualityOrder) + i
		}
	}
	return 99
}

func trimDescription(description string) string {
	description = strings.TrimSpace(description)
	if len([]rune(description)) <= 400 {
		return description
	}
	runes := []rune(description)
	return string(runes[:400]) + "..."
}

func formatDuration(seconds int64) string {
	if seconds <= 0 {
		return "-"
	}
	d := time.Duration(seconds) * time.Second
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%d:%02d", minutes, secs)
}

func formatBytes(bytes int64) string {
	if bytes <= 0 {
		return "-"
	}
	units := []string{"B", "KB", "MB", "GB"}
	value := float64(bytes)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", bytes, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
