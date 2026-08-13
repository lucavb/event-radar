package radar

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Radar struct {
	config  Config
	store   *Store
	sources []Source
	mu      sync.Mutex
}

func New(config Config, store *Store, sources []Source) *Radar {
	return &Radar{config: config, store: store, sources: sources}
}

func (r *Radar) Sync(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seedTentativeClaude(ctx)
	if err := r.store.PruneCandidates(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("prune candidates: %w", err)
	}
	var failures []error
	var verifier *GeminiSource
	for _, source := range r.sources {
		if gemini, ok := source.(GeminiSource); ok && gemini.Enabled() {
			copy := gemini
			verifier = &copy
			break
		}
	}
	for _, source := range r.sources {
		health := SourceHealth{Name: source.Name(), Enabled: source.Enabled()}
		if !source.Enabled() {
			health.State = "disabled_pending_attendance"
			health.LastError = "AI Tinkerers requires attendance within the last 90 days before API access is enabled"
			if err := r.store.SaveSourceHealth(ctx, health); err != nil {
				failures = append(failures, err)
			}
			continue
		}

		events, candidates, err := source.Fetch(ctx)
		if err != nil {
			health.State, health.LastError = "error", err.Error()
			if strings.Contains(source.Name(), "discovery") {
				health.State = "degraded"
			}
			if saveErr := r.store.SaveSourceHealth(ctx, health); saveErr != nil {
				failures = append(failures, saveErr)
			}
			if health.State == "error" {
				failures = append(failures, fmt.Errorf("%s: %w", source.Name(), err))
			}
			continue
		}
		now := time.Now().UTC()
		health.State, health.LastSuccess = "healthy", &now
		if err := r.store.SaveSourceHealth(ctx, health); err != nil {
			failures = append(failures, err)
			continue
		}
		for _, event := range events {
			if err := r.store.UpsertEvent(ctx, event); err != nil {
				failures = append(failures, fmt.Errorf("%s event %q: %w", source.Name(), event.Title, err))
			}
		}
		if verifier != nil && source.Name() != verifier.Name() {
			candidates = verifyCandidates(ctx, *verifier, candidates)
		}
		candidates = publishableCandidates(candidates, time.Now().UTC())
		for _, candidate := range candidates {
			if err := r.store.UpsertCandidate(ctx, candidate); err != nil {
				failures = append(failures, fmt.Errorf("%s candidate %q: %w", source.Name(), candidate.Title, err))
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("sync completed with %d failures: %v", len(failures), failures)
	}
	return nil
}

func verifyCandidates(ctx context.Context, verifier GeminiSource, candidates []Candidate) []Candidate {
	if len(candidates) > maxGeminiCandidates {
		candidates = candidates[:maxGeminiCandidates]
	}
	sem := make(chan struct{}, geminiVerificationConcurrency)
	var waitGroup sync.WaitGroup
	for index := range candidates {
		if !candidates[index].StartTime.IsZero() {
			continue
		}
		sem <- struct{}{}
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			defer func() { <-sem }()
			candidates[index] = verifier.VerifyCandidate(ctx, candidates[index])
		}(index)
	}
	waitGroup.Wait()
	return candidates
}

func publishableCandidates(candidates []Candidate, now time.Time) []Candidate {
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if candidate.Verification != CandidateVerified ||
			candidate.EventTitle == "" ||
			candidate.StartTime.IsZero() ||
			!candidate.StartTime.After(now) ||
			candidate.Location == "" ||
			candidate.EvidenceURL == "" ||
			candidate.DateEvidence == "" ||
			candidate.LocationEvidence == "" {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func (r *Radar) seedTentativeClaude(ctx context.Context) {
	starts := time.Date(2026, time.September, 21, 18, 0, 0, 0, time.FixedZone("Europe/Berlin", 2*3600))
	event := Event{
		Source: "claude-code-munich-seed", SourceID: "claude-code-medien-2026-09-21",
		Title: "Claude Code für Medien", Location: "KI.M, Balanstraße 73, Haus 11, München",
		URL: "https://claudecode-muenchen.de/", StartsAt: starts, EndsAt: starts.Add(3 * time.Hour),
		Status: StatusTentative, Anchor: true, Score: 100,
	}
	_ = r.store.UpsertEvent(ctx, event)
}

func (r *Radar) Events(ctx context.Context) ([]Event, error) {
	return r.store.UpcomingEvents(ctx, time.Now().UTC())
}

func (r *Radar) Health(ctx context.Context) ([]SourceHealth, error) { return r.store.SourceHealth(ctx) }

func (r *Radar) Candidates(ctx context.Context, includeRejected bool) ([]Candidate, error) {
	return r.store.Candidates(ctx, includeRejected)
}

func (r *Radar) Candidate(ctx context.Context, rawURL string) (Candidate, error) {
	return r.store.Candidate(ctx, rawURL)
}

func (r *Radar) UpdateCandidate(ctx context.Context, candidate Candidate) error {
	return r.store.UpdateCandidate(ctx, candidate)
}

func (r *Radar) CandidateCounts(ctx context.Context) (map[string]int, error) {
	return r.store.CandidateCounts(ctx)
}

func (r *Radar) Run(ctx context.Context) {
	ticker := time.NewTicker(r.config.SyncInterval)
	defer ticker.Stop()
	_ = r.Sync(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.Sync(ctx)
		}
	}
}
