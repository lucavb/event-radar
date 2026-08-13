package radar

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type ICSFeedConfig struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Anchor         bool   `json:"anchor"`
	FilterLocation bool   `json:"filter_location"`
	ForceConfirmed bool   `json:"force_confirmed"`
}

type Config struct {
	DatabasePath           string
	FeedToken              string
	ListenAddress          string
	SyncInterval           time.Duration
	HTTPTimeout            time.Duration
	AppName                string
	CalendarProdID         string
	Timezone               string
	EventCriteria          string
	LocationAliases        []string
	ICSFeeds               []ICSFeedConfig
	SearXNGURL             string
	SearXNGQueries         []string
	RelevanceWeights       map[string]int
	GeminiEndpoint         string
	GeminiAPIKey           string
	GeminiToken            string
	GeminiTimeout          time.Duration
	GeminiDiscoveryQueries []string
	AdminToken             string
	AITinkerersKey         string
	AITinkerersQuery       string
	SMTPHost               string
	SMTPUsername           string
	SMTPPassword           string
	SMTPFrom               string
	DigestRecipient        string
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
	feeds, err := jsonEnv[[]ICSFeedConfig]("RADAR_ICS_FEEDS", "[]")
	if err != nil {
		return Config{}, err
	}
	aliases, err := jsonEnv[[]string]("RADAR_LOCATION_ALIASES", "[]")
	if err != nil {
		return Config{}, err
	}
	searxQueries, err := jsonEnv[[]string]("RADAR_SEARXNG_QUERIES", "[]")
	if err != nil {
		return Config{}, err
	}
	geminiQueries, err := jsonEnv[[]string]("RADAR_GEMINI_DISCOVERY_QUERIES", "[]")
	if err != nil {
		return Config{}, err
	}
	weights, err := jsonEnv[map[string]int]("RADAR_RELEVANCE_WEIGHTS", "{}")
	if err != nil {
		return Config{}, err
	}
	legacyKey := os.Getenv("RADAR_AI_TINKERERS_API_KEY")

	return Config{
		DatabasePath:           stringEnv("RADAR_DATABASE_PATH", "event-radar.db"),
		FeedToken:              stringEnv("RADAR_FEED_TOKEN", "change-me-before-public-use"),
		ListenAddress:          stringEnv("RADAR_LISTEN_ADDRESS", "127.0.0.1:8080"),
		SyncInterval:           time.Duration(syncMinutes) * time.Minute,
		HTTPTimeout:            time.Duration(timeoutSeconds) * time.Second,
		AppName:                stringEnv("RADAR_APP_NAME", "Event Radar"),
		CalendarProdID:         stringEnv("RADAR_CALENDAR_PRODID", "-//Event Radar//Event Radar//EN"),
		Timezone:               stringEnv("RADAR_TIMEZONE", "UTC"),
		EventCriteria:          strings.TrimSpace(os.Getenv("RADAR_EVENT_CRITERIA")),
		LocationAliases:        aliases,
		ICSFeeds:               feeds,
		SearXNGURL:             strings.TrimRight(os.Getenv("RADAR_SEARXNG_URL"), "/"),
		SearXNGQueries:         searxQueries,
		RelevanceWeights:       weights,
		GeminiEndpoint:         strings.TrimRight(os.Getenv("RADAR_GEMINI_ENDPOINT"), "/"),
		GeminiAPIKey:           os.Getenv("RADAR_GEMINI_API_KEY"),
		GeminiToken:            os.Getenv("RADAR_GEMINI_TOKEN"),
		GeminiTimeout:          time.Duration(geminiTimeoutSeconds) * time.Second,
		GeminiDiscoveryQueries: geminiQueries,
		AdminToken:             os.Getenv("RADAR_ADMIN_TOKEN"),
		AITinkerersKey:         stringEnv("RADAR_AITINKERERS_API_KEY", legacyKey),
		AITinkerersQuery:       strings.TrimSpace(os.Getenv("RADAR_AITINKERERS_QUERY")),
		SMTPHost:               os.Getenv("RADAR_SMTP_HOST"),
		SMTPUsername:           os.Getenv("RADAR_SMTP_USERNAME"),
		SMTPPassword:           os.Getenv("RADAR_SMTP_PASSWORD"),
		SMTPFrom:               os.Getenv("RADAR_SMTP_FROM"),
		DigestRecipient:        os.Getenv("RADAR_DIGEST_RECIPIENT"),
	}, nil
}

