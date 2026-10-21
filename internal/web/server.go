// Package web serves the pairing / monitoring UI and the JSON API that backs
// it, plus a command console that drives the same agent as WhatsApp.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vishalgojha/sdsheetal/internal/assistant"
	"github.com/vishalgojha/sdsheetal/internal/config"
	"github.com/vishalgojha/sdsheetal/internal/gmail"
	"github.com/vishalgojha/sdsheetal/internal/spotify"
	"github.com/vishalgojha/sdsheetal/internal/store"
	"github.com/vishalgojha/sdsheetal/internal/whatsapp"
)

//go:embed static
var staticFiles embed.FS

type Server struct {
	cfg   *config.Config
	store *store.Store
	agent *assistant.Agent
	spot  *spotify.Client
	gmail *gmail.Client
	wa    *whatsapp.Client
	mux   *http.ServeMux
}

func New(cfg *config.Config, st *store.Store, agent *assistant.Agent, sp *spotify.Client, gm *gmail.Client, wa *whatsapp.Client) *Server {
	s := &Server{cfg: cfg, store: st, agent: agent, spot: sp, gmail: gm, wa: wa}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	mux := http.NewServeMux()
	s.mux = mux

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		if r.URL.Path == "/" {
			serveStatic(w, r, sub, "index.html", "text/html; charset=utf-8")
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/downloads/SheetalBridge.exe", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/vishalgojha/sd/raw/main/dist/AgentV.exe", http.StatusFound)
	})
	mux.HandleFunc("/downloads/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/downloads/" {
			http.NotFound(w, r)
			return
		}
		serveStatic(w, r, sub, "downloads.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("/downloads/AgentV.exe", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/vishalgojha/sd/raw/main/dist/AgentV.exe", http.StatusFound)
	})
	mux.HandleFunc("/downloads/sheetal-bridge-linux-amd64", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/vishalgojha/sd/raw/main/dist/agent-v-linux-amd64", http.StatusFound)
	})
	mux.HandleFunc("/downloads/agent-v-linux-amd64", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/vishalgojha/sd/raw/main/dist/agent-v-linux-amd64", http.StatusFound)
	})
	mux.HandleFunc("/downloads/Setup-SheetalBridge.ps1", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/vishalgojha/sd/raw/main/installer/windows/Setup-AgentV.ps1", http.StatusFound)
	})
	mux.HandleFunc("/downloads/Setup-AgentV.ps1", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/vishalgojha/sd/raw/main/installer/windows/Setup-AgentV.ps1", http.StatusFound)
	})
	mux.HandleFunc("/downloads/install-sheetal-bridge.sh", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/vishalgojha/sd/raw/main/installer/linux/install-agent-v.sh", http.StatusFound)
	})
	mux.HandleFunc("/downloads/install-agent-v.sh", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/vishalgojha/sd/raw/main/installer/linux/install-agent-v.sh", http.StatusFound)
	})
	mux.HandleFunc("/downloads/browser-worker.mjs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/vishalgojha/sd/raw/main/internal/web/static/browser-worker.mjs", http.StatusFound)
	})

	// WhatsApp pairing / status
	mux.HandleFunc("/api/whatsapp/status", s.status)
	mux.HandleFunc("/api/whatsapp/disconnect", s.whatsappDisconnect)
	mux.HandleFunc("/api/whatsapp/reconnect", s.whatsappReconnect)
	mux.HandleFunc("/api/whatsapp/qr.png", s.qrPNG)
	mux.HandleFunc("/api/command", s.command)

	// Dashboard + tools
	mux.HandleFunc("/api/dashboard", s.dashboard)
	mux.HandleFunc("/api/status", s.radioStatus)
	mux.HandleFunc("/api/search", s.search)
	mux.HandleFunc("/api/queue", s.queue)
	mux.HandleFunc("/api/queue/next", s.queueNext)
	mux.HandleFunc("/api/queue/remove", s.queueRemove)
	mux.HandleFunc("/api/queue/clear", s.queueClear)

	mux.HandleFunc("/api/email/status", s.emailStatus)
	mux.HandleFunc("/api/email/connect", s.emailConnect)
	mux.HandleFunc("/api/email/callback", s.emailCallback)
	mux.HandleFunc("/api/email/disconnect", s.emailDisconnect)
	mux.HandleFunc("/api/email/inbox", s.emailInbox)

	mux.HandleFunc("/api/music/state", s.musicState)
	mux.HandleFunc("/api/music/command", s.musicCommand)

	mux.HandleFunc("/api/memory/event", s.memoryEvent)
	mux.HandleFunc("/api/bridge/enqueue", s.bridgeEnqueue)
	mux.HandleFunc("/api/bridge/next", s.bridgeNext)
	mux.HandleFunc("/api/bridge/result", s.bridgeResult)
}

