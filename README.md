# sdsheetal (Go rewrite)

WhatsApp personal assistant built on [whatsmeow](https://github.com/tulir/whatsmeow).
Run Sheetal as a WhatsApp contact: tasks, notes, shopping, plans, music queue,
Spotify, Gmail via standard Google OAuth, and optional ElevenLabs voice-note replies. A small
web panel handles QR pairing, a command console, and a live dashboard.

## Quick start (local)

```bash
export SD_OWNER_NUMBER="+919876543210"   # only this number gets replies
export SDSHEETAL_DATA="$(pwd)/data"
go run .
```

Open http://localhost:8080, scan the WhatsApp QR with your phone, and message
the assistant. Session state is stored in `$SDSHEETAL_DATA/whatsmeow.db`.

## Configuration

Every setting comes from environment variables — see `.env.example`. Key ones:

| Variable | Purpose |
| --- | --- |
| `SD_OWNER_NUMBER` | One or more E.164 numbers only WhatsApp replies to (comma/semicolon separated; required) |
| `WHATSNEW_STORE` | path to the sqlite session db (default `$SDSHEETAL_DATA/whatsmeow.db`) |
| `SD_VOICE_REPLIES` | `true` to also send ElevenLabs voice notes |
| `SD_VOICE_NOTE_REPLIES` | `true` to send replies only as voice notes |
| `ELEVENLABS_API_KEY` | enables TTS voice replies (used when Sarvam is not configured) |
| `SARVAM_API_KEY` | Sarvam AI key — enables voice-note transcription (SPT/STT) and Indic TTS voice replies when set |
| `SARVAM_TTS_SPEAKER` / `SARVAM_TTS_LANG` / `SARVAM_TTS_MODEL` | TTS speaker (`shubh`), language (`hi-IN`), model (`bulbul:v3`) |
| `SARVAM_STT_MODEL` / `SARVAM_STT_MODE` / `SARVAM_STT_LANG` | transcription model (`saaras:v3`), mode (`transcribe`, `translate`, `verbatim`, `translit`, `codemix`), language (`unknown` = auto-detect) |
| `SARVAM_CHAT_MODEL` | reasoning/chat model (`sarvam-105b-conversations` by default) |
| `SPOTIFY_CLIENT_ID` / `SPOTIFY_CLIENT_SECRET` | enables play-queue + search from WhatsApp |
| `NANGO_SECRET_KEY` | enables Gmail read (`search email`, `check inbox`) |
| `SDSHEETAL_AGENT_TOKEN` | shared secret required by write endpoints on the web panel/API |

## What the assistant understands

Small keywords-based intent parser (English + Hindi). Examples:

- tasks: `remind me to call mom`, `add task buy tickets`, `my tasks`
- shopping: `add milk to shopping`, `shopping list`
- notes: `note down the wifi password`
- plans: `plan my day`, `what's my plan today`
- music queue + Spotify: `play dua lipa`, `queue`, `next`, `current song`
  (search/music only works when Spotify creds are set; queued audio streams
  from the JioSaavn-style `uplink` if `SD_*` music endpoints are configured)
- email: `check inbox`, `any new email`, `search email for invoice`
- Gmail cleanup: `scan Gmail promotions`, then explicitly confirm with `confirm Gmail cleanup` to move the previewed messages to Trash; permanent deletion is not exposed
- personal payments: `add EMI HDFC ₹42000 due 5th`, `payments due this week`, `mark rent paid`, `recurring payments`
- voice: send a voice note and Sheetal transcribes it with Sarvam STT, then
  answers; replies can be spoken with Sarvam TTS (Indic) or ElevenLabs
- text `help` returns the full tool help

## Native ClawHub skill ports

ClawHub skills are reviewed as behavior specifications and ported into the Go
runtime. The self-improvement skill stores bounded records in
`$SDSHEETAL_DATA/learnings.json` and injects recent pending guidance into AI
replies. The Gmail cleaner port uses the Gmail API directly after a standard Google OAuth connection,
supports bounded previews and confirmation-gated trash/restore operations, and
never permanently deletes mail.

## API

- `GET /healthz` – liveness
- `GET /api/whatsapp/status` – `{logged_in, pairing, connected, phone, last_error}`
- `GET /api/whatsapp/qr.png` – pairing QR while pairing
- `POST /api/command` – `{token?, message}` runs the assistant, returns `{reply, tool}`
- `GET /api/dashboard` – tasks / shopping / notes / plans snapshot
- `GET /api/search?q=…` – searches notes and tasks
- `GET /api/queue`, `POST /api/queue/add`, `POST /api/queue/next`, `GET /api/music/search?q=…`
- `GET /api/email/status`, `GET /api/email/connect`, `GET /api/email/inbox`
- `POST /api/memory/event` – record a memory event

The console is served at `/`. When `SDSHEETAL_AGENT_TOKEN` is set, all
state-changing API requests require it through `Authorization: Bearer <token>`
(recommended), `X-SD-Agent-Token`, `X-SD-Token`, or `?token=<token>`. This
includes `/api/command`, queue changes, music state/commands, memory events,
and the Gmail connection flow.

## Docker

```bash
docker compose up -d --build
```

The container keeps all state (WhatsApp session, JSON data files) under `/data`
— mount a volume there. Healthcheck hits `/healthz`.

### Deploying to Coolify

1. Add the repo as a new application, keep GitHub-style buildpack but override
   the **Build Command** with `docker buildx build . -t app:latest`
   (or set "Dockerfile" as the build method).
2. Set Environment Variables from `.env.example`.
3. Add a **Persistent Storage** volume mounted at `/data` so the WhatsApp
   session survives restarts.
4. Set the healthcheck/path to `/healthz` if Coolify asks.

## Design notes

- `whatsmeow` persists the session in sqlite via
  `go.mau.fi/whatsmeow/store/sqlstore`, so you pair once and stay logged in.
- QR pairing is exposed only through the web panel; the bot never auto-answers
  unknown numbers.
- Data formats in `internal/store` mirror the original Python project's JSON
  files, so an existing Coolify `/data` volume can be reused as-is.
