package radar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseICSUnfoldsAndRendersCalendar(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:test-1\r\nDTSTART;TZID=Europe/Berlin:20260917T180000\r\nDTEND;TZID=Europe/Berlin:20260917T220000\r\nSUMMARY:OpenClaw Munich #2\\, Share & Build\r\nDESCRIPTION:One long\\nline\r\nLOCATION:Octopus Energy\\, München\r\nURL:https://luma.com/diatnsu2\r\nSTATUS:CONFIRMED\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	events, err := ParseICS(raw, "openclaw-munich", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Title != "OpenClaw Munich #2, Share & Build" {
		t.Fatalf("title = %q", events[0].Title)
	}
	rendered := RenderICS(Config{AppName: "Event Radar", CalendarProdID: "-//Event Radar//EN", Timezone: "UTC"}, events)
	if !strings.Contains(rendered, "BEGIN:VCALENDAR") || !strings.Contains(rendered, "UID:") {
		t.Fatalf("unexpected calendar: %s", rendered)
	}
}

func TestStoreDeduplicatesByTitleTimeAndLocation(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	startsAt := time.Date(2026, 9, 17, 16, 0, 0, 0, time.UTC)
	first := Event{Source: "one", SourceID: "a", Title: "Event", Location: "Munich", StartsAt: startsAt, EndsAt: startsAt.Add(time.Hour), Status: StatusConfirmed}
	second := first
	second.Source, second.SourceID, second.Description = "two", "b", "updated"
	if err := store.UpsertEvent(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertEvent(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	events, err := store.UpcomingEvents(context.Background(), startsAt.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Description != "updated" {
		t.Fatalf("dedupe failed: %#v", events)
	}
}

func TestCalendarFeedKeepsPastEvents(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	past := Event{Source: "test", SourceID: "past", Title: "Past event", Location: "Munich", StartsAt: now.Add(-48 * time.Hour), EndsAt: now.Add(-46 * time.Hour), Status: StatusConfirmed}
	future := Event{Source: "test", SourceID: "future", Title: "Future event", Location: "Munich", StartsAt: now.Add(48 * time.Hour), EndsAt: now.Add(50 * time.Hour), Status: StatusConfirmed}
	for _, event := range []Event{past, future} {
		if err := store.UpsertEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	upcoming, err := store.UpcomingEvents(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(upcoming) != 1 || upcoming[0].Title != "Future event" {
		t.Fatalf("upcoming = %#v, want only the future event", upcoming)
	}
	all, err := store.AllEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all events = %d, want 2", len(all))
	}
	app := New(Config{AppName: "Event Radar", CalendarProdID: "-//Event Radar//EN", Timezone: "UTC", FeedToken: "test-token"}, store, nil)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/calendar/test-token.ics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("calendar status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "SUMMARY:Past event") || !strings.Contains(body, "SUMMARY:Future event") {
		t.Fatalf("calendar feed dropped history:\n%s", body)
	}
}

func TestDigestHashChangesWithContent(t *testing.T) {
	if DigestHash("one") == DigestHash("two") {
		t.Fatal("digest hashes must differ")
	}
}

func TestSearXNGSourceDeduplicatesResultsAndRejectsFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("format") != "json" {
			t.Error("format was not requested")
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"results": []map[string]string{
			{"title": "Munich AI meetup", "url": "https://example.test/event/#details", "content": "AI developer event"},
			{"title": "same event", "url": "https://EXAMPLE.test/event/", "content": "Munich agents"},
			{"title": "unrelated", "url": "https://example.test/other", "content": "music"},
		}})
	}))
	defer server.Close()
	source := SearXNGSource{endpoint: server.URL, queries: []string{"events"}, weights: map[string]int{"munich": 1, "ai": 1, "developer": 1}, client: server.Client()}
	_, candidates, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want one canonical result", len(candidates))
	}
}