func (s *Server) authed(r *http.Request) bool {
	if s.cfg.AgentToken == "" {
		return true
	}

	// Support the documented Bearer and query-string forms as well as the
	// existing explicit headers. The query form is useful for a simple browser
	// panel; clients should prefer an Authorization header so the secret stays
	// out of URLs and logs.
	provided := r.Header.Get("X-SD-Agent-Token")
	if provided == "" {
		provided = r.Header.Get("X-SD-Token")
	}
	if provided == "" {
		const bearer = "Bearer "
		if authorization := r.Header.Get("Authorization"); strings.HasPrefix(authorization, bearer) {
			provided = strings.TrimSpace(strings.TrimPrefix(authorization, bearer))
		}
	}
	if provided == "" {
		provided = r.URL.Query().Get("token")
	}
	if len(provided) != len(s.cfg.AgentToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.AgentToken)) == 1
}

func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.authed(r) {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="sdsheetal"`)
	writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "valid agent token required"})
	return false
}

func (s *Server) bridgeEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.requireAuth(w, r) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		}
		return
	}
	var b struct {
		DeviceID   string         `json:"device_id"`
		Action     string         `json:"action"`
		Parameters map[string]any `json:"parameters"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	j, err := s.store.EnqueueBridge(b.DeviceID, b.Action, b.Parameters)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": j})
}

