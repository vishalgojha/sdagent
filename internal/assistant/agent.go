// Package assistant is the rule-based personal assistant brain. It parses
// free-form messages (English and conversational Hindi) into tool calls using
// the same toolset as the original Python app: tasks, notes, shopping, day
// plans, Spotify music, preferences, and read-only Gmail.
package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vishalgojha/sdsheetal/internal/config"
	"github.com/vishalgojha/sdsheetal/internal/eleven"
	"github.com/vishalgojha/sdsheetal/internal/gmail"
	"github.com/vishalgojha/sdsheetal/internal/sarvam"
	"github.com/vishalgojha/sdsheetal/internal/spotify"
	"github.com/vishalgojha/sdsheetal/internal/store"
)

// Reply is the result of handling a single user message.
type Reply struct {
	Text string `json:"text"` // what to say back (safe, no markdown)
	Tool string `json:"tool"` // which tool fired
}

// Agent ties the assistant tools together.
type Agent struct {
	cfg               *config.Config
	store             *store.Store
	spotify           *spotify.Client
	gmail             *gmail.Client
	ai                *sarvam.Client
	eleven            *eleven.Client
	location          *time.Location
	cleanupMu         sync.Mutex
	pendingGmailQuery string
	pendingGmailCount int
}

func New(cfg *config.Config, st *store.Store, sp *spotify.Client, gm *gmail.Client, ai *sarvam.Client, el *eleven.Client) *Agent {
	loc, err := time.LoadLocation(cfg.TimeZone)
	if err != nil {
		loc = time.FixedZone("IST", 5*3600+30*60)
	}
	return &Agent{cfg: cfg, store: st, spotify: sp, gmail: gm, ai: ai, eleven: el, location: loc}
}

func (a *Agent) now() time.Time { return time.Now().In(a.location) }

// Dashboard aggregates the current assistant state for the web panel.
func (a *Agent) Dashboard() map[string]any {
	tasks := []store.Task{}
	for _, t := range a.store.Tasks() {
		if t.Status != "done" {
			tasks = append(tasks, t)
		}
		if len(tasks) >= 50 {
			break
		}
	}
	notes := a.store.Notes()
	if len(notes) > 8 {
		notes = notes[:8]
	}
	shopping := []store.ShoppingItem{}
	for _, x := range a.store.Shopping() {
		if x.Status != "done" {
			shopping = append(shopping, x)
		}
		if len(shopping) >= 50 {
			break
		}
	}
	plans := a.store.Plans()
	if len(plans) > 8 {
		plans = plans[:8]
	}
	m := a.store.Memory()
	return map[string]any{
		"now_ist":   a.now().Format(time.RFC3339),
		"tasks":     taskedJSON(tasks),
		"notes":     notes,
		"shopping":  shopping,
		"plans":     plans,
		"bills":     a.store.Bills(),
		"learnings": a.store.Learnings(),
		"memory": map[string]any{
			"favorite_tracks": store.MemoryTop(m.Plays, 6),
			"skipped_tracks":  store.MemoryTop(m.Skips, 6),
			"replayed_tracks": store.MemoryTop(m.Replays, 6),
			"preferences":     memoryTexts(m.Preferences, 8),
			"taste_notes":     memoryTexts(m.TasteNotes, 8),
			"recent_moods":    memoryTexts(m.Moods, 6),
		},
	}
}

func taskedJSON(tasks []store.Task) []map[string]any {
	out := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, map[string]any{
			"id": t.ID, "title": t.Title, "due": t.Due, "priority": t.Priority,
			"notes": t.Notes, "status": t.Status, "created_at": t.CreatedAt, "updated_at": t.UpdatedAt,
		})
	}
	return out
}

