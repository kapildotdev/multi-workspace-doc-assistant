package config

import (
	"bufio"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL  string
	GeminiAPIKey string
	ChatModel    string
	EmbedModel   string
	Port         string
	SessionKey   string
	WebhookURL   string
}

func Load() Config {
	loadDotEnv() // local dev convenience; real deploys use actual env vars
	c := Config{
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		GeminiAPIKey: os.Getenv("GEMINI_API_KEY"),
		ChatModel:    os.Getenv("GEMINI_CHAT_MODEL"),
		EmbedModel:   os.Getenv("GEMINI_EMBED_MODEL"),
		Port:         os.Getenv("PORT"),
		WebhookURL:   os.Getenv("DISCORD_WEBHOOK_URL"),
	}
	if c.ChatModel == "" {
		c.ChatModel = "gemini-3.8-flash"
	}
	if c.EmbedModel == "" {
		c.EmbedModel = "gemini-embedding-001" // 768-dim via outputDimensionality below
	}
	if c.Port == "" {
		c.Port = "8080"
	}
	return c
}

// loadDotEnv parses a local .env (KEY=VALUE, # comments, optional quotes)
// and sets vars that aren't already in the environment. No dependency.
func loadDotEnv() {
	f, err := os.Open(".env")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			k := strings.TrimSpace(line[:i])
			v := strings.TrimSpace(line[i+1:])
			v = strings.Trim(v, `"'`)
			if k != "" && os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}
}
