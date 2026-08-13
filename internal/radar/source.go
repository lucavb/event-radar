package radar

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Source interface {
	Name() string
	Enabled() bool
	Fetch(context.Context) ([]Event, []Candidate, error)
}

type ICSFeedSource struct {
	name           string
	endpoint       string
	anchor         bool
	matchMunich    bool
	forceConfirmed bool
	client         *http.Client
}

func (s ICSFeedSource) Name() string  { return s.name }
func (s ICSFeedSource) Enabled() bool { return true }

func (s ICSFeedSource) Fetch(ctx context.Context) ([]Event, []Candidate, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("User-Agent", "munich-events/0.1 (+local personal event radar)")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, nil, fmt.Errorf("GET %s: %s", s.endpoint, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 5<<20))
	if err != nil {
		return nil, nil, err
	}
	events, err := ParseICS(string(body), s.name, s.anchor)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", s.name, err)
	}
	if s.matchMunich {
		events = filterMunich(events)
	}
	if s.forceConfirmed {
		for index := range events {
			events[index].Status = StatusConfirmed
		}
	}
	return events, nil, nil
}

type SearXNGSource struct {
	endpoint string
	client   *http.Client
}

const (
	searxngRequestTimeout = 10 * time.Second
	searxngMaxResults     = 40
)

func (s SearXNGSource) Name() string  { return "searxng-discovery" }
func (s SearXNGSource) Enabled() bool { return s.endpoint != "" }

