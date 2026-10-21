package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime configuration loaded from environment variables.
// Mirror the env names used by the Python sdsheetal deployment so the same
// Coolify variables keep working, plus the new whatsmeow-specific ones.
type Config struct {
	// Server
	Port    string
	DataDir string

	// WhatsApp
	WhatsAppStore string // sqlite file for whatsmeow session state
	Owner         string // phone number in E.164, e.g. +919876543210; only owner gets replies

	// Assistant
	VoiceReplies    bool   // send ElevenLabs voice notes in addition to text
	ReplyVoiceNotes bool   // when true, replies are delivered as voice notes
	Station         string // assistant name used in replies
	TimeZone        string // IANA timezone for "IST" style scheduling

	// Spotify
	SpotifyClientID     string
	SpotifyClientSecret string
	SpotifyPlaylistID   string
	SpotifyTimeoutS     int

	// ElevenLabs
	ElevenLabsAPIKey  string
	ElevenLabsVoice   string
	ElevenLabsModel   string
	ElevenLabsAgentID string

	// Sarvam AI (Indic language TTS/STT)
	SarvamAPIKey     string
	SarvamTTSSpeaker string
	SarvamTTSLang    string
	SarvamTTSModel   string
	SarvamSTTModel   string
	SarvamSTTMode    string
	SarvamSTTLang    string
	SarvamChatModel  string

	// Gmail (standard Google OAuth)
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string

	// Auth
	AgentToken string // shared token for the manual web tool API

	// Media welcome message
	WelcomeMessage string
}

func env(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(name string, def bool) bool {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return def
		}
		return b
	}
	return def
}

// Load reads configuration from the environment.
func Load() *Config {
	dataDir := env("SDSHEETAL_DATA", "/data")
	ownerNumbers := env("SD_OWNER_NUMBER", "")
	// Coolify makes it convenient to add a second variable rather than edit an
	// existing secret. Fold SD_OWNER_NUMBER_1, _2, ... into the same allowlist.
	for i := 1; i <= 10; i++ {
		if v := strings.TrimSpace(os.Getenv("SD_OWNER_NUMBER_" + strconv.Itoa(i))); v != "" {
			if ownerNumbers != "" {
				ownerNumbers += ","
			}
			ownerNumbers += v
		}
	}
	return &Config{
		Port:    env("PORT", "8080"),
		DataDir: dataDir,

		WhatsAppStore: env("WHATSNEW_STORE", dataDir+"/whatsmeow.db"),
		Owner:         ownerNumbers,

		VoiceReplies:    envBool("SD_VOICE_REPLIES", false),
		ReplyVoiceNotes: envBool("SD_VOICE_NOTE_REPLIES", false),
		Station:         env("SDSHEETAL_STATION", "Sheetal"),
		TimeZone:        env("SDSHEETAL_TZ", "Asia/Kolkata"),

		SpotifyClientID:     env("SPOTIFY_CLIENT_ID", ""),
		SpotifyClientSecret: env("SPOTIFY_CLIENT_SECRET", ""),
		SpotifyPlaylistID:   env("SPOTIFY_PLAYLIST_ID", "2JXK0KRt8pLkmUqIPPmmQQ"),
		SpotifyTimeoutS:     envInt("SDSHEETAL_SPOTIFY_TIMEOUT", 6),

		ElevenLabsAPIKey:  env("ELEVENLABS_API_KEY", ""),
		ElevenLabsVoice:   env("ELEVENLABS_VOICE_ID", "7qBNUtXRGP0jPi0H4r8k"),
		ElevenLabsModel:   env("ELEVENLABS_MODEL_ID", "eleven_multilingual_v2"),
		ElevenLabsAgentID: env("ELEVENLABS_AGENT_ID", ""),

		SarvamAPIKey:     env("SARVAM_API_KEY", ""),
		SarvamTTSSpeaker: env("SARVAM_TTS_SPEAKER", "shubh"),
		SarvamTTSLang:    env("SARVAM_TTS_LANG", "hi-IN"),
		SarvamTTSModel:   env("SARVAM_TTS_MODEL", "bulbul:v3"),
		SarvamSTTModel:   env("SARVAM_STT_MODEL", "saaras:v3"),
		SarvamSTTMode:    env("SARVAM_STT_MODE", "transcribe"),
		SarvamSTTLang:    env("SARVAM_STT_LANG", "unknown"),
		SarvamChatModel:  env("SARVAM_CHAT_MODEL", "sarvam-105b-conversations"),

		GoogleClientID:     env("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: env("GOOGLE_CLIENT_SECRET", ""),
		GoogleRedirectURL:  env("GOOGLE_REDIRECT_URL", "https://sd.vishalojha.me/api/email/callback"),

		AgentToken: env("SDSHEETAL_AGENT_TOKEN", ""),

		WelcomeMessage: env("SD_WELCOME", ""),
	}
}

// HasSpotify reports whether client credentials were configured.
func (c *Config) HasSpotify() bool {
	return c.SpotifyClientID != "" && c.SpotifyClientSecret != ""
}

// HasElevenLabs reports whether TTS is configured.
func (c *Config) HasElevenLabs() bool {
	return c.ElevenLabsAPIKey != "" && c.ElevenLabsVoice != ""
}

// HasSarvam reports whether the Sarvam AI API key is configured.
func (c *Config) HasSarvam() bool {
	return c.SarvamAPIKey != ""
}

// HasGoogleOAuth reports whether standard Gmail OAuth is configured.
func (c *Config) HasGoogleOAuth() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != "" && c.GoogleRedirectURL != ""
}
