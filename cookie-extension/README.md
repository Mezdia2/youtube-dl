# YouTube Cookie Exporter

A small Chrome/Chromium (Manifest V3) extension that exports your
`youtube.com` cookies as a single base64 string, ready to paste into the bot's
`.env` file as `YT_COOKIES_B64`.

The bot uses these cookies only as a **fallback** — the cookieless download
method runs first. Export fresh cookies whenever the bot reports that the
stored cookies have been rotated or are no longer valid.

## What it produces

A base64-encoded [Netscape `cookies.txt`](https://github.com/yt-dlp/yt-dlp/wiki/FAQ#how-do-i-pass-cookies-to-yt-dlp)
file — exactly the format the bot decodes (`base64 -d` → `cookies.txt`) and
passes to `yt-dlp --cookies`.

## Install (Load unpacked)

1. Open `chrome://extensions` (or `edge://extensions`).
2. Turn on **Developer mode** (top-right).
3. Click **Load unpacked** and select this `cookie-extension/` folder.

## Use

1. Sign in to <https://www.youtube.com> in the same browser.
2. Click the extension icon, then **Generate**.
3. Click **Copy**.
4. Put the value in your `.env`:

   ```env
   YT_COOKIES_B64=<paste here>
   ```

5. Redeploy / restart the bot (and, for GitHub Actions downloads, update the
   `YT_COOKIES_B64` repository secret — the bot syncs it automatically on the
   next request).

## Verify (optional)

```bash
echo "$YT_COOKIES_B64" | base64 -d | head
# should start with: # Netscape HTTP Cookie File
```

## Notes

- Permissions are limited to the `cookies` API and `https://*.youtube.com/`.
- Everything runs locally in your browser; nothing is uploaded anywhere.
- Treat the exported value like a password — it grants access to your YouTube
  session.
