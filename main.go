// SD Sheetal — WhatsApp personal assistant.
//
// A Go rewrite of the sdsheetal Python assistant, carried on WhatsApp via
// whatsmeow. The web UI gives owner pairing (QR scan), a live status panel,
// a command console that drives the same agent, and manual tools.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/vishalgojha/sdsheetal/internal/assistant"
	"github.com/vishalgojha/sdsheetal/internal/config"
	"github.com/vishalgojha/sdsheetal/internal/eleven"
	"github.com/vishalgojha/sdsheetal/internal/gmail"
	"github.com/vishalgojha/sdsheetal/internal/openrouter"
	"github.com/vishalgojha/sdsheetal/internal/sarvam"
	"github.com/vishalgojha/sdsheetal/internal/spotify"
	"github.com/vishalgojha/sdsheetal/internal/store"
	"github.com/vishalgojha/sdsheetal/internal/tts"
	"github.com/vishalgojha/sdsheetal/internal/web"
	"github.com/vishalgojha/sdsheetal/internal/whatsapp"
)

func main() {
	cfg := config.Load()

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	st := store.New(cfg.DataDir)
	sp := spotify.New(cfg)
	voice := tts.New(cfg)
	sv := sarvam.New(cfg)
	or := openrouter.New(cfg)
	el := eleven.New(cfg)
	gm := gmail.New(cfg)
	agent := assistant.New(cfg, st, sp, gm, sv, or, el)

	wa := whatsapp.New(cfg, st, agent, voice, sv)
	if err := wa.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "whatsapp start:", err)
		os.Exit(1)
	}

	srv := web.New(cfg, st, agent, sp, gm, wa)

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("sdsheetal (whatsmeow agent) listening on :%s", cfg.Port)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()
	go reminderLoop(st, wa, cfg.TimeZone)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Printf("shutting down…")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wa.Disconnect()
	_ = httpSrv.Shutdown(ctx)
}

func reminderLoop(st *store.Store, wa *whatsapp.Client, zone string) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.FixedZone("IST", 5*3600+30*60)
	}
	for {
		now := time.Now().In(loc)
		for _, t := range st.Tasks() {
			if t.Status != "open" || t.Due == "" {
				continue
			}
			// Deliver overdue reminders after a restart or brief outage as well;
			// successful delivery completes the task so it is never repeated.
			due, err := time.Parse(time.RFC3339, t.Due)
			if err != nil || due.After(now) || now.Sub(due) > 24*time.Hour {
				continue
			}
			if err := wa.SendTextToOwner("Reminder, Sheetal: " + t.Title); err != nil {
				log.Printf("reminder %s: %v", t.ID, err)
				continue
			}
			// Supplement WhatsApp with a native desktop alert when Agent V's
			// companion bridge is online (useful when self-chat is muted).
			_, _ = st.EnqueueBridge("laptop", "notify", map[string]any{"title": "Agent V reminder", "message": t.Title})
			st.CompleteTask(t.ID, "")
			log.Printf("delivered reminder %s", t.ID)
		}
		time.Sleep(15 * time.Second)
	}
}
