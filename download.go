package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// MediaRequest describes a single download the server should perform.
type MediaRequest struct {
	URL        string
	FormatType string // "video" or "audio"
	Quality    string // best, 1080, 720, 480, 360, mp3, m4a
}

// MediaResult is a downloaded file ready to be uploaded. Cleanup removes the
// working directory and must be called once the file has been consumed. The
// video metadata is read from yt-dlp's info JSON so MTProto uploads can carry
// correct duration/dimensions and render as a playable video rather than a
// plain document.
type MediaResult struct {
	FilePath string
	Title    string
	Duration int
	Width    int
	Height   int
	Cleanup  func()
}

const defaultMediaTitle = "YouTube Video"

// downloadAttempt is one yt-dlp invocation strategy. extra carries the
// auth/client-selection flags that distinguish attempts from each other.
type downloadAttempt struct {
	name  string
	extra []string
}

// DownloadMedia downloads the requested video or audio using the server's own
// yt-dlp, trying progressively more permissive strategies. The first success
// wins; if every attempt fails it refreshes yt-dlp once and retries, mirroring
// the metadata path's self-healing behaviour.
func DownloadMedia(ctx context.Context, cfg *Config, req MediaRequest) (*MediaResult, error) {
	if req.FormatType != "video" && req.FormatType != "audio" {
		return nil, fmt.Errorf("invalid format type %q", req.FormatType)
	}

	ytdlpPath, err := ResolveYTDLP(ctx, cfg)
	if err != nil {
		return nil, err
	}

	cookiePath, cookieCleanup, cookieErr := writeTempCookies(cfg)
	defer cookieCleanup()
	if cookieErr != nil {
		// Bad cookies should never block the cookieless attempts.
		log.Printf("cookies setup failed, continuing without cookies: %v", cookieErr)
	}

	ffmpeg := ""
	if cfg != nil {
		ffmpeg = strings.TrimSpace(cfg.FFmpegLocation)
	}
	attempts := downloadAttempts(cookiePath)

	result, firstErr := runDownloadAttempts(ctx, ytdlpPath, ffmpeg, req, attempts)
	if result != nil {
		return result, nil
	}
	log.Printf("yt-dlp download attempts failed: %v", firstErr)

	refreshedPath, refreshed, refreshErr := RefreshYTDLP(ctx, cfg)
	if refreshErr != nil {
		return nil, fmt.Errorf("all download attempts failed: %v (yt-dlp refresh error: %w)", firstErr, refreshErr)
	}
	if !refreshed {
		return nil, firstErr
	}

	result, err = runDownloadAttempts(ctx, refreshedPath, ffmpeg, req, attempts)
	if result != nil {
		return result, nil
	}
	return nil, fmt.Errorf("all download attempts failed after yt-dlp refresh: %v", err)
}

// runDownloadAttempts tries each strategy in turn, returning the first file it
// manages to produce. Every attempt downloads into a fresh working directory so
// a partial failure never contaminates the next try.
func runDownloadAttempts(ctx context.Context, ytdlpPath, ffmpeg string, req MediaRequest, attempts []downloadAttempt) (*MediaResult, error) {
	var lastErr error
	for _, a := range attempts {
		workDir, err := os.MkdirTemp("", "yt-download-*")
		if err != nil {
			return nil, fmt.Errorf("create work dir: %w", err)
		}

		args, err := buildDownloadArgs(req, filepath.Join(workDir, "output.%(ext)s"), ffmpeg, a.extra)
		if err != nil {
			_ = os.RemoveAll(workDir)
			return nil, err
		}

		if err := runYTDLPCommand(ctx, ytdlpPath, args); err != nil {
			_ = os.RemoveAll(workDir)
			lastErr = fmt.Errorf("%s: %w", a.name, err)
			log.Printf("yt-dlp download attempt %s failed: %v", a.name, err)
			continue
		}

		filePath, err := findDownloadedFile(workDir)
		if err != nil {
			_ = os.RemoveAll(workDir)
			lastErr = fmt.Errorf("%s: %w", a.name, err)
			log.Printf("yt-dlp download attempt %s produced no file: %v", a.name, err)
			continue
		}

		dir := workDir
		title, duration, width, height := readMediaMeta(workDir)
		return &MediaResult{
			FilePath: filePath,
			Title:    title,
			Duration: duration,
			Width:    width,
			Height:   height,
			Cleanup:  func() { _ = os.RemoveAll(dir) },
		}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no download attempts were run")
	}
	return nil, lastErr
}