func memoryTexts(entries []store.MemoryEntry, n int) []string {
	out := make([]string, 0, n)
	for i := len(entries) - 1; i >= 0 && len(out) < n; i-- {
		t := strings.TrimSpace(entries[i].Text)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Help returns the assistant's capability description.
func (a *Agent) Help() Reply {
	lines := []string{
		"Hi — I'm your WhatsApp assistant. You can text me things like:",
		"• Tasks: \"remind me about an appointment\", \"what's pending?\" (I save these in your task list.)",
		"• Notes: \"note: pharmacy closes at 9\", \"my notes\"",
		"• Shopping: \"add milk and eggs\", \"shopping list\"",
		"• Plans: \"plan my day: walk, groceries, chores\"",
		"• Music: \"play Arijit Singh\", \"now playing?\"",
		"• Email: \"check unread email\"",
		"• Gmail cleanup: \"scan Gmail promotions\"; reply \"confirm Gmail cleanup\" to move the previewed messages to Trash (never permanently deleted).",
		"• Payments: \"add EMI HDFC ₹42000 due 5th\", \"payments due this week\", \"mark rent paid\"",
		"• Preferences: \"remember: I like chai over coffee\"",
		"• Self-improvement: I remember explicit corrections and feature requests to improve future replies.",
	}
	if a.cfg.VoiceReplies {
		lines[0] += " I can reply with voice notes too."
	}
	return Reply{Text: strings.Join(lines, "\n"), Tool: "help"}
}

// Run dispatches a user message and returns the assistant's reply.
func (a *Agent) Run(message string) Reply {
	raw := strings.TrimSpace(message)
	if raw == "" {
		return Reply{Text: "I didn't catch that — try \"help\" to see what I can do.", Tool: "unknown"}
	}
	text := strings.ToLower(raw)
	if matchesAny(text, []string{"confirm gmail cleanup", "confirm email cleanup", "confirm cleanup"}) {
		return a.confirmGmailCleanup()
	}

	// Wake-word strip for the agent name.
	for _, wake := range []string{"sheetal ji", "sheetal", "शीतल जी", "शीतल", a.cfg.Station} {
		w := strings.ToLower(wake)
		if strings.HasPrefix(text, w) {
			rest := strings.TrimLeft(raw[len(wake):], " ,:;—-")
			raw = rest
			text = strings.ToLower(rest)
			break
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Reply{Text: "Yes? How can I help?", Tool: "wake"}
	}

	a.store.RecordMemory("conversation", "", raw)
	a.captureLearningSignal(raw, text)

	if matchesAny(text, []string{"help", "commands", "what can you", "how do you work", "show commands", "menu", "मदद"}) {
		return a.Help()
	}

	if matchesAny(text, []string{"time", "समय", "कितने बजे", "बज रहे", "date", "कौन सा दिन"}) {
		return a.timeReply()
	}

	if matchesAny(text, []string{"hi", "hello", "hey", "namaste", "नमस्ते", "good morning", "good evening", "good afternoon", "gm", "hii", "hello sheetal"}) {
		return a.greeting(raw)
	}
	if matchesAny(text, []string{"how are you", "how r you", "how are u", "how is it going", "what's up", "whats up", "kaisi ho", "kaise ho"}) {
		return Reply{Text: "I’m doing well and ready to help. Aaj kis cheez ka dhyan rakhein — bills, reminders, planning, ya kuch aur?", Tool: "conversation"}
	}
	if matchesAny(text, []string{"thank you", "thanks", "thank u", "shukriya", "धन्यवाद"}) {
		return Reply{Text: "You’re welcome. Jab bhi zaroorat ho, message kar dena.", Tool: "conversation"}
	}
	if matchesAny(text, []string{"good night", "gn", "bye", "goodbye", "see you"}) {
		return Reply{Text: "Good night, Sheetal. Main yahin hoon — jab bhi zaroorat ho.", Tool: "conversation"}
	}
	if reply, ok := a.handleBills(raw, text); ok {
		return reply
	}

	if reply, ok := a.handleTasks(raw, text); ok {
		return reply
	}
	if reply, ok := a.handleNotes(raw, text); ok {
		return reply
	}
	if reply, ok := a.handleShopping(raw, text); ok {
		return reply
	}
	if reply, ok := a.handlePlans(raw, text); ok {
		return reply
	}
	if isBrowserIntent(text) {
		return Reply{Text: "I can’t perform that computer action yet because the Agent V bridge is not connected.", Tool: "browser_executor_offline"}
	}
	if reply, ok := a.handleMusic(raw, text); ok {
		return reply
	}
	if reply, ok := a.handleEmail(raw, text); ok {
		return reply
	}
	if reply, ok := a.handlePreferences(raw, text); ok {
		return reply
	}

	if aiText := a.aiReply(raw); aiText != "" {
		return Reply{Text: aiText, Tool: "ai_chat"}
	}
	return Reply{
		Text: "I’m not fully sure what you want yet. Tell me in your own words — Hindi, English, or Hinglish is fine — and I’ll ask a quick follow-up if I need more detail.",
		Tool: "fallback",
	}
}

// handleBills provides a lightweight personal payment ledger. It records
// reminders and payment status only; it never attempts to move money.
func (a *Agent) handleBills(raw, text string) (Reply, bool) {
	if matchesAny(text, []string{"mark ", "paid", "payment done", "bill paid", "emi paid"}) && (strings.Contains(text, "paid") || strings.Contains(text, "payment done")) {
		name := strings.TrimSpace(raw)
		for _, prefix := range []string{"mark ", "marked ", "bill paid ", "emi paid ", "payment done "} {
			name = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(name), prefix))
			if name != strings.ToLower(strings.TrimSpace(raw)) {
				break
			}
		}
		name = strings.TrimSpace(strings.TrimSuffix(name, " as paid"))
		if b := a.store.MarkBillPaid("", name); b != nil {
			return Reply{Text: fmt.Sprintf("Marked %s as paid.", b.Name), Tool: "mark_bill_paid"}, true
		}
		return Reply{Text: "I couldn't find that bill or EMI. Try 'show my payments'.", Tool: "mark_bill_paid"}, true
	}

	if matchesAny(text, []string{"add bill", "add emi", "add an emi", "track bill", "track emi", "add subscription", "add insurance", "add sip", "remember bill"}) {
		kind := "bill"
		if strings.Contains(text, "emi") {
			kind = "emi"
		} else if strings.Contains(text, "subscription") {
			kind = "subscription"
		} else if strings.Contains(text, "insurance") {
			kind = "insurance"
		} else if strings.Contains(text, "sip") {
			kind = "sip"
		}
		name := extractAfter(raw, []string{"add an emi", "add emi", "add bill", "track bill", "track emi", "add subscription", "add insurance", "add sip", "remember bill"}, text)
		amount := ""
		if m := regexp.MustCompile(`(?i)(?:₹|rs\.?|inr)\s*([\d,]+(?:\.\d{1,2})?)`).FindStringSubmatch(name); len(m) > 1 {
			amount = strings.ReplaceAll(m[1], ",", "")
		}
		name = regexp.MustCompile(`(?i)\s*(?:₹|rs\.?|inr)\s*[\d,]+(?:\.\d{1,2})?`).ReplaceAllString(name, "")
		due := parseBillDueDate(name, a.location)
		name = regexp.MustCompile(`(?i)\s+(?:due|on|by)\s+\d{1,2}(?:st|nd|rd|th)?`).ReplaceAllString(name, "")
		name = strings.TrimSpace(strings.Trim(name, "-:,"))
		if name == "" {
			return Reply{Text: "Tell me the bill or EMI name, for example: add EMI HDFC ₹42000 due 5th.", Tool: "add_bill"}, true
		}
		if due == "" {
			return Reply{Text: "What day is it due? For example: add EMI HDFC ₹42000 due 5th.", Tool: "add_bill"}, true
		}
		b, err := a.store.CreateBill(name, kind, amount, due, "monthly", "", "", false)
		if err != nil {
			return Reply{Text: "I couldn't save that payment reminder.", Tool: "add_bill"}, true
		}
		amountText := ""
		if b.Amount != "" {
			amountText = " for ₹" + b.Amount
		}
		return Reply{Text: fmt.Sprintf("Saved %s%s, due on %s every month. I won't make the payment automatically.", b.Name, amountText, b.DueDate), Tool: "add_bill"}, true
	}

	if matchesAny(text, []string{"payments due", "upcoming bills", "my bills", "my emis", "my emi", "recurring payments", "payment summary", "financial commitments"}) {
		bills := a.store.Bills()
		if len(bills) == 0 {
			return Reply{Text: "You don't have any bills or EMIs saved yet.", Tool: "list_bills"}, true
		}
		lines := []string{"Your saved payments:"}
		for i := len(bills) - 1; i >= 0 && len(lines) < 9; i-- {
			b := bills[i]
			line := fmt.Sprintf("• %s — due %s", b.Name, b.DueDate)
			if b.Amount != "" {
				line += " — ₹" + b.Amount
			}
			if b.Status == "paid" {
				line += " (paid)"
			}
			lines = append(lines, line)
		}
		return Reply{Text: strings.Join(lines, "\n"), Tool: "list_bills"}, true
	}
	return Reply{}, false
}

func parseBillDueDate(text string, loc *time.Location) string {
	m := regexp.MustCompile(`(?i)\b(?:due|on|by)\s+(\d{1,2})(?:st|nd|rd|th)?\b`).FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	day, err := strconv.Atoi(m[1])
	if err != nil || day < 1 || day > 31 {
		return ""
	}
	now := time.Now().In(loc)
	due := time.Date(now.Year(), now.Month(), day, 0, 0, 0, 0, loc)
	if due.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)) {
		due = due.AddDate(0, 1, 0)
	}
	return due.Format("2006-01-02")
}

