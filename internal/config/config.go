package config

import "os"

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
	c := Config{
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		GeminiAPIKey: os.Getenv("GEMINI_API_KEY"),
		ChatModel:    os.Getenv("GEMINI_CHAT_MODEL"),
		EmbedModel:   os.Getenv("GEMINI_EMBED_MODEL"),
		Port:         os.Getenv("PORT"),
		WebhookURL:   os.Getenv("DISCORD_WEBHOOK_URL"),
	}
	if c.ChatModel == "" {
		c.ChatModel = "gemini-2.0-flash"
	}
	if c.EmbedModel == "" {
		c.EmbedModel = "text-embedding-004"
	}
	if c.Port == "" {
		c.Port = "8080"
	}
	return c
}
