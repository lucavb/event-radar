package radar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type GeminiSource struct {
	endpoint         string
	apiKey           string
	token            string
	timeout          time.Duration
	discoveryQueries []string
	criteria         string
	timezone         string
	weights          map[string]int
	client           *http.Client
}

func (s GeminiSource) Name() string  { return "gemini-discovery" }
func (s GeminiSource) Enabled() bool { return s.endpoint != "" && (s.apiKey != "" || s.token != "") }

func (s GeminiSource) Fetch(ctx context.Context) ([]Event, []Candidate, error) {
	if !s.Enabled() {
		return nil, nil, nil
	}
	// Keep a source-wide budget, while giving each upstream call its own
	// timeout. Verification is bounded and concurrent so one slow page cannot
	// consume the entire sync deadline.
	ctx, cancel := context.WithTimeout(ctx, 2*s.timeout+time.Minute)
	defer cancel()
	var candidates []Candidate
	for _, query := range s.discoveryQueries {
		requestCtx, requestCancel := context.WithTimeout(ctx, s.timeout)
		found, err := s.discover(requestCtx, query)
		requestCancel()
		if err != nil {
			return nil, candidates, err
		}
		candidates = append(candidates, found...)
	}
	seen := map[string]bool{}
	unique := candidates[:0]
	for _, candidate := range candidates {
		candidate.URL = canonicalURL(candidate.URL)
		if candidate.URL == "" || seen[candidate.URL] {
			continue
		}
		seen[candidate.URL] = true
		unique = append(unique, candidate)
	}
	if len(unique) > maxGeminiCandidates {
		unique = unique[:maxGeminiCandidates]
	}
	results := make([]Candidate, len(unique))
	copy(results, unique)
	sem := make(chan struct{}, geminiVerificationConcurrency)
	var waitGroup sync.WaitGroup
	for index := range results {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			requestCtx, requestCancel := context.WithTimeout(ctx, s.timeout)
			defer requestCancel()
			verified, err := s.verify(requestCtx, results[index])
			if err != nil {
				results[index].Verification = "failed"
				results[index].LastError = err.Error()
				return
			}
			results[index] = verified
		}(index)
	}
	waitGroup.Wait()
	return nil, results, nil
}

// VerifyCandidate enriches a candidate discovered by another provider with a
// date, venue, and evidence from its exact event page.
func (s GeminiSource) VerifyCandidate(ctx context.Context, candidate Candidate) Candidate {
	requestCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	verified, err := s.verify(requestCtx, candidate)
	if err != nil {
		candidate.Verification = CandidateFailed
		candidate.LastError = err.Error()
		return candidate
	}
	return verified
}

const (
	maxGeminiCandidates           = 8
	geminiVerificationConcurrency = 3
)

type geminiEnvelope struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func (s GeminiSource) request(ctx context.Context, prompt string, schema map[string]any, tools []map[string]any) (map[string]any, error) {
	payload := map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": prompt}}}},
		"tools":    tools,
		"generationConfig": map[string]any{
			"responseMimeType":   "application/json",
			"responseJsonSchema": schema,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		request.Header.Set("Authorization", "Bearer "+s.token)
	} else if s.apiKey != "" {
		request.Header.Set("x-goog-api-key", s.apiKey)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("Gemini search: %s", response.Status)
	}
	var envelope geminiEnvelope
	if err := json.NewDecoder(io.LimitReader(response.Body, 5<<20)).Decode(&envelope); err != nil {
		return nil, err
	}
	if len(envelope.Candidates) == 0 || len(envelope.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("Gemini search returned no candidate text")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(structuredJSON(envelope.Candidates[0].Content.Parts[0].Text)), &result); err != nil {
		return nil, fmt.Errorf("Gemini structured response: %w", err)
	}
	return result, nil
}

func structuredJSON(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "```json")
	value = strings.TrimPrefix(value, "```")
	value = strings.TrimSuffix(value, "```")
	value = strings.TrimSpace(value)
	start := strings.IndexByte(value, '{')
	end := strings.LastIndexByte(value, '}')
	if start >= 0 && end > start {
		return value[start : end+1]
	}
	return value
}