func isBrowserIntent(text string) bool {
	return (strings.Contains(text, "youtube") || strings.Contains(text, "chrome") || strings.Contains(text, "browser") || strings.Contains(text, "spotify web")) && (strings.Contains(text, "play") || strings.Contains(text, "open") || strings.Contains(text, "search") || strings.Contains(text, "check"))
}

func matchesAny(text string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(text, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

func (a *Agent) timeReply() Reply {
	now := a.now()
	format := now.Format("2 January 2006, 3:04 PM")
	return Reply{Text: fmt.Sprintf("It's %s now.", format), Tool: "get_time"}
}

func (a *Agent) greeting(raw string) Reply {
	if text := a.aiReply(raw); text != "" {
		return Reply{Text: text, Tool: "ai_chat"}
	}
	name := strings.TrimSpace(raw)
	name = strings.TrimLeft(name, "hH ")
	hour := a.now().Hour()
	period := "good evening"
	if hour < 12 {
		period = "good morning"
	} else if hour < 17 {
		period = "good afternoon"
	}
	return Reply{
		Text: fmt.Sprintf("%s, Sheetal — I’m here. What would you like to do?", strings.Title(period)),
		Tool: "greet",
	}
}

func indianSystemPrompt(_ string) string {
	return "You are Agent V, Sheetal’s personal assistant. The user’s name is Sheetal; address her by name naturally when it helps, but do not overuse it. Agent V is the product identity: never call the product Sheetal and never pretend to be Sheetal. STRICT SCRIPT RULE: output only Latin/English characters; Hindi must be written in Roman Hinglish. Never output Devanagari, Bengali, Tamil, or any non-Latin script, including quoted user text. Reply naturally in English, Hindi, or Hinglish, using IST and INR when relevant. You are an autonomous agent, not a fixed command parser: understand intent, preserve continuity from the conversation context, ask one short clarification only when needed, and use configured tools for real actions. Use assistant_action for tasks, notes, shopping, plans, preferences, Gmail, and Spotify. Use open_external_app or device/browser tools for Chrome, Spotify Web, YouTube Music, and microphone checks. Never claim an action happened unless its tool confirms success. Never send a message to another person or group without explicit confirmation immediately before sending; accept only a clear follow-up approval such as ‘confirm’, ‘yes’, or ‘do it’. Owner-requested reminders are sent only to Sheetal’s paired account after she asks for them. Keep WhatsApp replies concise and human. Do not emit canned menus or deterministic fallback scripts; respond to the actual request."
}

func (a *Agent) aiReply(message string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prompt := indianSystemPrompt(a.cfg.Station)
	prompt += fmt.Sprintf("\nCurrent local time in IST: %s. Use this for time-aware greetings and relative dates.", a.now().Format("Monday, 2 January 2006, 3:04 PM"))
	if learnings := a.store.ActiveLearningContext(8); len(learnings) > 0 {
		var lines []string
		for _, learning := range learnings {
			line := fmt.Sprintf("- [%s] %s", learning.Kind, learning.Summary)
			if learning.SuggestedAction != "" {
				line += " | Suggested action: " + learning.SuggestedAction
			}
			lines = append(lines, line)
		}
		prompt += "\nRelevant self-improvement records (treat as provisional guidance; do not expose internal details):\n" + strings.Join(lines, "\n")
	}
	// Each hosted turn is a fresh session, so provide a short durable window
	// from the on-disk conversation log for continuity across WhatsApp messages.
	if m := a.store.Memory(); len(m.Conversations) > 1 {
		start := len(m.Conversations) - 12
		if start < 0 {
			start = 0
		}
		var recent []string
		for _, entry := range m.Conversations[start:] {
			if strings.TrimSpace(entry.Text) != "" {
				recent = append(recent, entry.Text)
			}
		}
		if len(recent) > 0 {
			contextText := strings.Join(recent, "\n")
			// Keep hosted-agent prompts below websocket/provider limits while
			// retaining the most recent continuity window.
			if len(contextText) > 6000 {
				contextText = contextText[len(contextText)-6000:]
			}
			prompt += "\nRecent conversation (use only as context):\n" + contextText
		}
		if len(m.Preferences) == 0 && len(m.Conversations) <= 2 {
			prompt += "\nThis is Sheetal’s first interaction. Give a warm time-aware greeting, then ask at most three concise onboarding questions: preferred reminder channel (WhatsApp, email, or screen), preferred language, and one useful preference to remember. Do not assume parents or family members."
		}
		if len(m.KnowledgeGraph.Nodes) > 0 {
			var facts []string
			for _, n := range m.KnowledgeGraph.Nodes {
				if n.Type == "preference" && n.Value != "" {
					facts = append(facts, n.Value)
				}
			}
			if len(facts) > 0 {
				prompt += "\nPersonal knowledge graph facts about Sheetal (use discreetly):\n- " + strings.Join(facts, "\n- ")
			}
		}
	}
	if a.eleven != nil && a.eleven.Enabled() {
		if text, err := a.eleven.Chat(ctx, prompt, message); err == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		} else if err != nil {
			log.Printf("elevenlabs agent turn failed: %v", err)
		}
	}
	if a.ai != nil && a.ai.Enabled() {
		if text, err := a.ai.Chat(ctx, prompt, message); err == nil {
			return strings.TrimSpace(text)
		} else {
			log.Printf("sarvam chat turn failed: %v", err)
		}
	}
	return ""
}

// captureLearningSignal ports the ClawHub self-improvement workflow into the
// WhatsApp runtime without OpenClaw hooks or transcript access. It captures
// only explicit, short user signals; it never stores credentials or raw logs.
func (a *Agent) captureLearningSignal(raw, text string) {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return
	}
	correction := matchesAny(text, []string{
		"that's wrong", "that is wrong", "not correct", "you misunderstood", "i meant", "no, ",
		"गलत", "नहीं,",
	})
	feature := matchesAny(text, []string{
		"please add", "can you add", "i wish you could", "feature request", "you should support",
		"add a feature", "could you support",
	})
	if correction {
		_, _ = a.store.RecordLearning("correction", clean, "owner message", "Prefer the corrected interpretation on future turns")
		return
	}
	if feature {
		_, _ = a.store.RecordLearning("feature_request", clean, "owner message", "Review before implementing; ask for details if ambiguous")
	}
}

