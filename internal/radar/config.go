package radar

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lucabecker/event-radar/internal/auth"
)

type ICSFeedConfig struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Anchor         bool   `json:"anchor"`
	FilterLocation bool   `json:"filter_location"`
	ForceConfirmed bool   `json:"force_confirmed"`
}

// Branding carries the user-visible identity shared by the calendar feed and
// the digest: application name, calendar PRODID, and timezone.
type Branding struct {
	AppName        string
	CalendarProdID string
	Timezone       string
}

// Sources carries every anchored feed and discovery provider configuration.
type Sources struct {
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
	AITinkerersKey         string
	AITinkerersQuery       string
}

// Delivery carries the SMTP settings for the opt-in email digest.
type Delivery struct {
	SMTPHost        string
	SMTPUsername    string
	SMTPPassword    string
	SMTPFrom        string
	DigestRecipient string
}

// Observability carries logging and tracing configuration.
type Observability struct {
	LogFormat      string
	LogLevel       string
	TracingEnabled string
}

// Runtime carries process-level settings: storage, serving, scheduling, and
// admin authentication (break-glass token and OIDC login).
type Runtime struct {
	DatabasePath       string
	FeedToken          string
	ListenAddress      string
	SyncInterval       time.Duration
	HTTPTimeout        time.Duration
	AdminToken         string
	OIDCIssuer         string
	OIDCClientID       string
	OIDCClientSecret   string
	OIDCRedirectURL    string
	OIDCScopes         []string
	OIDCAllowedEmails  []string
	OIDCAllowedDomains []string
	OIDCAllowedSubs    []string
	TrustProxy         bool
}

// Config groups configuration into the sub-structs each consumer reads.
type Config struct {
	Branding      Branding
	Sources       Sources
	Delivery      Delivery
	Observability Observability
	Runtime       Runtime
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
		Branding: Branding{
			AppName:        stringEnv("RADAR_APP_NAME", "Event Radar"),
			CalendarProdID: stringEnv("RADAR_CALENDAR_PRODID", "-//Event Radar//Event Radar//EN"),
			Timezone:       stringEnv("RADAR_TIMEZONE", "UTC"),
		},
		Sources: Sources{
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
			AITinkerersKey:         stringEnv("RADAR_AITINKERERS_API_KEY", legacyKey),
			AITinkerersQuery:       strings.TrimSpace(os.Getenv("RADAR_AITINKERERS_QUERY")),
		},
		Delivery: Delivery{
			SMTPHost:        os.Getenv("RADAR_SMTP_HOST"),
			SMTPUsername:    os.Getenv("RADAR_SMTP_USERNAME"),
			SMTPPassword:    os.Getenv("RADAR_SMTP_PASSWORD"),
			SMTPFrom:        os.Getenv("RADAR_SMTP_FROM"),
			DigestRecipient: os.Getenv("RADAR_DIGEST_RECIPIENT"),
		},
		Observability: Observability{
			LogFormat:      stringEnv("RADAR_LOG_FORMAT", "text"),
			LogLevel:       stringEnv("RADAR_LOG_LEVEL", "info"),
			TracingEnabled: stringEnv("RADAR_TRACING_ENABLED", ""),
		},
		Runtime: Runtime{
			DatabasePath:       stringEnv("RADAR_DATABASE_PATH", "event-radar.db"),
			FeedToken:          stringEnv("RADAR_FEED_TOKEN", "change-me-before-public-use"),
			ListenAddress:      stringEnv("RADAR_LISTEN_ADDRESS", "127.0.0.1:8080"),
			SyncInterval:       time.Duration(syncMinutes) * time.Minute,
			HTTPTimeout:        time.Duration(timeoutSeconds) * time.Second,
			AdminToken:         os.Getenv("RADAR_ADMIN_TOKEN"),
			OIDCIssuer:         strings.TrimSpace(os.Getenv("RADAR_OIDC_ISSUER")),
			OIDCClientID:       strings.TrimSpace(os.Getenv("RADAR_OIDC_CLIENT_ID")),
			OIDCClientSecret:   strings.TrimSpace(os.Getenv("RADAR_OIDC_CLIENT_SECRET")),
			OIDCRedirectURL:    strings.TrimSpace(os.Getenv("RADAR_OIDC_REDIRECT_URL")),
			OIDCScopes:         listEnv("RADAR_OIDC_SCOPES"),
			OIDCAllowedEmails:  listEnv("RADAR_OIDC_ALLOWED_EMAILS"),
			OIDCAllowedDomains: listEnv("RADAR_OIDC_ALLOWED_DOMAINS"),
			OIDCAllowedSubs:    listEnv("RADAR_OIDC_ALLOWED_SUBS"),
			TrustProxy:         boolEnv("RADAR_TRUST_PROXY"),
		},
	}, nil
}

