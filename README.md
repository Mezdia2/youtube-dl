<div align="center">

  <h1>
    YouTube DL Bot 
  </h1>

  <p><strong>Telegram Bot for downloading YouTube videos & audio, served straight from the bot's own host via MTProto</strong></p>

  <p>
    <img src="https://img.shields.io/badge/Go-1.22-00ADD8?style=for-the-badge&logo=go" alt="Go 1.22" />
    <img src="https://img.shields.io/badge/gotd%2Ftd-v0.99-00ADD8?style=for-the-badge" alt="gotd/td" />
    <img src="https://img.shields.io/badge/yt--dlp-latest-orange?style=for-the-badge" alt="yt-dlp" />
    <img src="https://img.shields.io/badge/MTProto-2.0-26A5E4?style=for-the-badge" alt="MTProto" />
  </p>

  <br/>

  <em>Send a link → Pick quality → Get the file in Telegram</em>

</div>

---

## Features

- **Video downloads** — Best, 1080p, 720p, 480p, 360p
- **Audio extraction** — MP3 & M4A formats
- **Cookieless-first downloads** — tries modern `yt-dlp` player clients (tv, mweb, web_safari, ios) before falling back to cookies
- **Sticker & reaction UX** — greets with a sticker, reacts 👀 to links, and uses UtyaDuck / UtyaDuckFull stickers for searching and error states (falls back to the emoji as text when a pack has no match)
- **Cookie exporter extension** — bundled Chrome extension (`cookie-extension/`) produces a paste-ready `YT_COOKIES_B64`
- **MTProto upload** — sends files up to 2 GB via gotd/td
- **Bot API fallback** — files ≤ 50 MB sent via Telegram Bot API
- **Webhook mode** — receives Telegram updates via HTTPS webhook (no polling)
- **Healthcheck** — `/healthz` endpoint for Railway and monitoring
- **Secure** — webhook secret token verification; secrets stay in server-side `.env`
- **Session timeout** — 5 min auto-expiry for pending quality selections
- **Multilingual UI** — Persian and English via separate locale JSON files
- **MySQL language storage** — stores each Telegram user's selected language by numeric user ID
- **Video preview** — sends thumbnail, title, duration, description, quality choices, and estimated size before download
- **Interactive session setup** — `--setup-session` command to authenticate and generate `TG_SESSION`

---

## How It Works

```
┌───────────┐      ┌──────────────┐      ┌──────────────────┐
│   User    │─────▶│  Telegram Bot │─────▶│  yt-dlp + ffmpeg │
│ (Telegram)│◀────│  (Go server)  │◀────│  (same host)     │
└───────────┘      └──────────────┘      └──────────────────┘
      │                   │                       │
      │  1. Send URL      │                       │
      │──────────────────▶│                       │
      │                   │  2. Show quality menu  │
      │◀──────────────────│                       │
      │  3. Pick quality  │                       │
      │──────────────────▶│                       │
      │                   │  4. Download & encode  │
      │                   │──────────────────────▶│
      │                   │                       │
      │                   │  5. Upload via MTProto │
      │                   │◀──────────────────────│
      │  6. Receive file  │                       │
      │◀──────────────────│                       │
```

1. **User** sends a YouTube URL to the Telegram bot
2. **Telegram** pushes the update to the bot server via HTTPS webhook
3. **Bot** asks for a language on the user's first message and stores it in MySQL
4. **Bot** reads metadata with `yt-dlp`, then sends thumbnail, description, duration, quality choices, and estimated sizes
5. **User** picks a quality (e.g. 720p video or MP3 audio)
6. **Bot** runs `yt-dlp` + `ffmpeg` on its own host to download and encode the media in a background job
7. **Bot** uploads the file to the user via MTProto (gotd/td), or falls back to Bot API for ≤ 50 MB
8. **User** receives the file directly in Telegram

---

## Quick Start

### 1. Clone

```sh
git clone https://github.com/Mezdia2/youtube-dl.git
cd youtube-dl
```

### 2. Configure environment

```sh
cp .env.example .env
```