// --- tasks -----------------------------------------------------------------

func (a *Agent) handleTasks(raw, text string) (Reply, bool) {
	// Create/follow-up reminder.
	createKeywords := []string{
		"remind me", "remind me to", "add a task", "add task", "create task", "new task",
		"todo:", "task:", "make a task", "set reminder", "remind me about", "to-do:",
		"याद दिलाना", "टास्क", "काम है", "करना है", "remind",
	}
	if matchesAny(text, createKeywords) && !matchesAny(text, []string{"list", "show", "what", "pending", "status"}) {
		title := extractAfter(raw, []string{"remind me to", "remind me about", "remind me", "add a task", "add task", "create task", "new task:", "todo:", "task:", "make a task", "set reminder", "याद दिलाना"}, text)
		if title == "" {
			return Reply{Text: "What would you like me to remember? Try \"remind me to call Aarti at 7pm\".", Tool: "create_task"}, true
		}
		due := ""
		if m := regexp.MustCompile(`(?i)\bat\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b`).FindStringSubmatch(raw); len(m) > 0 {
			h, _ := strconv.Atoi(m[1])
			min := 0
			if m[2] != "" {
				min, _ = strconv.Atoi(m[2])
			}
			if strings.EqualFold(m[3], "pm") && h < 12 {
				h += 12
			}
			if strings.EqualFold(m[3], "am") && h == 12 {
				h = 0
			}
			n := a.now()
			due = time.Date(n.Year(), n.Month(), n.Day(), h, min, 0, 0, a.location).Format(time.RFC3339)
		}
		t, err := a.store.CreateTask(title, due, "normal", "")
		if err != nil {
			return Reply{Text: "Sorry, I couldn't save that task.", Tool: "create_task"}, true
		}
		return Reply{Text: fmt.Sprintf("Got it, Sheetal — I saved \"%s\" in your task list.", t.Title), Tool: "create_task"}, true
	}

	if matchesAny(text, []string{"complete task", "mark done", "task done", "complete:", "done task", "mark as done", "finished task", "पूरा हो गया", "हो गया"}) {
		title := extractAfter(raw, []string{"complete task", "mark task done", "mark done", "mark as done", "task done", "complete:", "done task", "finished task", "काम पूरा"}, text)
		t := a.store.CompleteTask("", title)
		if t == nil {
			return Reply{Text: "I couldn't find that open task. Type \"what's pending\" to see them.", Tool: "complete_task"}, true
		}
		return Reply{Text: fmt.Sprintf("Done. Marked \"%s\" as complete.", t.Title), Tool: "complete_task"}, true
	}

	if matchesAny(text, []string{"what's pending", "whats pending", "list tasks", "show tasks", "my tasks", "pending tasks", "tasks", "to-do list", "todo list", "todo", "टास्क दिखा", "काम दिखा", "pending"}) {
		tasks := a.store.Tasks()
		open := []store.Task{}
		for _, t := range tasks {
			if t.Status != "done" {
				open = append(open, t)
			}
		}
		if len(open) == 0 {
			return Reply{Text: "No pending tasks. You're all clear 🎉", Tool: "list_tasks"}, true
		}
		sort.Slice(open, func(i, j int) bool {
			rank := map[string]int{"high": 0, "normal": 1, "low": 2}
			return rank[open[i].Priority] < rank[open[j].Priority]
		})
		lines := make([]string, 0, len(open)+1)
		lines = append(lines, fmt.Sprintf("You have %d open task(s):", len(open)))
		for _, t := range open {
			s := fmt.Sprintf("• %s", t.Title)
			if t.Due != "" {
				s += " (" + t.Due + ")"
			}
			if t.Priority == "high" {
				s += " ⭐"
			}
			lines = append(lines, s)
		}
		return Reply{Text: strings.Join(lines, "\n"), Tool: "list_tasks"}, true
	}
	return Reply{}, false
}

// --- notes -----------------------------------------------------------------