func (s GeminiSource) discover(ctx context.Context, prompt string) ([]Candidate, error) {
	itemSchema := map[string]any{
		"type": "object", "properties": map[string]any{
			"title":   map[string]any{"type": "string"},
			"url":     map[string]any{"type": "string"},
			"snippet": map[string]any{"type": "string"},
		}, "required": []string{"title", "url", "snippet"},
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"candidates": map[string]any{"type": "array", "items": itemSchema},
	}, "required": []string{"candidates"}}
	result, err := s.request(ctx, prompt, schema, []map[string]any{{"google_search": map[string]any{}}})
	if err != nil {
		return nil, err
	}
	raw, _ := result["candidates"].([]any)
	now := time.Now().UTC()
	output := make([]Candidate, 0, len(raw))
	for _, item := range raw {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := record["title"].(string)
		rawURL, _ := record["url"].(string)
		snippet, _ := record["snippet"].(string)
		if rawURL == "" {
			continue
		}
		score := relevanceScore(title+" "+snippet, s.weights)
		output = append(output, Candidate{Source: s.Name(), Title: title, URL: rawURL, Snippet: snippet, Score: score, Discovered: now})
	}
	return output, nil
}

func (s GeminiSource) verify(ctx context.Context, candidate Candidate) (Candidate, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"is_specific_event": map[string]any{"type": "boolean"},
		"title":             map[string]any{"type": "string"},
		"start_time":        map[string]any{"type": "string"},
		"end_time":          map[string]any{"type": "string"},
		"location":          map[string]any{"type": "string"},
		"event_page_url":    map[string]any{"type": "string"},
		"date_evidence":     map[string]any{"type": "string"},
		"location_evidence": map[string]any{"type": "string"},
		"confidence":        map[string]any{"type": "string"},
		"rejection_reason":  map[string]any{"type": "string"},
	}, "required": []string{"is_specific_event", "title", "start_time", "end_time", "location", "event_page_url", "date_evidence", "location_evidence", "confidence", "rejection_reason"}}
	zone, err := time.LoadLocation(s.timezone)
	if err != nil {
		zone = time.UTC
	}
	prompt := fmt.Sprintf("Verify this exact event page: %s. Today is %s. Accept only one specific future event matching this criteria: %s. Require an explicit date, start time, venue or location, event URL, and short verbatim date and location evidence. Do not infer missing facts.", candidate.URL, time.Now().In(zone).Format("2006-01-02"), s.criteria)
	result, err := s.request(ctx, prompt, schema, []map[string]any{{"url_context": map[string]any{}}})
	if err != nil {
		return candidate, err
	}
	specific, _ := result["is_specific_event"].(bool)
	if !specific {
		reason, _ := result["rejection_reason"].(string)
		return candidate, fmt.Errorf("not a specific event: %s", reason)
	}
	start, err := time.Parse(time.RFC3339, stringValue(result, "start_time"))
	if err != nil {
		return candidate, fmt.Errorf("invalid verified start time: %w", err)
	}
	if !start.After(time.Now().UTC()) {
		return candidate, fmt.Errorf("verified event is not in the future")
	}
	end, _ := time.Parse(time.RFC3339, stringValue(result, "end_time"))
	title := stringValue(result, "title")
	location := stringValue(result, "location")
	eventURL := canonicalURL(stringValue(result, "event_page_url"))
	if title == "" || location == "" || eventURL == "" ||
		stringValue(result, "date_evidence") == "" ||
		stringValue(result, "location_evidence") == "" {
		return candidate, fmt.Errorf("verified event is missing specific date, location, URL, or evidence")
	}
	candidate.EventTitle = title
	candidate.StartTime = start
	candidate.EndTime = end
	candidate.Location = stringValue(result, "location")
	candidate.EvidenceURL = eventURL
	candidate.DateEvidence = stringValue(result, "date_evidence")
	candidate.LocationEvidence = stringValue(result, "location_evidence")
	candidate.Confidence = stringValue(result, "confidence")
	candidate.Verification = CandidateVerified
	now := time.Now().UTC()
	candidate.VerifiedAt = &now
	candidate.LastError = ""
	return candidate, nil
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func canonicalURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Fragment = ""
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	return strings.TrimRight(parsed.String(), "/")
}