Edit `.env` and fill in your values (see [Configuration](#configuration)).

### 3. Set up MTProto session

Before running the bot, you need to generate a `TG_SESSION` for the MTProto uploader. The program provides an interactive setup command:

```sh
go run . --setup-session
```

This will:
1. Ask for your phone number (or use `TG_PHONE` from `.env` if set)
2. Request a verification code from Telegram and ask you to enter it
3. If your account has 2FA (two-factor authentication), ask for your password; otherwise it skips this step
4. Authenticate and save the session
5. Output the `TG_SESSION` base64 value
6. Offer to automatically write `TG_SESSION` into your `.env` file

After the setup completes, the `TG_SESSION` value will be in your `.env`.

### 4. Install yt-dlp and ffmpeg

Downloads run on the bot's own host, so the machine that runs the bot needs:

- **yt-dlp** — optional to install manually. If it is not on `PATH` and `YT_DLP_PATH` is unset, the bot downloads the official binary into its cache and self-refreshes it.
- **ffmpeg** — **required** to merge separate video/audio streams and to extract MP3 audio. Install it from your package manager (e.g. `apt-get install -y ffmpeg`). If ffmpeg lives in a non-standard location, point `FFMPEG_LOCATION` at it.

### 5. Run the bot locally

The bot runs an HTTP server that receives Telegram updates via webhook. For local testing you need a publicly reachable URL (e.g. via [ngrok](https://ngrok.com) or a similar tunnel):

```sh
# Example: tunnel local port 8080 to a public HTTPS URL
ngrok http 8080

# Set WEBHOOK_DOMAIN to the ngrok URL and start the bot
WEBHOOK_DOMAIN=https://xxxx.ngrok-free.app go run .
```

The bot will call Telegram's `setWebhook` on startup, pointing to `WEBHOOK_DOMAIN` + `WEBHOOK_PATH`.

### 6. Deploy on Railway

The bot exposes an HTTP server with webhook and healthcheck endpoints. Railway must assign a **public domain** so Telegram can deliver updates to the webhook URL.

1. Create a Railway project from this GitHub repository.
2. Use the default Railpack builder. `railway.json` pins the deploy start command to `./out` and sets `healthcheckPath` to `/healthz`.
3. In Railway → Service → Variables, add the same values you use locally:

```env
BOT_TOKEN=
TG_APP_ID=
TG_APP_HASH=
TG_SESSION=
MYSQL_URL=
MYSQL_DSN=
WEBHOOK_DOMAIN=
WEBHOOK_PATH=/telegram/webhook
WEBHOOK_SECRET_TOKEN=
PORT=
```

4. In Railway → Service → Networking, **generate a public domain** (e.g. `your-service.up.railway.app`). Set `WEBHOOK_DOMAIN` to `https://your-service.up.railway.app`.
5. Make sure the runtime image has **ffmpeg** available (and enough disk for the largest file you allow). Set `FFMPEG_LOCATION` if ffmpeg is not on `PATH`.
6. Deploy the service. The bot will call `setWebhook` on startup and start receiving updates.

Downloads and uploads both run inside this service, so `BOT_TOKEN`, `TG_APP_ID`, `TG_APP_HASH`, and `TG_SESSION` only ever need to exist in the Railway environment.

---

## Configuration

All settings are loaded from environment variables (or `.env` file). The `.env.example` file contains all required variables:

```env
# Telegram bot token from @BotFather.
BOT_TOKEN=

# Telegram webhook public domain.
# Use your Railway public domain, for example:
# WEBHOOK_DOMAIN=https://your-service.up.railway.app
WEBHOOK_DOMAIN=
WEBHOOK_PATH=/telegram/webhook

# Optional. If empty, the app derives a stable secret from BOT_TOKEN.
WEBHOOK_SECRET_TOKEN=

# MTProto uploader secrets. The server uses these to upload downloaded files.
# Generate TG_SESSION once with: ./out --setup-session
TG_APP_ID=
TG_APP_HASH=
TG_SESSION=

# MySQL connection. The bot creates the user_languages table automatically.
# MYSQL_URL takes priority (recommended for Railway). Format: mysql://user:password@host:3306/database
# MYSQL_DSN is the fallback. Format: user:password@tcp(host:3306)/database?parseTime=true&charset=utf8mb4
MYSQL_URL=
MYSQL_DSN=user:password@tcp(host:3306)/database?parseTime=true&charset=utf8mb4

# Optional. Full path to yt-dlp. If empty, the bot uses PATH or downloads yt-dlp automatically.
# YT_DLP_PATH=

# Optional. Directory or path to ffmpeg, passed to yt-dlp as --ffmpeg-location.
# Leave empty to use ffmpeg from PATH. ffmpeg is required on the host.
# FFMPEG_LOCATION=
```

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `BOT_TOKEN` | **Yes** | — | Telegram bot token from @BotFather |
| `WEBHOOK_DOMAIN` | **Yes** | — | Public HTTPS domain where Telegram sends updates (e.g. `https://your-service.up.railway.app`) |
| `WEBHOOK_PATH` | No | `/telegram/webhook` | URL path for the webhook endpoint |
| `WEBHOOK_SECRET_TOKEN` | No | derived from `BOT_TOKEN` | Secret token for verifying webhook requests from Telegram; auto-generated from `BOT_TOKEN` SHA-256 if not set |
| `PORT` | No | `8080` | HTTP server listen port; Railway provides this automatically |
| `TG_APP_ID` | **Yes** | — | Telegram API ID from my.telegram.org |
| `TG_APP_HASH` | **Yes** | — | Telegram API Hash from my.telegram.org |
| `TG_SESSION` | **Yes** | — | Base64-encoded MTProto session (generate with `--setup-session`) |
| `TG_PHONE` | No | — | Phone number for `--setup-session` (optional; asked interactively if not set) |
| `MYSQL_URL` | **Yes** (one of) | — | MySQL URL format (priority; recommended for Railway). Example: `mysql://user:password@host:3306/database` |
| `MYSQL_DSN` | **Yes** (one of) | — | MySQL DSN format (fallback). Example: `user:password@tcp(host:3306)/database?parseTime=true&charset=utf8mb4` |
| `YT_DLP_PATH` | No | auto | Optional path to `yt-dlp`; if omitted, the bot checks PATH then downloads the official binary into its cache |
| `FFMPEG_LOCATION` | No | PATH | Optional directory/path to `ffmpeg`, passed to `yt-dlp` as `--ffmpeg-location`; ffmpeg must be installed on the host |
| `YT_COOKIES_B64` | No | — | Optional base64-encoded YouTube cookies file used as a **fallback** after the cookieless method; generate it with the bundled [`cookie-extension/`](cookie-extension/) |

---

## MTProto Session Setup (`--setup-session`)

The MTProto uploader needs an authenticated Telegram session. Use the built-in `--setup-session` command to generate the session interactively:

```sh
go run . --setup-session
```

**Steps:**

1. **Phone number** — Enter your phone number in international format (e.g. `+989123456789`). You can also set `TG_PHONE` in `.env` to skip this prompt.
2. **Verification code** — Telegram sends a code to your account. Enter it when prompted.
3. **2FA password** — If your account has two-factor authentication enabled, you'll be asked for your password. If you don't have 2FA, this step is skipped automatically.
4. **Session output** — After successful authentication, the program outputs the `TG_SESSION` base64 string.
5. **Auto-update `.env`** — The program asks whether to automatically write `TG_SESSION` to your `.env` file. Choose `y` to auto-update, or `n` to set it manually.

**Required before running `--setup-session`:**
- `TG_APP_ID` and `TG_APP_HASH` must be set in `.env` (get them from https://my.telegram.org)

**Manual session encoding (alternative):**

If you prefer to generate the session manually:

```sh
# Authenticate and save session to file
go run . --auth --session session.json

# Base64-encode the session
# Linux/macOS:
base64 -w0 session.json

# Windows PowerShell:
[Convert]::ToBase64String([IO.File]::ReadAllBytes("session.json"))
```

Copy the base64 string and set it as `TG_SESSION` in `.env`.

---

## YouTube Cookies (fallback)

The bot downloads **without cookies first**, using modern `yt-dlp` player
clients (`tv`, `mweb`, `web_safari`, `ios`). Cookies are only used as a fallback
when the cookieless attempt fails. YouTube rotates session cookies often, so when
the bot reports that cookies are invalid, export fresh ones:

1. Load the bundled [`cookie-extension/`](cookie-extension/) in Chrome
   (`chrome://extensions` → Developer mode → Load unpacked).
2. Sign in to YouTube, open the extension, click **Generate**, then **Copy**.
3. Set the value as `YT_COOKIES_B64` in `.env`. The bot reads it directly on the
   next request.

See [`cookie-extension/README.md`](cookie-extension/README.md) for details.

---

## Architecture

### Bot Server (`main.go`, `handlers.go`, `config.go`, `state.go`, `download.go`, `process.go`)

| File | Responsibility |
|------|----------------|
| `main.go` | Bot API types, webhook server, `setWebhook` on startup, CLI flags (`--setup-session`), healthcheck `/healthz` |
| `handlers.go` | `/start`, `/help`, `/cancel`, URL detection, quality selection, sticker/reaction UX |
| `stickers.go` | Sticker set fetching/caching, emoji matching, and sticker/reaction send helpers |
| `config.go` | Environment loading, `.env` parser, validation (all env vars: BOT_TOKEN, TG_*, WEBHOOK_*, PORT) |
| `database.go` | MySQL connection, migration, and user language persistence |
| `i18n.go` | Locale loading and translation helpers |
| `youtube.go` | `yt-dlp` metadata extraction, thumbnail info, quality sizes |
| `download.go` | Server-side `yt-dlp` media download with the cookieless/cookie fallback ladder |
| `process.go` | Background download→size-check→upload job and the Bot API fallback |
| `state.go` | In-memory pending URL and quality-selection stores with 5-min TTL |

### Webhook Flow

1. On startup, the bot calls Telegram `setWebhook` with `WEBHOOK_DOMAIN` + `WEBHOOK_PATH` and the secret token.
2. An HTTP server listens on `PORT` with two routes:
   - **`/healthz`** — returns `200 ok` for Railway healthchecks and monitoring.
   - **`WEBHOOK_PATH`** — receives POST requests from Telegram, verifies the `X-Telegram-Bot-Api-Secret-Token` header, decodes the update, and dispatches handlers asynchronously.
3. Each webhook update is processed in a background goroutine with a 2-minute timeout. A quality selection then spawns its own download job with a longer (30-minute) timeout so large downloads are not cut short.

### Server-side Download Job (`download.go`, `process.go`)

| Step | Action |
|------|--------|
| Download | `DownloadMedia` runs `yt-dlp` for the selected format/quality into a per-request temp dir, trying cookieless modern clients → cookieless default clients → cookie variants, with a one-shot `yt-dlp` self-refresh if all attempts fail |
| Size check | Files ≥ 2 GiB are rejected with a localized "pick a lower quality" message |
| Upload | `UploadFile` sends the file over MTProto with a private per-upload session |
| Fallback | For files ≤ 50 MB, `sendLocalMedia` retries over the Bot API (`sendVideo`/`sendAudio`) |
| Status & cleanup | The transient "downloading…" message is removed on completion; the temp dir is always cleaned up |

### MTProto Uploader (`uploader.go`)

| Feature | Detail |
|---------|--------|
| Session setup | `runSessionSetup()` — interactive phone+code auth, 2FA support, auto-write `.env` |
| Auth | `terminalAuth` struct implements gotd/td auth flow |
| Peer resolution | Username-based or dialog-based `InputPeer` lookup |
| Upload | `gotd/td` uploader with `FromPath` for large files |
| Send | `MessagesSendMedia` with proper MIME type & attributes |

---

## Security

- `.env` is excluded from git via `.gitignore`
- `BOT_TOKEN`, `TG_APP_ID`, `TG_APP_HASH`, and `TG_SESSION` stay in the server-side `.env` and never leave the host
- `TG_SESSION` is materialized only into a private (`0600`) per-upload temp file that is removed as soon as the upload finishes
- Downloaded media lives in a per-request temp directory that is always cleaned up, even on failure
- Webhook requests are verified via `X-Telegram-Bot-Api-Secret-Token` header; unmatched requests get `401`
- `WEBHOOK_SECRET_TOKEN` is auto-derived from `BOT_TOKEN` SHA-256 if not explicitly set
- `--setup-session` asks before writing to `.env` and uses file permission `0600`

---

## Building

```sh
go build -o youtube-dl-bot .
```

---

## Supported YouTube URLs

| Pattern | Example |
|---------|---------|
| Standard watch | `youtube.com/watch?v=...` |
| Short link | `youtu.be/...` |
| Shorts | `youtube.com/shorts/...` |
| Embed | `youtube.com/embed/...` |
| Legacy | `youtube.com/v/...` |
| Mobile | `m.youtube.com/watch?v=...` |
| Live | `youtube.com/live/...` |
| Music | `music.youtube.com/...` |

---

## File Size Limits

| Method | Max Size | Condition |
|--------|----------|-----------|
| Bot API | 50 MB | Automatic fallback when MTProto fails |
| MTProto (gotd/td) | ~2 GB | Primary upload method |
| Rejected | >2 GB | User notified to pick lower quality |

---

## License

This project is open source. See the [LICENSE](LICENSE) file for details.
