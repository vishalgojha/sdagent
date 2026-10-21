// Package store provides persistent JSON file storage for the assistant.
// File formats match the original Python sdsheetal app so an existing mounted
// /data volume can be migrated without conversion.
package store

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store keeps assistant data on disk under a single data directory.
type Store struct {
	dir     string
	mu      sync.Mutex
	nowFunc func() time.Time
}

func New(dir string) *Store { return &Store{dir: dir, nowFunc: time.Now} }

func (s *Store) Dir() string { return s.dir }

func (s *Store) now() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

func (s *Store) file(name string) string { return filepath.Join(s.dir, name) }

// ReadJSONFile loads an arbitrary JSON file from the data dir.
func (s *Store) ReadJSONFile(name string, into any) error {
	return s.readJSON(s.file(name), into)
}

// WriteJSONFile stores an arbitrary JSON value under the data dir.
func (s *Store) WriteJSONFile(name string, value any) error {
	return s.writeJSON(s.file(name), value)
}

func (s *Store) readJSON(path string, into any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func (s *Store) writeJSON(path string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Records duplicating the Python on-disk shapes.

type Task struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Due         string `json:"due,omitempty"`
	Priority    string `json:"priority,omitempty"`
	Notes       string `json:"notes,omitempty"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	CompletedAt string `json:"completed_at,omitempty"`
}

// Bill stores safe payment metadata only. It must never contain card numbers,
// bank credentials, UPI credentials, or payment links with secrets.
type Bill struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"` // bill, emi, subscription, insurance, sip
	Amount       string `json:"amount,omitempty"`
	Currency     string `json:"currency,omitempty"`
	DueDate      string `json:"due_date"`
	Frequency    string `json:"frequency"`
	AccountLast4 string `json:"account_last4,omitempty"`
	Autopay      bool   `json:"autopay,omitempty"`
	Status       string `json:"status"`
	Notes        string `json:"notes,omitempty"`
	LastPaidAt   string `json:"last_paid_at,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type Note struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Tags      string `json:"tags,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type ShoppingItem struct {
	ID        string `json:"id"`
	Item      string `json:"item"`
	Quantity  string `json:"quantity,omitempty"`
	Category  string `json:"category,omitempty"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Plan struct {
	ID        string   `json:"id"`
	Date      string   `json:"date"`
	Items     []string `json:"items"`
	Summary   string   `json:"summary,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

type MemoryEntry struct {
	Text     string `json:"text,omitempty"`
	Category string `json:"category,omitempty"`
	Track    string `json:"track,omitempty"`

	TS int64 `json:"ts"`
}

type Memory struct {
	Plays   map[string]int `json:"plays"`
	Skips   map[string]int `json:"skips"`
	Replays map[string]int `json:"replays"`

	Preferences    []MemoryEntry  `json:"preferences"`
	TasteNotes     []MemoryEntry  `json:"taste_notes"`
	Conversations  []MemoryEntry  `json:"conversations"`
	Moods          []MemoryEntry  `json:"moods"`
	KnowledgeGraph KnowledgeGraph `json:"knowledge_graph"`

	Updated int64 `json:"updated"`
}

type KnowledgeNode struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Label   string `json:"label"`
	Value   string `json:"value,omitempty"`
	Updated int64  `json:"updated"`
}
type KnowledgeEdge struct {
	From     string `json:"from"`
	Relation string `json:"relation"`
	To       string `json:"to"`
}
type KnowledgeGraph struct {
	Nodes []KnowledgeNode `json:"nodes"`
	Edges []KnowledgeEdge `json:"edges"`
}

type QueueItem struct {
	ID     string `json:"id"`
	URI    string `json:"uri"`
	Name   string `json:"name"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
	Art    string `json:"art"`
	DurMS  int64  `json:"dur_ms"`
	TS     int64  `json:"ts"`
	Status string `json:"status"`
	Source string `json:"source,omitempty"`
}

type BridgeJob struct {
	ID         string         `json:"id"`
	DeviceID   string         `json:"device_id"`
	Action     string         `json:"action"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Status     string         `json:"status"`
	Result     string         `json:"result,omitempty"`
	CreatedAt  string         `json:"created_at"`
	UpdatedAt  string         `json:"updated_at"`
}

// Learning is a durable self-improvement record captured from the owner's
// corrections, failed actions, and requested capabilities. It deliberately
// stores short, redacted summaries rather than transcripts or secrets.
type Learning struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Summary         string `json:"summary"`
	Context         string `json:"context,omitempty"`
	SuggestedAction string `json:"suggested_action,omitempty"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

func (s *Store) Learnings() []Learning {
	out := []Learning{}
	if err := s.readJSON(s.file("learnings.json"), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) SaveLearnings(entries []Learning) error {
	return s.writeJSON(s.file("learnings.json"), entries)
}

// RecordLearning appends a bounded, non-secret self-improvement record.
func (s *Store) RecordLearning(kind, summary, context, suggestedAction string) (Learning, error) {
	summary = CleanText(summary, 500)
	if summary == "" {
		return Learning{}, fmt.Errorf("learning summary required")
	}
	kind = strings.ToLower(CleanText(kind, 40))
	if kind == "" {
		kind = "insight"
	}
	now := NowISO()
	entry := Learning{
		ID: NewID("learn"), Kind: kind, Summary: summary,
		Context: CleanText(context, 300), SuggestedAction: CleanText(suggestedAction, 300),
		Status: "pending", CreatedAt: now, UpdatedAt: now,
	}
	entries := append(s.Learnings(), entry)
	if len(entries) > 200 {
		entries = entries[len(entries)-200:]
	}
	return entry, s.SaveLearnings(entries)
}

// ActiveLearningContext returns recent pending learnings suitable for a model
// prompt. Resolved records stay on disk for auditability but are not injected.
func (s *Store) ActiveLearningContext(limit int) []Learning {
	entries := s.Learnings()
	out := make([]Learning, 0, limit)
	for i := len(entries) - 1; i >= 0 && len(out) < limit; i-- {
		if entries[i].Status == "resolved" || entries[i].Status == "wont_fix" {
			continue
		}
		out = append(out, entries[i])
	}
	return out
}

func (s *Store) BridgeJobs() []BridgeJob {
	var out []BridgeJob
	_ = s.readJSON(s.file("bridge-jobs.json"), &out)
	return out
}
func (s *Store) SaveBridgeJobs(v []BridgeJob) error {
	return s.writeJSON(s.file("bridge-jobs.json"), v)
}
func (s *Store) EnqueueBridge(device, action string, params map[string]any) (BridgeJob, error) {
	if strings.TrimSpace(action) == "" {
		return BridgeJob{}, fmt.Errorf("bridge action required")
	}
	now := NowISO()
	j := BridgeJob{ID: NewID("bridge"), DeviceID: CleanText(device, 80), Action: CleanText(action, 80), Parameters: params, Status: "queued", CreatedAt: now, UpdatedAt: now}
	jobs := append(s.BridgeJobs(), j)
	if len(jobs) > 200 {
		jobs = jobs[len(jobs)-200:]
	}
	return j, s.SaveBridgeJobs(jobs)
}

// Helpers.

func CleanText(v string, limit int) string {
	fields := strings.Fields(v)
	out := strings.Join(fields, " ")
	runes := []rune(out)
	if limit > 0 && len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func NewID(prefix string) string {
	buf := make([]byte, 7)
	_, _ = rand.Read(buf)
	return prefix + "-" + base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(buf)
}

func NowISO() string { return time.Now().UTC().Format(time.RFC3339) }

// --- tasks -----------------------------------------------------------------

func (s *Store) Tasks() []Task {
	out := []Task{}
	if err := s.readJSON(s.file("tasks.json"), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) SaveTasks(tasks []Task) error { return s.writeJSON(s.file("tasks.json"), tasks) }

func (s *Store) Bills() []Bill {
	out := []Bill{}
	if err := s.readJSON(s.file("bills.json"), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) SaveBills(bills []Bill) error { return s.writeJSON(s.file("bills.json"), bills) }

func (s *Store) CreateBill(name, kind, amount, dueDate, frequency, last4, notes string, autopay bool) (Bill, error) {
	name = CleanText(name, 120)
	if name == "" {
		return Bill{}, fmt.Errorf("bill name required")
	}
	kind = strings.ToLower(CleanText(kind, 30))
	if kind == "" {
		kind = "bill"
	}
	frequency = strings.ToLower(CleanText(frequency, 30))
	if frequency == "" {
		frequency = "monthly"
	}
	now := NowISO()
	bill := Bill{
		ID: NewID("bill"), Name: name, Kind: kind, Amount: CleanText(amount, 40), Currency: "INR",
		DueDate: CleanText(dueDate, 40), Frequency: frequency, AccountLast4: CleanText(last4, 4),
		Autopay: autopay, Status: "open", Notes: CleanText(notes, 300), CreatedAt: now, UpdatedAt: now,
	}
	bills := append(s.Bills(), bill)
	if len(bills) > 300 {
		bills = bills[len(bills)-300:]
	}
	return bill, s.SaveBills(bills)
}

func (s *Store) MarkBillPaid(id, name string) *Bill {
	bills := s.Bills()
	for i := range bills {
		b := &bills[i]
		match := (id != "" && b.ID == CleanText(id, 100)) || (name != "" && strings.Contains(strings.ToLower(b.Name), strings.ToLower(CleanText(name, 120))))
		if !match {
			continue
		}
		b.Status = "paid"
		b.LastPaidAt = NowISO()
		b.UpdatedAt = NowISO()
		_ = s.SaveBills(bills)
		return b
	}
	return nil
}

func (s *Store) CreateTask(title, due, priority, notes string) (Task, error) {
	title = CleanText(title, 180)
	if title == "" {
		return Task{}, fmt.Errorf("task title required")
	}
	priority = strings.ToLower(CleanText(priority, 20))
	if priority != "low" && priority != "normal" && priority != "high" {
		priority = "normal"
	}
	now := NowISO()
	t := Task{
		ID: NewID("task"), Title: title, Due: CleanText(due, 80),
		Priority: priority, Notes: CleanText(notes, 400),
		Status: "open", CreatedAt: now, UpdatedAt: now,
	}
	tasks := s.Tasks()
	keep := append(tasks, t)
	if len(keep) > 300 {
		keep = keep[len(keep)-300:]
	}
	return t, s.SaveTasks(keep)
}

func (s *Store) FindOpenTask(id, title string) *Task {
	id = CleanText(id, 100)
	name := strings.ToLower(CleanText(title, 180))
	tasks := s.Tasks()
	for i := range tasks {
		t := &tasks[i]
		if t.Status == "done" {
			continue
		}
		if id != "" && t.ID == id {
			return t
		}
		if name != "" {
			b := strings.ToLower(t.Title)
			if b == name || strings.Contains(b, name) {
				return t
			}
		}
	}
	return nil
}

func (s *Store) CompleteTask(id, title string) *Task {
	tasks := s.Tasks()
	for i := range tasks {
		t := &tasks[i]
		if t.Status == "done" {
			continue
		}
		match := false
		if CleanText(id, 100) != "" {
			match = t.ID == CleanText(id, 100)
		} else if CleanText(title, 180) != "" {
			b := strings.ToLower(t.Title)
			match = b == strings.ToLower(CleanText(title, 180)) || strings.Contains(b, strings.ToLower(CleanText(title, 180)))
		}
		if match {
			t.Status = "done"
			t.CompletedAt = NowISO()
			t.UpdatedAt = NowISO()
			_ = s.SaveTasks(tasks)
			return t
		}
	}
	return nil
}

func (s *Store) RemoveTask(id, title string) *Task {
	target := s.FindOpenTask(id, title)
	if target == nil {
		return nil
	}
	tasks := s.Tasks()
	kept := tasks[:0]
	for _, t := range tasks {
		if t.ID != target.ID {
			kept = append(kept, t)
		}
	}
	_ = s.SaveTasks(kept)
	return target
}

// --- notes -----------------------------------------------------------------

func (s *Store) Notes() []Note {
	out := []Note{}
	if err := s.readJSON(s.file("notes.json"), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) SaveNotes(notes []Note) error { return s.writeJSON(s.file("notes.json"), notes) }

func (s *Store) CreateNote(title, body, tags string) (Note, error) {
	title = CleanText(title, 120)
	if title == "" {
		title = "Untitled note"
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return Note{}, fmt.Errorf("note body required")
	}
	runes := []rune(body)
	if len(runes) > 2000 {
		body = string(runes[:2000])
	}
	now := NowISO()
	n := Note{ID: NewID("note"), Title: title, Body: body, Tags: CleanText(tags, 160), CreatedAt: now, UpdatedAt: now}
	notes := s.Notes()
	keep := append(notes, n)
	if len(keep) > 300 {
		keep = keep[len(keep)-300:]
	}
	return n, s.SaveNotes(keep)
}

func (s *Store) RemoveNote(id string) *Note {
	id = CleanText(id, 100)
	notes := s.Notes()
	for i := range notes {
		if notes[i].ID == id {
			removed := notes[i]
			_ = s.SaveNotes(append(notes[:i], notes[i+1:]...))
			return &removed
		}
	}
	return nil
}

// --- shopping --------------------------------------------------------------

func (s *Store) Shopping() []ShoppingItem {
	out := []ShoppingItem{}
	if err := s.readJSON(s.file("shopping.json"), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) SaveShopping(items []ShoppingItem) error {
	return s.writeJSON(s.file("shopping.json"), items)
}

func (s *Store) AddShopping(item, quantity, category string) (ShoppingItem, error) {
	item = CleanText(item, 140)
	if item == "" {
		return ShoppingItem{}, fmt.Errorf("shopping item required")
	}
	now := NowISO()
	items := s.Shopping()
	for i := range items {
		x := &items[i]
		if x.Status != "done" && strings.EqualFold(x.Item, item) {
			if quantity != "" {
				x.Quantity = CleanText(quantity, 60)
			}
			x.UpdatedAt = now
			_ = s.SaveShopping(items)
			return *x, nil
		}
	}
	entry := ShoppingItem{
		ID: NewID("shop"), Item: item, Quantity: CleanText(quantity, 60),
		Category: CleanText(category, 60), Status: "open", CreatedAt: now, UpdatedAt: now,
	}
	keep := append(items, entry)
	if len(keep) > 300 {
		keep = keep[len(keep)-300:]
	}
	return entry, s.SaveShopping(keep)
}

func (s *Store) ToggleShopping(id string) *ShoppingItem {
	id = CleanText(id, 100)
	items := s.Shopping()
	for i := range items {
		if items[i].ID == id {
			if items[i].Status == "done" {
				items[i].Status = "open"
			} else {
				items[i].Status = "done"
			}
			items[i].UpdatedAt = NowISO()
			_ = s.SaveShopping(items)
			return &items[i]
		}
	}
	return nil
}

func (s *Store) RemoveShopping(id string) *ShoppingItem {
	id = CleanText(id, 100)
	items := s.Shopping()
	for i := range items {
		if items[i].ID == id {
			removed := items[i]
			_ = s.SaveShopping(append(items[:i], items[i+1:]...))
			return &removed
		}
	}
	return nil
}

// --- plans -----------------------------------------------------------------

func (s *Store) Plans() []Plan {
	out := []Plan{}
	if err := s.readJSON(s.file("plans.json"), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) SavePlans(plans []Plan) error { return s.writeJSON(s.file("plans.json"), plans) }

func normalizePlanItems(items []string) []string {
	out := make([]string, 0, len(items))
	for _, raw := range items {
		parts := strings.Split(CleanText(raw, 180), ",")
		for _, p := range parts {
			p = strings.TrimSpace(strings.Trim(p, " -•"))
			if p != "" {
				out = append(out, p)
			}
		}
		if len(out) >= 30 {
			break
		}
	}
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

func (s *Store) SavePlan(date string, items []string, summary string) (Plan, error) {
	if CleanText(date, 40) == "" {
		date = s.now().Format("2006-01-02")
	}
	items = normalizePlanItems(items)
	now := NowISO()
	plans := s.Plans()
	kept := plans[:0]
	var old *Plan
	for i := range plans {
		if plans[i].Date == date {
			old = &plans[i]
		} else {
			kept = append(kept, plans[i])
		}
	}
	p := Plan{ID: NewID("plan"), Date: date, Items: items, Summary: CleanText(summary, 400), UpdatedAt: now}
	if old != nil {
		p.ID = old.ID
		p.CreatedAt = old.CreatedAt
	} else {
		p.CreatedAt = now
	}
	kept = append(kept, p)
	if len(kept) > 180 {
		kept = kept[len(kept)-180:]
	}
	return p, s.SavePlans(kept)
}

func (s *Store) Plan(date string) *Plan {
	date = CleanText(date, 40)
	if date == "" {
		date = s.now().Format("2006-01-02")
	}
	for _, p := range s.Plans() {
		if p.Date == date {
			p2 := p
			return &p2
		}
	}
	return nil
}

// --- memory ---------------------------------------------------------------

func (s *Store) Memory() *Memory {
	m := &Memory{}
	if err := s.readJSON(s.file("radio-memory.json"), m); err != nil {
		m = &Memory{}
	}
	if m.Plays == nil {
		m.Plays = map[string]int{}
	}
	if m.Skips == nil {
		m.Skips = map[string]int{}
	}
	if m.Replays == nil {
		m.Replays = map[string]int{}
	}
	return m
}

func (s *Store) SaveMemory(m *Memory) error {
	m.Updated = s.now().Unix()
	return s.writeJSON(s.file("radio-memory.json"), m)
}

func MemoryTop(bucket map[string]int, n int) []string {
	type pair struct {
		name string
		n    int
	}
	prs := make([]pair, 0, len(bucket))
	for name, count := range bucket {
		prs = append(prs, pair{name, count})
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].n > prs[j].n })
	out := make([]string, 0, n)
	for i := 0; i < n && i < len(prs); i++ {
		out = append(out, prs[i].name)
	}
	return out
}

// RecordMemory stores behaviour signals (bounded lists, no audio/tokens).
func (s *Store) RecordMemory(kind, track, text string) {
	m := s.Memory()
	switch kind {
	case "play", "skip", "replay":
		if track != "" {
			bucket := m.Plays
			switch kind {
			case "skip":
				bucket = m.Skips
			case "replay":
				bucket = m.Replays
			}
			track = CleanText(track, 240)
			bucket[track]++
		}
	case "conversation":
		s.ensurePersonNode(m)
		if text != "" {
			m.Conversations = append(m.Conversations, MemoryEntry{Text: CleanText(text, 400), TS: s.now().Unix()})
			if n := len(m.Conversations); n > 80 {
				m.Conversations = m.Conversations[n-80:]
			}
		}
	case "mood":
		if text != "" {
			m.Moods = append(m.Moods, MemoryEntry{Text: CleanText(text, 240), TS: s.now().Unix()})
			if n := len(m.Moods); n > 50 {
				m.Moods = m.Moods[n-50:]
			}
		}
	case "preference":
		if text != "" {
			s.ensurePersonNode(m)
			nodeID := "preference-" + NewID("kg")
			m.KnowledgeGraph.Nodes = append(m.KnowledgeGraph.Nodes, KnowledgeNode{ID: nodeID, Type: "preference", Label: CleanText(text, 120), Value: CleanText(text, 300), Updated: s.now().Unix()})
			m.KnowledgeGraph.Edges = append(m.KnowledgeGraph.Edges, KnowledgeEdge{From: "person-sheetal", Relation: "prefers", To: nodeID})
			m.Preferences = append(m.Preferences, MemoryEntry{Text: CleanText(text, 300), TS: s.now().Unix()})
			if n := len(m.Preferences); n > 80 {
				m.Preferences = m.Preferences[n-80:]
			}
		}
	case "taste":
		if text != "" {
			m.TasteNotes = append(m.TasteNotes, MemoryEntry{Text: CleanText(text, 300), TS: s.now().Unix()})
			if n := len(m.TasteNotes); n > 80 {
				m.TasteNotes = m.TasteNotes[n-80:]
			}
		}
	}
	_ = s.SaveMemory(m)
}

func (s *Store) ensurePersonNode(m *Memory) {
	for _, n := range m.KnowledgeGraph.Nodes {
		if n.ID == "person-sheetal" {
			return
		}
	}
	m.KnowledgeGraph.Nodes = append(m.KnowledgeGraph.Nodes, KnowledgeNode{ID: "person-sheetal", Type: "person", Label: "Sheetal", Updated: s.now().Unix()})
}

// --- queue -----------------------------------------------------------------

func (s *Store) Queue() []QueueItem {
	out := []QueueItem{}
	if err := s.readJSON(s.file("requests.json"), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) SaveQueue(q []QueueItem) error { return s.writeJSON(s.file("requests.json"), q) }

func (s *Store) QueueHasURI(uri string) bool {
	for _, item := range s.Queue() {
		if item.URI == uri && item.Status != "done" {
			return true
		}
	}
	return false
}

func (s *Store) ActiveQueue() []QueueItem {
	out := []QueueItem{}
	for _, item := range s.Queue() {
		if item.Status != "done" {
			out = append(out, item)
		}
	}
	return out
}