func (s *Server) bridgeNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.requireAuth(w, r) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		}
		return
	}
	device := r.URL.Query().Get("device_id")
	jobs := s.store.BridgeJobs()
	for i := range jobs {
		if jobs[i].Status == "queued" && (jobs[i].DeviceID == "" || jobs[i].DeviceID == device) {
			jobs[i].Status = "running"
			jobs[i].UpdatedAt = store.NowISO()
			_ = s.store.SaveBridgeJobs(jobs)
			writeJSON(w, http.StatusOK, map[string]any{"job": jobs[i]})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": nil})
}

func (s *Server) bridgeResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.requireAuth(w, r) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		}
		return
	}
	var b struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Result string `json:"result"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false})
		return
	}
	jobs := s.store.BridgeJobs()
	for i := range jobs {
		if jobs[i].ID == b.ID {
			jobs[i].Status = store.CleanText(b.Status, 20)
			if jobs[i].Status != "done" && jobs[i].Status != "failed" {
				jobs[i].Status = "done"
			}
			jobs[i].Result = store.CleanText(b.Result, 4000)
			jobs[i].UpdatedAt = store.NowISO()
			_ = s.store.SaveBridgeJobs(jobs)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "job not found"})
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func serveStatic(w http.ResponseWriter, r *http.Request, fsys fs.FS, name, ctype string) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// --- WhatsApp endpoints ------------------------------------------------

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	voiceProvider := ""
	if s.cfg.HasSarvam() {
		voiceProvider = "sarvam"
	} else if s.cfg.HasElevenLabs() {
		voiceProvider = "elevenlabs"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"logged_in":      s.wa.LoggedIn(),
		"connected":      s.wa.Connected(),
		"pairing":        s.wa.QRPairing(),
		"last_error":     s.wa.LastError(),
		"station":        s.cfg.Station,
		"voice_replies":  s.cfg.VoiceReplies,
		"voice_ready":    s.wa.VoiceReplyEnabled(),
		"voice_provider": voiceProvider,
		"stt_ready":      s.wa.STTEnabled(),
		"spotify":        s.spot.Enabled(),
		"gmail":          s.gmail.Enabled(),
	})
}

func (s *Server) whatsappDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		return
	}
	if !s.requireAuth(w, r) {
		return
	}
	s.wa.Disconnect()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) whatsappReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		return
	}
	if !s.requireAuth(w, r) {
		return
	}
	if err := s.wa.Reconnect(); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) qrPNG(w http.ResponseWriter, r *http.Request) {
	png, err := s.wa.QRPNG()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if png == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no QR available"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) command(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		return
	}
	if !s.requireAuth(w, r) {
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "message required"})
		return
	}
	var reply assistant.Reply
	if isCompletionQuestion(body.Message) {
		reply = s.computerStatusReply()
	} else if action, params, ok := computerAction(body.Message); ok {
		if _, err := s.store.EnqueueBridge("", action, params); err != nil {
			reply = assistant.Reply{Text: "I understood that, but I could not queue the computer action: " + err.Error(), Tool: "computer_queue_error"}
		} else {
			reply = assistant.Reply{Text: computerActionReply(action, params), Tool: "computer_action"}
		}
	} else {
		reply = s.agent.Run(body.Message)
	}
	s.store.RecordMemory("conversation", "", "assistant: "+reply.Text)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reply": reply.Text, "tool": reply.Tool})
}

func isCompletionQuestion(message string) bool {
	text := strings.ToLower(strings.TrimSpace(message))
	return text == "done?" || text == "did you do it" || text == "did it work" || text == "is it done" || text == "did you open it"
}

func (s *Server) computerStatusReply() assistant.Reply {
	jobs := s.store.BridgeJobs()
	for i := len(jobs) - 1; i >= 0; i-- {
		job := jobs[i]
		switch job.Status {
		case "done":
			return assistant.Reply{Text: "Yes — the computer confirmed: " + job.Result, Tool: "computer_status"}
		case "failed":
			return assistant.Reply{Text: "No. The computer could not complete it: " + job.Result, Tool: "computer_status"}
		case "queued", "running":
			return assistant.Reply{Text: "Not yet — the computer action is still " + job.Status + ".", Tool: "computer_status"}
		}
	}
	return assistant.Reply{Text: "No recent computer action is available to confirm.", Tool: "computer_status"}
}

func computerAction(message string) (string, map[string]any, bool) {
	text := strings.ToLower(strings.TrimSpace(message))
	if (strings.Contains(text, "mic") || strings.Contains(text, "microphone")) && (strings.Contains(text, "open") || strings.Contains(text, "find") || strings.Contains(text, "setting")) {
		return "playwright", map[string]any{"steps": []map[string]any{{"goto": "chrome://settings/content/microphone"}, {"wait": 800}}}, true
	}
	for _, app := range []string{"chrome", "spotify", "firefox"} {
		if strings.Contains(text, "open "+app) || strings.Contains(text, "launch "+app) || strings.Contains(text, "start "+app) {
			return "open_app", map[string]any{"name": app}, true
		}
	}
	return "", nil, false
}

func computerActionReply(action string, params map[string]any) string {
	if action == "playwright" {
		return "I’m opening the microphone settings with browser automation now."
	}
	return "Opening " + fmt.Sprint(params["name"]) + " on your computer now."
}

// --- dashboard & radio -------------------------------------------------

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.agent.Dashboard())
}

func (s *Server) radioStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"station":   s.cfg.Station,
		"on_air":    false,
		"listeners": 0,
		"queue_len": len(s.store.Queue()),
	})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	tracks, err := s.spot.SearchTracks(q, 6)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"results": []any{}, "error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(tracks))
	for _, t := range tracks {
		out = append(out, t.ToJSON())
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"queue": s.store.Queue()})
	case http.MethodPost:
		if !s.requireAuth(w, r) {
			return
		}
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply := s.agent.Run("play " + body.Query)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reply": reply.Text})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) queueNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		return
	}
	if !s.requireAuth(w, r) {
		return
	}
	q := s.store.Queue()
	var next *store.QueueItem
	for i := range q {
		if q[i].Status == "queued" {
			q[i].Status = "claimed"
			next = &q[i]
			break
		}
	}
	_ = s.store.SaveQueue(q)
	if next == nil {
		writeJSON(w, http.StatusOK, map[string]any{"item": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": next})
}

func (s *Server) queueRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		return
	}
	if !s.requireAuth(w, r) {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	q := s.store.Queue()
	removed := false
	for i := range q {
		if q[i].ID == body.ID && q[i].Status != "done" {
			q[i].Status = "done"
			removed = true
			break
		}
	}
	_ = s.store.SaveQueue(q)
	writeJSON(w, http.StatusOK, map[string]any{"ok": removed})
}

func (s *Server) queueClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		return
	}
	if !s.requireAuth(w, r) {
		return
	}
	if err := s.store.SaveQueue([]store.QueueItem{}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not clear queue"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- email --------------------------------------------------------------

func (s *Server) emailStatus(w http.ResponseWriter, r *http.Request) {
	if !s.gmail.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "connected": false, "setup_required": true})
		return
	}
	conn := s.gmail.Connection()
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":     true,
		"connected":      conn != nil && conn.ConnectionID != "",
		"setup_required": false,
		"account_email":  s.gmail.ProfileEmail(),
	})
}

func (s *Server) emailDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		return
	}
	if !s.requireAuth(w, r) {
		return
	}
	if err := s.gmail.Disconnect(); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) emailConnect(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	if !s.gmail.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "Gmail integration is not configured"})
		return
	}
	link, err := s.gmail.ConnectLink()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	http.Redirect(w, r, link, http.StatusFound)
}

func (s *Server) emailCallback(w http.ResponseWriter, r *http.Request) {
	if errText := r.URL.Query().Get("error"); errText != "" {
		http.Error(w, "Google connection cancelled: "+errText, http.StatusBadRequest)
		return
	}
	if err := s.gmail.Callback(r.URL.Query().Get("code"), r.URL.Query().Get("state")); err != nil {
		http.Error(w, "Google connection failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/#/email?connected=1", http.StatusFound)
}

func (s *Server) emailInbox(w http.ResponseWriter, r *http.Request) {
	if !s.gmail.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "Gmail integration is not configured"})
		return
	}
	query := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	messages, err := s.gmail.Messages(query, limit)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": messages})
}

// --- music state/command file plumbing -------------------------------

func (s *Server) musicState(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data := map[string]any{}
		_ = s.store.ReadJSONFile("music-state.json", &data)
		writeJSON(w, http.StatusOK, data)
	case http.MethodPost:
		if !s.requireAuth(w, r) {
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		s.store.WriteJSONFile("music-state.json", body)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) musicCommand(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data := map[string]any{}
		_ = s.store.ReadJSONFile("music-command.json", &data)
		writeJSON(w, http.StatusOK, map[string]any{"command": data})
	case http.MethodPost:
		if !s.requireAuth(w, r) {
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = store.NewID("music")
		body["created_at"] = time.Now().UTC().Format(time.RFC3339)
		s.store.WriteJSONFile("music-command.json", body)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "command_id": body["id"]})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) memoryEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		return
	}
	if !s.requireAuth(w, r) {
		return
	}
	var body struct {
		Type  string `json:"type"`
		Track string `json:"track"`
		Text  string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch body.Type {
	case "play", "skip", "replay", "conversation", "mood", "preference", "taste":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported memory event"})
		return
	}
	s.store.RecordMemory(body.Type, body.Track, body.Text)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
