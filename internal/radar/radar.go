package radar

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const (
	maxGeminiCandidates           = 8
	geminiVerificationConcurrency = 3
)

type Radar struct {
	config   Config
	store    Store
	verifier Verifier
	sources  []Source
	mu       sync.Mutex
}

func New(config Config, store Store, sources []Source, verifier Verifier) *Radar {
	return &Radar{config: config, store: store, sources: sources, verifier: verifier}
}

func (r *Radar) Sync(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	tracer := otel.Tracer("event-radar")
	syncCtx, span := tracer.Start(ctx, "radar.sync")
	defer span.End()
	err := r.runSync(syncCtx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (r *Radar) runSync(ctx context.Context) error {
	if err := r.store.PruneCandidates(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("prune candidates: %w", err)
	}
	tracer := otel.Tracer("event-radar")
	var failures []error
	for _, source := range r.sources {
		health := SourceHealth{Name: source.Name(), Enabled: source.Enabled()}
		if !source.Enabled() {
			health.State = "disabled"
			health.LastError = "source disabled"
			if err := r.store.SaveSourceHealth(ctx, health); err != nil {
				failures = append(failures, err)
			}
			continue
		}

		fetchCtx, fetchSpan := tracer.Start(ctx, "source.fetch")
		fetchSpan.SetAttributes(attribute.String("source.name", source.Name()))
		events, candidates, err := source.Fetch(fetchCtx)
		if err != nil {
			fetchSpan.RecordError(err)
			fetchSpan.SetStatus(codes.Error, err.Error())
		}
		fetchSpan.End()
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
		candidates = verifyCandidates(ctx, r.verifier, candidates)
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

// verifyCandidates is the pipeline's single verification fan-out. It caps the
// batch, skips candidates that already carry a start time, and verifies the
// rest under a bounded concurrency.
func verifyCandidates(ctx context.Context, verifier Verifier, candidates []Candidate) []Candidate {
	if verifier == nil {
		return candidates
	}
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

func (r *Radar) UpcomingEvents(ctx context.Context) ([]Event, error) {
	return r.store.UpcomingEvents(ctx, time.Now().UTC())
}

// AllEvents returns the full event history for the calendar feed.
func (r *Radar) AllEvents(ctx context.Context) ([]Event, error) {
	return r.store.AllEvents(ctx)
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
	ticker := time.NewTicker(r.config.Runtime.SyncInterval)
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