func (c Config) Validate() error {
	if c.DatabasePath == "" || c.FeedToken == "" || c.ListenAddress == "" {
		return fmt.Errorf("RADAR_DATABASE_PATH, RADAR_FEED_TOKEN and RADAR_LISTEN_ADDRESS are required")
	}
	if c.SyncInterval <= 0 || c.HTTPTimeout <= 0 {
		return fmt.Errorf("sync interval and HTTP timeout must be positive")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("RADAR_TIMEZONE: %w", err)
	}
	if c.FeedToken == "change-me-before-public-use" {
		return fmt.Errorf("RADAR_FEED_TOKEN must be changed before use")
	}
	seen := map[string]bool{}
	hasFilter := false
	for index, feed := range c.ICSFeeds {
		if strings.TrimSpace(feed.Name) == "" || strings.TrimSpace(feed.URL) == "" {
			return fmt.Errorf("RADAR_ICS_FEEDS entry %d requires name and url", index)
		}
		if seen[feed.Name] {
			return fmt.Errorf("RADAR_ICS_FEEDS entry %d duplicates source %q", index, feed.Name)
		}
		seen[feed.Name] = true
		parsed, err := url.Parse(feed.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("RADAR_ICS_FEEDS entry %d has invalid HTTP(S) url", index)
		}
		hasFilter = hasFilter || feed.FilterLocation
	}
	if hasFilter && len(cleanList(c.LocationAliases)) == 0 {
		return fmt.Errorf("RADAR_LOCATION_ALIASES is required by a filtered ICS feed")
	}
	if c.SearXNGURL != "" && len(cleanList(c.SearXNGQueries)) == 0 {
		return fmt.Errorf("RADAR_SEARXNG_QUERIES is required when RADAR_SEARXNG_URL is set")
	}
	geminiConfigured := c.GeminiEndpoint != "" || c.GeminiAPIKey != "" || c.GeminiToken != ""
	if geminiConfigured {
		if c.GeminiEndpoint == "" || (c.GeminiAPIKey == "" && c.GeminiToken == "") {
			return fmt.Errorf("Gemini requires RADAR_GEMINI_ENDPOINT and an API key or token")
		}
		if len(cleanList(c.GeminiDiscoveryQueries)) == 0 || c.EventCriteria == "" {
			return fmt.Errorf("RADAR_GEMINI_DISCOVERY_QUERIES and RADAR_EVENT_CRITERIA are required for Gemini")
		}
	}
	if c.AITinkerersKey != "" && c.AITinkerersQuery == "" {
		return fmt.Errorf("RADAR_AITINKERERS_QUERY is required when the AI Tinkerers API key is set")
	}
	if len(c.ICSFeeds) == 0 && c.SearXNGURL == "" && !geminiConfigured && c.AITinkerersKey == "" {
		return fmt.Errorf("at least one event source must be configured")
	}
	for term, weight := range c.RelevanceWeights {
		if strings.TrimSpace(term) == "" || weight <= 0 {
			return fmt.Errorf("RADAR_RELEVANCE_WEIGHTS must contain non-empty terms with positive weights")
		}
	}
	return nil
}

func jsonEnv[T any](name, fallback string) (T, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		raw = fallback
	}
	var value T
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return value, fmt.Errorf("%s must contain valid JSON: %w", name, err)
	}
	return value, nil
}

func cleanList(values []string) []string {
	output := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value != "" {
			output = append(output, value)
		}
	}
	return output
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
