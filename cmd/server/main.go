package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"workspace-assistant/internal/config"
	"workspace-assistant/internal/llm"
	"workspace-assistant/internal/store"
	"workspace-assistant/internal/web"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	var st store.Store
	if cfg.DatabaseURL != "" {
		pg, err := store.NewPostgres(ctx, cfg.DatabaseURL)
		if err != nil {
			log.Printf("postgres unavailable (%v) — using in-memory store", err)
			st = store.NewMemory()
		} else {
			log.Print("using postgres+pgvector store (shared table)")
			st = pg
		}
	} else {
		log.Print("DATABASE_URL unset — using in-memory shared store (local dev)")
		st = store.NewMemory()
	}

	lc := llm.New(cfg.GeminiAPIKey, cfg.ChatModel, cfg.EmbedModel)
	if !lc.HasKey() {
		log.Print("GEMINI_API_KEY unset — LLM runs in offline stub mode (extractive + keyword tools)")
	}

	tpl := template.Must(template.ParseGlob("internal/web/templates/*.html"))
	srv := &web.Server{Store: st, LLM: lc, Cfg: cfg, Tpl: tpl}
	addr := ":" + cfg.Port
	fmt.Println("listening on", addr)
	log.Fatal(http.ListenAndServe(addr, web.NewRouter(srv)))
}