func TestGeminiSourceDiscoversAndVerifiesStructuredCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		tools, _ := payload["tools"].([]any)
		if len(tools) == 0 {
			t.Fatal("missing tools")
		}
		body := map[string]any{}
		if _, ok := tools[0].(map[string]any)["google_search"]; ok {
			body["candidates"] = []map[string]string{{"title": "Munich AI Night", "url": "https://example.test/event", "snippet": "AI meetup in Munich"}}
		} else {
			body = map[string]any{"is_specific_event": true, "title": "Munich AI Night", "start_time": "2026-09-20T18:00:00+02:00", "end_time": "2026-09-20T20:00:00+02:00", "location": "Munich", "event_page_url": "https://example.test/event", "date_evidence": "20 September 2026 18:00", "location_evidence": "Munich", "confidence": "high", "rejection_reason": ""}
		}
		text, _ := json.Marshal(body)
		_ = json.NewEncoder(writer).Encode(map[string]any{"candidates": []map[string]any{{"content": map[string]any{"parts": []map[string]string{{"text": string(text)}}}}}})
	}))
	defer server.Close()
	source := GeminiSource{endpoint: server.URL, token: "test", timeout: time.Second, discoveryQueries: []string{"find events"}, criteria: "AI events in Munich", timezone: "UTC", weights: map[string]int{"munich": 1, "ai": 1}, client: server.Client()}
	_, candidates, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Verification != CandidateVerified || candidates[0].EventTitle != "Munich AI Night" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestCandidateUpsertRefreshesVerificationState(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	candidate := Candidate{Source: "searxng-discovery", URL: "https://example.test/event", Title: "Event", Status: CandidatePending, Verification: "unverified"}
	if err := store.UpsertCandidate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Verification = CandidateVerified
	candidate.EventTitle = "Event"
	candidate.Location = "Munich"
	candidate.StartTime = time.Date(2026, 9, 20, 18, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	if err := store.UpsertCandidate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Candidate(context.Background(), candidate.URL)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Verification != CandidateVerified || stored.EventTitle != "Event" {
		t.Fatalf("stored candidate = %#v", stored)
	}
}

func TestConfigValidationRequiresLocationAliasesForFilteredFeeds(t *testing.T) {
	config := Config{
		DatabasePath: "events.db", FeedToken: "secret", ListenAddress: "127.0.0.1:0",
		SyncInterval: time.Hour, HTTPTimeout: time.Second, Timezone: "UTC",
		ICSFeeds: []ICSFeedConfig{{Name: "feed", URL: "https://example.test/events.ics", FilterLocation: true}},
	}
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "RADAR_LOCATION_ALIASES") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestUnverifiedCandidateRemainsReviewable(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	candidate := Candidate{Source: "discovery", URL: "https://example.test/event", Title: "Event", Verification: CandidateUnverified}
	if err := store.UpsertCandidate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.Candidates(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Verification != CandidateUnverified {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestLoadConfigLoggingDefaults(t *testing.T) {
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.LogFormat != "text" || config.LogLevel != "info" {
		t.Fatalf("logging defaults = %q/%q, want text/info", config.LogFormat, config.LogLevel)
	}
	t.Setenv("RADAR_LOG_FORMAT", "json")
	t.Setenv("RADAR_LOG_LEVEL", "debug")
	config, err = LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.LogFormat != "json" || config.LogLevel != "debug" {
		t.Fatalf("logging config = %q/%q, want json/debug", config.LogFormat, config.LogLevel)
	}
}

func TestValidateLoggingAllowlist(t *testing.T) {
	base := Config{
		DatabasePath: "events.db", FeedToken: "secret", ListenAddress: "127.0.0.1:0",
		SyncInterval: time.Hour, HTTPTimeout: time.Second, Timezone: "UTC",
		ICSFeeds: []ICSFeedConfig{{Name: "feed", URL: "https://example.test/events.ics"}},
	}
	for _, test := range []struct {
		name                   string
		format, level, tracing string
		want                   string
	}{
		{name: "defaults"},
		{name: "json debug", format: "json", level: "debug"},
		{name: "text warn", format: "text", level: "warn"},
		{name: "empty format", format: ""},
		{name: "empty level", level: ""},
		{name: "tracing enabled", tracing: "true"},
		{name: "bad format", format: "yaml", want: "RADAR_LOG_FORMAT"},
		{name: "bad level", level: "verbose", want: "RADAR_LOG_LEVEL"},
		{name: "bad tracing", tracing: "yes", want: "RADAR_TRACING_ENABLED"},
	} {
		config := base
		config.LogFormat = test.format
		config.LogLevel = test.level
		config.TracingEnabled = test.tracing
		err := config.Validate()
		if test.want == "" {
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", test.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: validation error = %v", test.name, err)
		}
	}
}