func (a *Agent) handleNotes(raw, text string) (Reply, bool) {
	if matchesAny(text, []string{"note:", "save a note", "save note", "add note", "new note", "note down", "remember this:", "make a note", "नोट", "लिख लो", "नोट बना"}) && !matchesAny(text, []string{"my notes", "list notes", "show notes", "read notes"}) {
		title := ""
		body := extractAfter(raw, []string{"note:", "save a note", "save note", "add note", "new note", "note down", "make a note", "नोट"}, text)
		if body == "" {
			return Reply{Text: "What should the note say? Try \"note: boarding pass is ticket B24, gate 12\".", Tool: "save_note"}, true
		}
		firstLine := body
		if idx := strings.IndexAny(firstLine, "\n"); idx > 0 {
			firstLine = firstLine[:idx]
		}
		n, err := a.store.CreateNote(title, body, "")
		if err != nil {
			return Reply{Text: "Sorry, I couldn't save that note.", Tool: "save_note"}, true
		}
		return Reply{Text: fmt.Sprintf("Noted. \"%s\"", n.Title), Tool: "save_note"}, true
	}
	if matchesAny(text, []string{"my notes", "list notes", "show notes", "read notes", "notes", "नोट्स दिखा"}) {
		notes := a.store.Notes()
		if len(notes) == 0 {
			return Reply{Text: "You don't have any notes yet.", Tool: "list_notes"}, true
		}
		lines := make([]string, 0, len(notes)+1)
		lines = append(lines, fmt.Sprintf("Your latest %d note(s):", minInt(len(notes), 6)))
		for i := len(notes) - 1; i >= 0 && len(lines)-1 < 6; i-- {
			n := notes[i]
			excerpt := n.Body
			if len(excerpt) > 80 {
				excerpt = excerpt[:80] + "…"
			}
			lines = append(lines, fmt.Sprintf("• %s", excerpt))
		}
		return Reply{Text: strings.Join(lines, "\n"), Tool: "list_notes"}, true
	}
	return Reply{}, false
}

// --- shopping ----------------------------------------------------------------

func (a *Agent) handleShopping(raw, text string) (Reply, bool) {
	if matchesAny(text, []string{"add ", "add—", "put "}) || matchesAny(text, []string{"shopping", "grocery", "list me", "बाजार", "खरीद", "समान", "list add"}) {
		// Only trigger when there's a clear item after the keyword.
		item := ""
		for _, prefix := range []string{"add ", "add—", "put ", "need to buy ", "buy ", "groceries "} {
			if strings.HasPrefix(text, prefix) {
				item = strings.TrimSpace(raw[len(prefix):])
				break
			}
		}
		if item == "" && strings.Contains(text, "shopping") && strings.Contains(text, "add") {
			item = strings.TrimSpace(raw[strings.Index(raw, "add")+len("add"):])
		}
		trimmed := strings.Trim(item, " \t.!?")
		trimmed = strings.TrimPrefix(strings.TrimPrefix(trimmed, "to "), "my ")
		if trimmed != "" && len(trimmed) > 1 && !matchesAny(strings.ToLower(trimmed), []string{"list", "shopping list", "add to list", "shopping"}) {
			qty, name := splitQuantity(trimmed)
			entry, err := a.store.AddShopping(name, qty, "")
			if err != nil {
				return Reply{Text: "Sorry, I couldn't add that.", Tool: "add_shopping"}, true
			}
			reply := fmt.Sprintf("Added %s to your shopping list.", entry.Item)
			if entry.Quantity != "" {
				reply = fmt.Sprintf("Added %s to your shopping list (%s).", entry.Item, entry.Quantity)
			}
			return Reply{Text: reply, Tool: "add_shopping"}, true
		}
	}

	// "add X to shopping" phrasing.
	if idx := strings.Index(text, " to shopping"); idx > 0 {
		item := strings.TrimSpace(raw[:idx])
		item = strings.TrimPrefix(strings.ToLower(item), "add ")
		if item != "" && !matchesAny(item, []string{"add", "please"}) {
			qty, name := splitQuantity(item)
			entry, err := a.store.AddShopping(name, qty, "")
			if err == nil {
				return Reply{Text: fmt.Sprintf("Added %s to your shopping list.", entry.Item), Tool: "add_shopping"}, true
			}
		}
	}

	if matchesAny(text, []string{"shopping list", "show shopping", "my list", "list items", "what do i need", "shopping", "बाजार की list", "लिस्ट दिखा"}) && !matchesAny(text, []string{"add "}) {
		open := []store.ShoppingItem{}
		for _, x := range a.store.Shopping() {
			if x.Status != "done" {
				open = append(open, x)
			}
		}
		if len(open) == 0 {
			return Reply{Text: "Your shopping list is empty.", Tool: "list_shopping"}, true
		}
		lines := make([]string, 0, len(open)+1)
		lines = append(lines, fmt.Sprintf("Shopping list (%d items):", len(open)))
		for _, x := range open {
			line := "• " + x.Item
			if x.Quantity != "" {
				line += " — " + x.Quantity
			}
			lines = append(lines, line)
		}
		return Reply{Text: strings.Join(lines, "\n"), Tool: "list_shopping"}, true
	}
	return Reply{}, false
}

func splitQuantity(item string) (quantity, name string) {
	item = strings.TrimSpace(item)
	// Match leading quantities like "2 litres milk", "1kg rice", "5 eggs".
	fields := strings.Fields(item)
	if len(fields) >= 2 {
		first := fields[0]
		second := fields[1]
		if isQuantity(first) {
			quantity = strings.ToLower(first)
			if isUnitWord(second) {
				quantity = strings.ToLower(first + " " + second)
				name = strings.TrimSpace(strings.Replace(item, quantity, "", 1))
			} else {
				name = strings.TrimSpace(strings.Replace(item, first, "", 1))
			}
			return strings.TrimSpace(quantity), strings.TrimSpace(name)
		}
	}
	return "", item
}

func isQuantity(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, ch := range s {
		if (ch < '0' || ch > '9') && ch != '.' && ch != '/' {
			return false
		}
	}
	return s != ""
}

func isUnitWord(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "kg", "kgs", "g", "gm", "gram", "grams", "litre", "litres", "liter", "liters", "l", "ml", "packet", "packs", "pack", "bottle", "bottles", "box", "boxes", "dozen", "pieces", "piece", "bags", "bag":
		return true
	}
	return false
}