// downloadAttempts orders the strategies cheapest-and-most-likely first. The
// default-clients attempts omit --extractor-args so yt-dlp falls back to its
// own continuously-updated client list when the pinned clients are broken.
func downloadAttempts(cookiePath string) []downloadAttempt {
	attempts := []downloadAttempt{
		{name: "cookieless/modern-clients", extra: []string{"--extractor-args", modernPlayerClients}},
		{name: "cookieless/default-clients"},
	}
	if cookiePath != "" {
		attempts = append(attempts,
			downloadAttempt{name: "cookies/modern-clients", extra: []string{"--extractor-args", modernPlayerClients, "--cookies", cookiePath}},
			downloadAttempt{name: "cookies/default-clients", extra: []string{"--cookies", cookiePath}},
		)
	}
	return attempts
}

func buildDownloadArgs(req MediaRequest, outputTemplate, ffmpeg string, extra []string) ([]string, error) {
	args := append([]string{}, extra...)

	switch req.FormatType {
	case "video":
		args = append(args, "-f", videoFormatSelector(req.Quality), "--merge-output-format", "mp4")
	case "audio":
		if strings.EqualFold(req.Quality, "m4a") {
			args = append(args, "-f", "ba[ext=m4a]/ba/b")
		} else {
			args = append(args, "-x", "--audio-format", "mp3", "--audio-quality", "0")
		}
	default:
		return nil, fmt.Errorf("invalid format type %q", req.FormatType)
	}

	args = append(args,
		"--no-playlist",
		"--no-progress",
		"--write-info-json",
		"--extractor-retries", "3",
		"--retry-sleep", "extractor:5",
		"--sleep-requests", "1",
		"--socket-timeout", "30",
		"-o", outputTemplate,
	)
	if ffmpeg != "" {
		args = append(args, "--ffmpeg-location", ffmpeg)
	}
	return append(args, req.URL), nil
}

func videoFormatSelector(quality string) string {
	switch quality {
	case "1080", "720", "480", "360":
		return fmt.Sprintf("bv*[height<=%s]+ba/b[height<=%s]/bv+ba/b", quality, quality)
	default:
		return "bv*+ba/b"
	}
}

func runYTDLPCommand(ctx context.Context, ytdlpPath string, args []string) error {
	cmd := exec.CommandContext(ctx, ytdlpPath, args...)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := lastStderrLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// findDownloadedFile returns the largest media file in dir, ignoring the
// sidecar info JSON and any leftover partial fragments.
func findDownloadedFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read work dir: %w", err)
	}

	var best string
	var bestSize int64 = -1
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".info.json") || strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".ytdl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Size() > bestSize {
			bestSize = info.Size()
			best = filepath.Join(dir, name)
		}
	}
	if best == "" || bestSize <= 0 {
		return "", errors.New("download produced no output file")
	}
	return best, nil
}

// readMediaMeta pulls the human-readable title and, for video, the duration and
// dimensions from the info JSON yt-dlp writes alongside the media file. Missing
// fields default to zero and the title falls back to a generic label.
func readMediaMeta(dir string) (title string, duration, width, height int) {
	title = defaultMediaTitle
	entries, err := os.ReadDir(dir)
	if err != nil {
		return title, 0, 0, 0
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".info.json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var meta struct {
			Title    string  `json:"title"`
			Duration float64 `json:"duration"`
			Width    int     `json:"width"`
			Height   int     `json:"height"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if strings.TrimSpace(meta.Title) != "" {
			title = meta.Title
			duration = int(meta.Duration)
			width = meta.Width
			height = meta.Height
			return title, duration, width, height
		}
	}
	return title, 0, 0, 0
}

func lastStderrLine(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