func (c Config) Validate() error {
	if c.Runtime.DatabasePath == "" || c.Runtime.FeedToken == "" || c.Runtime.ListenAddress == "" {
		return fmt.Errorf("RADAR_DATABASE_PATH, RADAR_FEED_TOKEN and RADAR_LISTEN_ADDRESS are required")
	}
	if c.Runtime.SyncInterval <= 0 || c.Runtime.HTTPTimeout <= 0 {
		return fmt.Errorf("sync interval and HTTP timeout must be positive")
	}
	if !slices.Contains([]string{"", "text", "json"}, c.Observability.LogFormat) {
		return fmt.Errorf("RADAR_LOG_FORMAT must be empty, text, or json")
	}
	if !slices.Contains([]string{"", "debug", "info", "warn", "error"}, c.Observability.LogLevel) {
		return fmt.Errorf("RADAR_LOG_LEVEL must be empty, debug, info, warn, or error")
	}
	if !slices.Contains([]string{"", "true"}, c.Observability.TracingEnabled) {
		return fmt.Errorf("RADAR_TRACING_ENABLED must be empty or true")
	}
	if _, err := time.LoadLocation(c.Branding.Timezone); err != nil {
		return fmt.Errorf("RADAR_TIMEZONE: %w", err)
	}
	if c.Runtime.FeedToken == "change-me-before-public-use" {
		return fmt.Errorf("RADAR_FEED_TOKEN must be changed before use")
	}
	seen := map[string]bool{}
	hasFilter := false
	for index, feed := range c.Sources.ICSFeeds {
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
	if hasFilter && len(cleanList(c.Sources.LocationAliases)) == 0 {
		return fmt.Errorf("RADAR_LOCATION_ALIASES is required by a filtered ICS feed")
	}
	if c.Sources.SearXNGURL != "" && len(cleanList(c.Sources.SearXNGQueries)) == 0 {
		return fmt.Errorf("RADAR_SEARXNG_QUERIES is required when RADAR_SEARXNG_URL is set")
	}
	geminiConfigured := c.Sources.GeminiEndpoint != "" || c.Sources.GeminiAPIKey != "" || c.Sources.GeminiToken != ""
	if geminiConfigured {
		if c.Sources.GeminiEndpoint == "" || (c.Sources.GeminiAPIKey == "" && c.Sources.GeminiToken == "") {
			return fmt.Errorf("gemini requires RADAR_GEMINI_ENDPOINT and an API key or token")
		}
		if len(cleanList(c.Sources.GeminiDiscoveryQueries)) == 0 || c.Sources.EventCriteria == "" {
			return fmt.Errorf("RADAR_GEMINI_DISCOVERY_QUERIES and RADAR_EVENT_CRITERIA are required for Gemini")
		}
	}
	if c.Sources.AITinkerersKey != "" && c.Sources.AITinkerersQuery == "" {
		return fmt.Errorf("RADAR_AITINKERERS_QUERY is required when the AI Tinkerers API key is set")
	}
	if len(c.Sources.ICSFeeds) == 0 && c.Sources.SearXNGURL == "" && !geminiConfigured && c.Sources.AITinkerersKey == "" {
		return fmt.Errorf("at least one event source must be configured")
	}
	for term, weight := range c.Sources.RelevanceWeights {
		if strings.TrimSpace(term) == "" || weight <= 0 {
			return fmt.Errorf("RADAR_RELEVANCE_WEIGHTS must contain non-empty terms with positive weights")
		}
	}
	if c.Runtime.OIDCIssuer != "" {
		if err := validateHTTPSOrLocalhost(c.Runtime.OIDCIssuer, "RADAR_OIDC_ISSUER"); err != nil {
			return err
		}
		if c.Runtime.OIDCClientID == "" || c.Runtime.OIDCClientSecret == "" || c.Runtime.OIDCRedirectURL == "" {
			return fmt.Errorf("RADAR_OIDC_ISSUER requires RADAR_OIDC_CLIENT_ID, RADAR_OIDC_CLIENT_SECRET and RADAR_OIDC_REDIRECT_URL")
		}
		if err := validateHTTPSOrLocalhost(c.Runtime.OIDCRedirectURL, "RADAR_OIDC_REDIRECT_URL"); err != nil {
			return err
		}
		if len(c.Runtime.OIDCAllowedEmails) == 0 && len(c.Runtime.OIDCAllowedDomains) == 0 && len(c.Runtime.OIDCAllowedSubs) == 0 {
			return fmt.Errorf("at least one of RADAR_OIDC_ALLOWED_EMAILS, RADAR_OIDC_ALLOWED_DOMAINS or RADAR_OIDC_ALLOWED_SUBS must be set")
		}
	}
	return nil
}

func validateHTTPSOrLocalhost(raw, name string) error {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL", name)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1") {
		return nil
	}
	return fmt.Errorf("%s must use https (plain http is only allowed for localhost/127.0.0.1)", name)
}

// OIDCConfigured reports whether OIDC admin login is configured.
func (c Config) OIDCConfigured() bool { return c.Runtime.OIDCIssuer != "" }

// AuthConfig maps the radar configuration onto the auth package's config.
func (c Config) AuthConfig() auth.Config {
	return auth.Config{
		Issuer:         c.Runtime.OIDCIssuer,
		ClientID:       c.Runtime.OIDCClientID,
		ClientSecret:   c.Runtime.OIDCClientSecret,
		RedirectURL:    c.Runtime.OIDCRedirectURL,
		Scopes:         c.Runtime.OIDCScopes,
		AllowedEmails:  c.Runtime.OIDCAllowedEmails,
		AllowedDomains: c.Runtime.OIDCAllowedDomains,
		AllowedSubs:    c.Runtime.OIDCAllowedSubs,
		TrustProxy:     c.Runtime.TrustProxy,
	}
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

// listEnv reads a comma-separated environment variable and returns the
// trimmed, non-empty entries. Unlike cleanList it does not lowercase, so
// allowlist subjects keep their case.
func listEnv(name string) []string {
	var values []string
	for _, value := range strings.Split(os.Getenv(name), ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// boolEnv reports whether the environment variable is set to exactly "true".
func boolEnv(name string) bool {
	return os.Getenv(name) == "true"
}