// --- plans ----------------------------------------------------------------------

func (a *Agent) handlePlans(raw, text string) (Reply, bool) {
	if matchesAny(text, []string{"plan my day", "plan today", "day plan", "make a plan", "plan the day", "schedule my day", "plan:", "my plan", "दिन का plan", "प्लान"}) {
		items := extractAfter(raw, []string{"plan my day for", "plan my day", "plan today", "day plan", "make a plan for", "make a plan", "plan the day for", "plan the day", "schedule my day", "plan:", "my plan"}, text)
		planItems := []string{}
		if items != "" {
			planItems = strings.FieldsFunc(items, func(r rune) bool { return r == ',' || r == ';' || r == '|' || r == '\n' })
		}
		if len(planItems) == 0 {
			// Fall back to prompting.
			if matchesAny(text, []string{"plan today", "plan my day", "day plan", "make a plan", "my plan"}) && !strings.Contains(text, ":") && !strings.Contains(text, ",") {
				p := a.store.Plan("")
				if p != nil && len(p.Items) > 0 {
					lines := make([]string, 0, len(p.Items)+1)
					lines = append(lines, "Here's your plan for today:")
					for _, item := range p.Items {
						lines = append(lines, "• "+item)
					}
					return Reply{Text: strings.Join(lines, "\n"), Tool: "show_plan"}, true
				}
				return Reply{Text: "I can plan your day — just tell me what's on it, e.g. \"plan my day: morning walk, groceries, homework with Charvi, me-time\".", Tool: "plan_day"}, true
			}
		}
		p, err := a.store.SavePlan("", planItems, "")
		if err != nil {
			return Reply{Text: "Sorry, I couldn't save that plan.", Tool: "plan_day"}, true
		}
		if len(p.Items) == 0 {
			return Reply{Text: "That plan looks empty. Try \"plan my day: walk, calls, lunch, gym\".", Tool: "plan_day"}, true
		}
		lines := make([]string, 0, len(p.Items)+1)
		lines = append(lines, "Plan saved for today:")
		for _, item := range p.Items {
			lines = append(lines, "• "+item)
		}
		return Reply{Text: strings.Join(lines, "\n"), Tool: "plan_day"}, true
	}
	if matchesAny(text, []string{"what's my plan", "whats my plan", "show plan", "show my plan", "today's plan", "today plan"}) {
		p := a.store.Plan("")
		if p == nil || len(p.Items) == 0 {
			return Reply{Text: "No plan for today yet. Say \"plan my day: …\" and I'll build one.", Tool: "show_plan"}, true
		}
		lines := make([]string, 0, len(p.Items)+1)
		lines = append(lines, "Here's your plan for today:")
		for _, item := range p.Items {
			lines = append(lines, "• "+item)
		}
		return Reply{Text: strings.Join(lines, "\n"), Tool: "show_plan"}, true
	}
	return Reply{}, false
}

// --- music ------------------------------------------------------------------------

func (a *Agent) handleMusic(raw, text string) (Reply, bool) {
	if matchesAny(text, []string{"now playing", "playing now", "what song", "what is playing", "what's playing", "current song", "क्या बज"}) {
		return a.nowPlayingReply(), true
	}
	if matchesAny(text, []string{"queue", "coming up", "next songs"}) && !matchesAny(text, []string{"add"}) {
		items := a.store.ActiveQueue()
		if len(items) == 0 {
			return Reply{Text: "The request queue is empty — ask me to play something.", Tool: "get_queue"}, true
		}
		lines := make([]string, 0, minInt(len(items), 5)+1)
		lines = append(lines, "In the queue:")
		for _, item := range items[:minInt(len(items), 5)] {
			lines = append(lines, fmt.Sprintf("• %s — %s", item.Name, item.Artist))
		}
		return Reply{Text: strings.Join(lines, "\n"), Tool: "get_queue"}, true
	}

	playKeywords := []string{"play ", "play—", "put on ", "request ", "चलाओ", "बजाओ", "बजा ", "सुनना है", "mood ", "play some", "play a "}
	for _, p := range playKeywords {
		if strings.Contains(text, p) {
			query := extractQueryAfter(raw, text, p)
			if query == "" && strings.Contains(text, "mood") {
				mood := extractQueryAfter(raw, text, "mood")
				return a.moodReply(mood), true
			}
			if query != "" {
				return a.playReply(query), true
			}
		}
	}
	// Mood phrasing like "play a calm mood" or "energetic music".
	if strings.Contains(text, " music") || matchesAny(text, []string{"happy", "sad", "calm", "focus", "sleep", "romantic", "energetic", "travel"}) {
		for _, mood := range []string{"calm", "focus", "happy", "happier", "sad", "soft", "romantic", "travel", "energetic", "sleep"} {
			if strings.Contains(text, mood) {
				return a.moodReply(mood), true
			}
		}
	}
	return Reply{}, false
}

func (a *Agent) nowPlayingReply() Reply {
	state, ok := a.musicState()
	if ok && state.Title != "" {
		s := fmt.Sprintf("%s by %s is playing now.", state.Title, state.Artist)
		if state.Paused {
			s = fmt.Sprintf("%s by %s is paused.", state.Title, state.Artist)
		}
		if state.Device != "" {
			s += " On " + state.Device + "."
		}
		return Reply{Text: s, Tool: "now_playing"}
	}
	return Reply{Text: "Nothing is marked as playing right now.", Tool: "now_playing"}
}

var moodQueries = map[string]string{
	"happy":     "Hindi upbeat feel good",
	"happier":   "Hindi upbeat feel good",
	"sad":       "Hindi soft emotional",
	"soft":      "Hindi soft romantic acoustic",
	"romantic":  "Hindi romantic",
	"focus":     "Hindi instrumental chill",
	"calm":      "Hindi calm acoustic",
	"travel":    "Hindi road trip upbeat",
	"energetic": "Hindi dance workout",
	"sleep":     "Hindi relaxing acoustic",
}

