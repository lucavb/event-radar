package radar

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultOpenClawICS = "https://api.lu.ma/ics/get?entity=calendar&id=cal-UWGMNtNXdqgGCx5"
	DefaultClaudeICS   = "https://api.lu.ma/ics/get?entity=calendar&id=cal-TOpA5LAFfuDeFpu"
	DefaultAIAgentsICS = "https://www.meetup.com/ai-agents-munich/events/ical"
	DefaultAIDevICS    = "https://www.meetup.com/munchen-ai-developers-group/events/ical"
)

type Config struct {
	DatabasePath    string
	FeedToken       string
	ListenAddress   string
	SyncInterval    time.Duration
	HTTPTimeout     time.Duration
	SearXNGURL      string
	GeminiEndpoint  string
	GeminiModel     string
	GeminiAPIKey    string
	GeminiToken     string
	GeminiTimeout   time.Duration
	AdminToken      string
	AIThinkererKey  string
	SMTPHost        string
	SMTPUsername    string
	SMTPPassword    string
	SMTPFrom        string
	DigestRecipient string
	NtfyURL         string
	NtfyTopic       string
	OpenClawICS     string
	ClaudeICS       string
	AIAgentsICS     string
	AIDevICS        string
}

func LoadConfig() (Config, error) {
	syncMinutes, err := intEnv("RADAR_SYNC_INTERVAL_MINUTES", 360)
	if err != nil {
		return Config{}, err
	}
	timeoutSeconds, err := intEnv("RADAR_HTTP_TIMEOUT_SECONDS", 20)
	if err != nil {
		return Config{}, err
	}
	geminiTimeoutSeconds, err := intEnv("RADAR_GEMINI_TIMEOUT_SECONDS", 210)
	if err != nil {
		return Config{}, err
	}

	return Config{
		DatabasePath:    stringEnv("RADAR_DATABASE_PATH", "munich-events.db"),
		FeedToken:       stringEnv("RADAR_FEED_TOKEN", "change-me-before-public-use"),
		ListenAddress:   stringEnv("RADAR_LISTEN_ADDRESS", "127.0.0.1:8080"),
		SyncInterval:    time.Duration(syncMinutes) * time.Minute,
		HTTPTimeout:     time.Duration(timeoutSeconds) * time.Second,
		SearXNGURL:      strings.TrimRight(os.Getenv("RADAR_SEARXNG_URL"), "/"),
		GeminiEndpoint:  strings.TrimRight(os.Getenv("RADAR_GEMINI_ENDPOINT"), "/"),
		GeminiModel:     stringEnv("RADAR_GEMINI_MODEL", "gemini-3.5-flash"),
		GeminiAPIKey:    os.Getenv("RADAR_GEMINI_API_KEY"),
		GeminiToken:     os.Getenv("RADAR_GEMINI_TOKEN"),
		GeminiTimeout:   time.Duration(geminiTimeoutSeconds) * time.Second,
		AdminToken:      os.Getenv("RADAR_ADMIN_TOKEN"),
		AIThinkererKey:  os.Getenv("RADAR_AI_TINKERERS_API_KEY"),
		SMTPHost:        os.Getenv("RADAR_SMTP_HOST"),
		SMTPUsername:    os.Getenv("RADAR_SMTP_USERNAME"),
		SMTPPassword:    os.Getenv("RADAR_SMTP_PASSWORD"),
		SMTPFrom:        os.Getenv("RADAR_SMTP_FROM"),
		DigestRecipient: os.Getenv("RADAR_DIGEST_RECIPIENT"),
		NtfyURL:         strings.TrimRight(os.Getenv("RADAR_NTFY_URL"), "/"),
		NtfyTopic:       os.Getenv("RADAR_NTFY_TOPIC"),
		OpenClawICS:     stringEnv("RADAR_OPENCLAW_ICS_URL", DefaultOpenClawICS),
		ClaudeICS:       stringEnv("RADAR_CLAUDE_ICS_URL", DefaultClaudeICS),
		AIAgentsICS:     stringEnv("RADAR_AI_AGENTS_ICS_URL", DefaultAIAgentsICS),
		AIDevICS:        stringEnv("RADAR_AI_DEVELOPERS_ICS_URL", DefaultAIDevICS),
	}, nil
}

func (c Config) Validate() error {
	if c.DatabasePath == "" || c.FeedToken == "" || c.ListenAddress == "" {
		return fmt.Errorf("RADAR_DATABASE_PATH, RADAR_FEED_TOKEN and RADAR_LISTEN_ADDRESS are required")
	}
	if c.SyncInterval <= 0 || c.HTTPTimeout <= 0 {
		return fmt.Errorf("sync interval and HTTP timeout must be positive")
	}
	return nil
}

func stringEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func intEnv(name string, fallback int) (int, error) {
	raw := stringEnv(name, strconv.Itoa(fallback))
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}