func (s SearXNGSource) Fetch(ctx context.Context) ([]Event, []Candidate, error) {
	if !s.Enabled() {
		return nil, nil, nil
	}
	queries := []string{
		"Munich AI meetup event 2026",
		"München KI Meetup Veranstaltung 2026",
		"Munich AI agents developer meetup September 2026",
		"site:luma.com Munich AI meetup 2026",
	}
	seen := map[string]bool{}
	var candidates []Candidate
	var failures []string
	for _, rawQuery := range queries {
		target, err := url.Parse(s.endpoint + "/search")
		if err != nil {
			return nil, nil, err
		}
		query := target.Query()
		query.Set("q", rawQuery)
		query.Set("format", "json")
		query.Set("language", "all")
		query.Set("categories", "general")
		target.RawQuery = query.Encode()
		requestCtx, requestCancel := context.WithTimeout(ctx, searxngRequestTimeout)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
		if err != nil {
			requestCancel()
			return nil, nil, err
		}
		request.Header.Set("User-Agent", "munich-events/0.1 (+local personal event radar)")
		response, err := s.client.Do(request)
		if err != nil {
			requestCancel()
			failures = append(failures, err.Error())
			continue
		}
		var payload struct {
			Results []struct {
				Title, URL, Content string `json:",omitempty"`
			} `json:"results"`
			Unresponsive [][]string `json:"unresponsive_engines"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 5<<20)).Decode(&payload)
		response.Body.Close()
		requestCancel()
		if response.StatusCode < 200 || response.StatusCode > 299 {
			failures = append(failures, response.Status)
			continue
		}
		if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
			failures = append(failures, "unexpected content type "+response.Header.Get("Content-Type"))
			continue
		}
		if decodeErr != nil {
			failures = append(failures, decodeErr.Error())
			continue
		}
		for _, engine := range payload.Unresponsive {
			if len(engine) > 0 {
				failures = append(failures, strings.Join(engine, ": "))
			}
		}
		for _, result := range payload.Results {
			result.URL = canonicalURL(result.URL)
			score := relevanceScore(result.Title + " " + result.Content)
			if score < 2 || result.URL == "" || seen[result.URL] {
				continue
			}
			seen[result.URL] = true
			candidates = append(candidates, Candidate{Source: s.Name(), Title: result.Title, URL: result.URL, Snippet: result.Content, Score: score, Discovered: time.Now().UTC()})
			if len(candidates) >= searxngMaxResults {
				return nil, candidates, nil
			}
		}
	}
	if len(candidates) == 0 && len(failures) > 0 {
		return nil, nil, fmt.Errorf("SearXNG discovery degraded: %s", strings.Join(failures, "; "))
	}
	return nil, candidates, nil
}

type DisabledAIThinkererSource struct{}

func (DisabledAIThinkererSource) Name() string  { return "ai-tinkerers-munich" }
func (DisabledAIThinkererSource) Enabled() bool { return false }
func (DisabledAIThinkererSource) Fetch(context.Context) ([]Event, []Candidate, error) {
	return nil, nil, nil
}

type AIThinkererSource struct {
	apiKey string
	client *http.Client
}

func (s AIThinkererSource) Name() string  { return "ai-tinkerers-munich" }
func (s AIThinkererSource) Enabled() bool { return s.apiKey != "" }

func (s AIThinkererSource) Fetch(ctx context.Context) ([]Event, []Candidate, error) {
	target, _ := url.Parse("https://aitinkerers.org/api/agents/v1/meetups/search")
	query := target.Query()
	query.Set("query", "Munich")
	query.Set("status", "upcoming")
	query.Set("limit", "100")
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+s.apiKey)
	response, err := s.client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, nil, fmt.Errorf("AI Tinkerers API: %s", response.Status)
	}

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 5<<20)).Decode(&envelope); err != nil {
		return nil, nil, err
	}
	var records []map[string]any
	if err := json.Unmarshal(envelope.Data, &records); err != nil {
		var wrapped struct {
			Events  []map[string]any `json:"events"`
			Meetups []map[string]any `json:"meetups"`
		}
		if wrapErr := json.Unmarshal(envelope.Data, &wrapped); wrapErr != nil {
			return nil, nil, fmt.Errorf("AI Tinkerers API payload: %w", err)
		}
		records = append(wrapped.Events, wrapped.Meetups...)
	}
	events := make([]Event, 0, len(records))
	for _, record := range records {
		event, ok := aiThinkererEvent(record)
		if ok {
			events = append(events, event)
		}
	}
	return events, nil, nil
}

func aiThinkererEvent(record map[string]any) (Event, bool) {
	title, _ := firstString(record, "title", "name")
	startRaw, _ := firstString(record, "starts_at", "start_at", "start_date", "startsAt")
	if title == "" || startRaw == "" {
		return Event{}, false
	}
	startsAt, err := time.Parse(time.RFC3339, startRaw)
	if err != nil {
		return Event{}, false
	}
	endsAt := startsAt.Add(2 * time.Hour)
	if endRaw, ok := firstString(record, "ends_at", "end_at", "endsAt"); ok {
		if parsed, parseErr := time.Parse(time.RFC3339, endRaw); parseErr == nil {
			endsAt = parsed
		}
	}
	sourceID, _ := firstString(record, "token", "id", "uuid")
	eventURL, _ := firstString(record, "url", "public_url")
	location, _ := firstString(record, "location_name", "location", "venue")
	event := Event{
		Source: "ai-tinkerers-munich", SourceID: sourceID, Title: title, Location: location,
		URL: eventURL, StartsAt: startsAt, EndsAt: endsAt, Status: StatusConfirmed, Anchor: true, Score: 100,
	}
	event.UID = EventUID(event.Source, event.SourceID, event.Title, event.StartsAt)
	return event, true
}

func firstString(record map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		value, ok := record[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return typed, true
			}
		case map[string]any:
			if name, ok := typed["name"].(string); ok && strings.TrimSpace(name) != "" {
				return name, true
			}
		}
	}
	return "", false
}

func DefaultSources(config Config) []Source {
	client := &http.Client{Timeout: config.HTTPTimeout}
	sources := []Source{
		ICSFeedSource{name: "openclaw-munich", endpoint: config.OpenClawICS, anchor: true, forceConfirmed: true, client: client},
		ICSFeedSource{name: "claude-code-munich", endpoint: config.ClaudeICS, anchor: true, matchMunich: true, client: client},
		ICSFeedSource{name: "ai-agents-munich", endpoint: config.AIAgentsICS, anchor: true, client: client},
		ICSFeedSource{name: "munich-ai-developers-group", endpoint: config.AIDevICS, anchor: true, client: client},
	}
	if config.AIThinkererKey == "" {
		sources = append(sources, DisabledAIThinkererSource{})
	} else {
		sources = append(sources, AIThinkererSource{apiKey: config.AIThinkererKey, client: client})
	}
	if config.SearXNGURL != "" {
		sources = append(sources, SearXNGSource{endpoint: config.SearXNGURL, client: client})
	}
	if config.GeminiEndpoint != "" {
		sources = append(sources, GeminiSource{
			endpoint: config.GeminiEndpoint, model: config.GeminiModel,
			apiKey: config.GeminiAPIKey, token: config.GeminiToken,
			timeout: config.GeminiTimeout, client: &http.Client{Timeout: config.GeminiTimeout},
		})
	}
	return sources
}

func ParseICS(raw, source string, anchor bool) ([]Event, error) {
	lines := unfoldICS(raw)
	var events []Event
	var component map[string]string
	for _, line := range lines {
		switch line {
		case "BEGIN:VEVENT":
			component = map[string]string{}
		case "END:VEVENT":
			if component == nil {
				continue
			}
			event, err := eventFromICS(component, source, anchor)
			if err != nil {
				return nil, err
			}
			if event.Title != "" && !event.StartsAt.IsZero() {
				events = append(events, event)
			}
			component = nil
		default:
			if component == nil {
				continue
			}
			key, value, ok := splitICSLine(line)
			if ok {
				component[key] = unescapeICS(value)
			}
		}
	}
	return events, nil
}

func unfoldICS(raw string) []string {
	scanner := bufio.NewScanner(strings.NewReader(strings.ReplaceAll(raw, "\r\n", "\n")))
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if len(lines) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			lines[len(lines)-1] += strings.TrimLeft(line, " \t")
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func splitICSLine(line string) (string, string, bool) {
	index := strings.IndexByte(line, ':')
	if index < 0 {
		return "", "", false
	}
	rawKey := strings.ToUpper(line[:index])
	key := rawKey
	if parameterIndex := strings.IndexByte(key, ';'); parameterIndex >= 0 {
		key = key[:parameterIndex]
	}
	value := line[index+1:]
	if (key == "DTSTART" || key == "DTEND") && strings.Contains(rawKey, "TZID=") {
		for _, parameter := range strings.Split(rawKey, ";")[1:] {
			if zone, ok := strings.CutPrefix(parameter, "TZID="); ok {
				value = "[TZID=" + zone + "]" + value
				break
			}
		}
	}
	return key, value, true
}

func eventFromICS(component map[string]string, source string, anchor bool) (Event, error) {
	startsAt, err := parseICSDate(component["DTSTART"])
	if err != nil {
		return Event{}, err
	}
	endsAt, err := parseICSDate(component["DTEND"])
	if err != nil {
		endsAt = startsAt.Add(2 * time.Hour)
	}
	location := component["LOCATION"]
	eventURL := component["URL"]
	if eventURL == "" && strings.HasPrefix(location, "https://") {
		eventURL = location
	}
	sourceID := component["UID"]
	if sourceID == "" {
		sourceID = eventURL
	}
	status := StatusConfirmed
	if strings.EqualFold(component["STATUS"], string(StatusTentative)) {
		status = StatusTentative
	}
	event := Event{Source: source, SourceID: sourceID, Title: component["SUMMARY"], Description: component["DESCRIPTION"], Location: location, URL: eventURL, StartsAt: startsAt, EndsAt: endsAt, Status: status, Anchor: anchor, Score: 100}
	event.UID = EventUID(source, sourceID, event.Title, startsAt)
	return event, nil
}

func parseICSDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	location := time.UTC
	if strings.HasPrefix(value, "[TZID=") {
		end := strings.Index(value, "]")
		if end < 0 {
			return time.Time{}, fmt.Errorf("invalid TZID value %q", value)
		}
		zone, err := time.LoadLocation(value[len("[TZID="):end])
		if err != nil {
			return time.Time{}, err
		}
		location, value = zone, value[end+1:]
	}
	for _, layout := range []string{"20060102T150405Z", "20060102T150400Z", "20060102T150405", "20060102T150400", "20060102"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid iCalendar timestamp %q", value)
}

func filterMunich(events []Event) []Event {
	filtered := make([]Event, 0, len(events))
	for _, event := range events {
		text := strings.ToLower(event.Title + " " + event.Location + " " + event.Description)
		if strings.Contains(text, "munich") || strings.Contains(text, "münchen") {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func unescapeICS(value string) string {
	replacer := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return replacer.Replace(value)
}

var wordPattern = regexp.MustCompile(`[[:alnum:]]+`)

func relevanceScore(text string) int {
	terms := map[string]int{"ai": 2, "agent": 2, "claude": 2, "llm": 2, "developer": 1, "coding": 1, "machine": 1, "munich": 1, "münchen": 1}
	score := 0
	for _, word := range wordPattern.FindAllString(strings.ToLower(text), -1) {
		score += terms[word]
	}
	return score
}

func parsePort(value string) (int, error) { return strconv.Atoi(value) }