func (a *Agent) moodReply(mood string) Reply {
	key := ""
	for k := range moodQueries {
		if strings.Contains(mood, k) {
			key = k
			break
		}
	}
	query, ok := moodQueries[key]
	if !ok {
		return Reply{Text: fmt.Sprintf("I can set a mood: %s.", strings.Join(sortedKeys(moodQueries), ", ")), Tool: "set_mood"}
	}
	tracks, err := a.spotify.SearchTracks(query, 3)
	if err != nil || len(tracks) == 0 {
		return Reply{Text: "I couldn't find Spotify tracks for that mood right now.", Tool: "set_mood"}
	}
	added := a.enqueueTracks(tracks, "mood")
	if len(added) == 0 {
		return Reply{Text: "Those tracks are already queued.", Tool: "set_mood"}
	}
	a.store.RecordMemory("mood", "", fmt.Sprintf("%s asked for %s music", a.cfg.Station, key))
	lines := make([]string, 0, len(added)+1)
	lines = append(lines, fmt.Sprintf("Set a %s mood. Queued:", key))
	for _, t := range added {
		lines = append(lines, fmt.Sprintf("• %s — %s", t.Name, t.Artist))
	}
	return Reply{Text: strings.Join(lines, "\n"), Tool: "set_mood"}
}

func (a *Agent) playReply(query string) Reply {
	query = strings.TrimSpace(query)
	query = strings.TrimPrefix(query, "please ")
	if len(query) < 2 {
		return Reply{Text: "What would you like me to play?", Tool: "request_song"}
	}
	if !a.spotify.Enabled() {
		return Reply{Text: "Spotify isn't connected yet, so I can't queue songs. Ask the app owner to set SPOTIFY_CLIENT_ID.", Tool: "request_song"}
	}
	tracks, err := a.spotify.SearchTracks(query, 1)
	if err != nil || len(tracks) == 0 {
		return Reply{Text: fmt.Sprintf("I couldn't find \"%s\" on Spotify.", query), Tool: "request_song"}
	}
	track := tracks[0]
	if !a.store.QueueHasURI(track.URI) {
		a.store.SaveQueue(append(a.store.Queue(), store.QueueItem{
			ID: store.NewID("rq"), URI: track.URI, Name: track.Name, Artist: track.Artist,
			Album: track.Album, Art: track.Art, DurMS: track.DurMS, TS: time.Now().Unix(), Status: "queued",
		}))
		a.store.RecordMemory("request", "", fmt.Sprintf("%s — %s", track.Name, track.Artist))
		return Reply{Text: fmt.Sprintf("Queued \"%s\" by %s.", track.Name, track.Artist), Tool: "request_song"}
	}
	return Reply{Text: fmt.Sprintf("\"%s\" is already in the queue.", track.Name), Tool: "request_song"}
}

func (a *Agent) enqueueTracks(tracks []spotify.Track, source string) []spotify.Track {
	q := a.store.Queue()
	added := []spotify.Track{}
	active := map[string]bool{}
	for _, item := range q {
		if item.Status != "done" {
			active[item.URI] = true
		}
	}
	for _, t := range tracks {
		if t.URI == "" || active[t.URI] {
			continue
		}
		q = append(q, store.QueueItem{
			ID: store.NewID("rq"), URI: t.URI, Name: t.Name, Artist: t.Artist,
			Album: t.Album, Art: t.Art, DurMS: t.DurMS, TS: time.Now().Unix(), Status: "queued", Source: source,
		})
		active[t.URI] = true
		added = append(added, t)
	}
	if len(added) > 0 {
		_ = a.store.SaveQueue(q)
	}
	return added
}

// --- email --------------------------------------------------------------------------

func (a *Agent) handleEmail(raw, text string) (Reply, bool) {
	emailKeywords := []string{"email", "inbox", "gmail", "mail", "miss you", "मेल", "ईमेल"}
	if !matchesAny(text, emailKeywords) {
		return Reply{}, false
	}
	if matchesAny(text, []string{"scan gmail", "scan email", "gmail cleanup", "clean gmail", "clean email"}) {
		query := "-in:trash -in:spam"
		cleanupRequested := matchesAny(text, []string{"gmail cleanup", "clean gmail", "clean email", "promotions", "promos", "newsletter"})
		if strings.Contains(text, "promotions") || strings.Contains(text, "promos") {
			query = "category:promotions -in:trash"
		} else if strings.Contains(text, "newsletter") {
			query = "unsubscribe older_than:90d -in:trash -in:spam"
		} else if cleanupRequested {
			query = "category:promotions -in:trash"
		}
		ids, err := a.gmail.SearchIDs(query, 1000)
		if err != nil {
			return Reply{Text: "I couldn't scan Gmail right now. Please check the Gmail connection in the web panel.", Tool: "gmail_scan"}, true
		}
		if len(ids) == 0 {
			return Reply{Text: "I found no matching Gmail messages.", Tool: "gmail_scan"}, true
		}
		if !cleanupRequested {
			return Reply{Text: fmt.Sprintf("I found %d Gmail message(s) outside Trash and Spam. Nothing was changed. Say 'clean Gmail promotions' if you want a reviewable cleanup preview.", len(ids)), Tool: "gmail_scan"}, true
		}
		a.cleanupMu.Lock()
		a.pendingGmailQuery = query
		a.pendingGmailCount = len(ids)
		a.cleanupMu.Unlock()
		return Reply{Text: fmt.Sprintf("I found %d matching Gmail message(s) for %q. Reply 'confirm gmail cleanup' to move them to Trash, or send anything else to cancel.", len(ids), query), Tool: "gmail_scan"}, true
	}
	if !a.gmail.Enabled() {
		return Reply{Text: "Gmail isn't connected yet — connect Google once from the web panel.", Tool: "email_inbox"}, true
	}
	unreadOnly := matchesAny(text, []string{"unread", "अनपढ़ा", "new mail"})
	query := ""
	if unreadOnly {
		query = "is:unread"
	} else if idx := strings.Index(text, "search"); idx >= 0 {
		query = extractAfter(raw, []string{"search email for", "search my email for", "search email", "search mail"}, text)
	} else if strings.Contains(text, "from ") {
		query = "from:" + strings.TrimSpace(raw[idxFrom(text)+5:])
	}
	messages, err := a.gmail.Messages(query, 5)
	if err != nil {
		if strings.Contains(err.Error(), "no Gmail connection") || strings.Contains(strings.ToLower(err.Error()), "no gmail connection") {
			return Reply{Text: "Sheetal needs to connect Gmail once from the web panel before I can read it.", Tool: "email_inbox"}, true
		}
		return Reply{Text: "Gmail is temporarily unavailable. Please try again in a moment.", Tool: "email_inbox"}, true
	}
	if len(messages) == 0 {
		return Reply{Text: "Your inbox looks empty here — no matching messages.", Tool: "email_inbox"}, true
	}
	lines := make([]string, 0, len(messages)+1)
	lines = append(lines, fmt.Sprintf("Top %d message(s):", len(messages)))
	for _, m := range messages {
		from := strings.TrimSpace(m.From)
		if from == "" {
			from = "unknown sender"
		}
		mark := ""
		if m.Unread {
			mark = " [unread]"
		}
		lines = append(lines, fmt.Sprintf("• %s from %s%s", m.Subject, from, mark))
	}
	return Reply{Text: strings.Join(lines, "\n"), Tool: "email_inbox"}, true
}

func (a *Agent) confirmGmailCleanup() Reply {
	a.cleanupMu.Lock()
	query, count := a.pendingGmailQuery, a.pendingGmailCount
	a.pendingGmailQuery, a.pendingGmailCount = "", 0
	a.cleanupMu.Unlock()
	if query == "" || count == 0 {
		return Reply{Text: "There is no pending Gmail cleanup to confirm.", Tool: "gmail_cleanup"}
	}
	n, err := a.gmail.TrashQuery(query, count)
	if err != nil {
		return Reply{Text: "Gmail cleanup failed before completion; nothing more was attempted.", Tool: "gmail_cleanup_error"}
	}
	return Reply{Text: fmt.Sprintf("Done — moved %d Gmail message(s) to Trash. I did not permanently delete anything.", n), Tool: "gmail_cleanup"}
}

func idxFrom(s string) int {
	for i := 0; i+5 <= len(s); i++ {
		if strings.EqualFold(s[i:i+5], "from ") {
			return i
		}
	}
	return -1
}

// --- preferences ----------------------------------------------------------------------

func (a *Agent) handlePreferences(raw, text string) (Reply, bool) {
	if matchesAny(text, []string{"remember that", "remember:", "remember ", "i like ", "i prefer ", "i love ", "i enjoy ", "my favourite is", "my favorite is", "you know what i like", "याद रख", "पसंद है"}) {
		value := extractAfter(raw, []string{"remember that", "remember:", "remember", "i like", "i prefer", "i love", "i enjoy", "my favourite is", "my favorite is"}, text)
		value = strings.TrimPrefix(strings.TrimPrefix(value, "I "), "i ")
		if len(value) < 2 {
			return Reply{Text: "I'll remember — what should the preference be? Try \"remember: no meetings before 10am\".", Tool: "remember"}, true
		}
		kind := "preference"
		if strings.Contains(text, "like ") || strings.Contains(text, "love ") || strings.Contains(text, "favourite") {
			kind = "taste"
		}
		a.store.RecordMemory(kind, "", value)
		return Reply{Text: fmt.Sprintf("Got it — I'll remember that: %s", value), Tool: "remember"}, true
	}
	if matchesAny(text, []string{"what do you remember", "what do you know about me", "my preferences", "my taste", "remembered"}) {
		m := a.store.Memory()
		prefs := memoryTexts(m.Preferences, 5)
		tastes := memoryTexts(m.TasteNotes, 5)
		lines := []string{}
		if len(prefs) > 0 {
			lines = append(lines, "Preferences:")
			for _, p := range prefs {
				lines = append(lines, "• "+p)
			}
		}
		if len(tastes) > 0 {
			lines = append(lines, "Tastes:")
			for _, t := range tastes {
				lines = append(lines, "• "+t)
			}
		}
		if len(lines) == 0 {
			return Reply{Text: "I don't have remembered preferences yet. Tell me \"remember: …\" and I'll keep them.", Tool: "remember"}, true
		}
		return Reply{Text: strings.Join(lines, "\n"), Tool: "remember"}, true
	}
	return Reply{}, false
}

// MusicState mirrors the browser-player state file written by the web UI.
type MusicState struct {
	ClientID   string `json:"client_id"`
	Device     string `json:"device"`
	TrackURI   string `json:"track_uri"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Art        string `json:"art"`
	PositionMS int64  `json:"position_ms"`
	DurationMS int64  `json:"duration_ms"`
	Paused     bool   `json:"paused"`
	UpdatedAt  string `json:"updated_at"`
}

func (a *Agent) musicState() (MusicState, bool) {
	st := MusicState{}
	if err := a.readStateJSON("music-state.json", &st); err != nil {
		return st, false
	}
	return st, st.Title != ""
}

// readStateJSON is a tiny JSON loader used for states only.
func (a *Agent) readStateJSON(name string, into any) error {
	data, err := os.ReadFile(a.store.Dir() + "/" + name)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

func extractAfter(raw string, prefixes []string, lowerText string) string {
	for _, p := range prefixes {
		if idx := strings.Index(lowerText, strings.ToLower(p)); idx >= 0 {
			start := idx + len(p)
			if start <= len(raw) {
				return strings.TrimSpace(raw[start:])
			}
		}
	}
	return ""
}

func extractQueryAfter(raw, text, token string) string {
	idx := strings.Index(text, token)
	if idx < 0 {
		return ""
	}
	start := idx + len(token)
	if start > len(raw) {
		return ""
	}
	return strings.TrimSpace(raw[start:])
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
