package radar

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type stubSource struct {
	name       string
	enabled    bool
	events     []Event
	candidates []Candidate
	err        error
	fetchCalls int
}

func (s *stubSource) Name() string  { return s.name }
func (s *stubSource) Enabled() bool { return s.enabled }

func (s *stubSource) Fetch(ctx context.Context) ([]Event, []Candidate, error) {
	s.fetchCalls++
	return s.events, s.candidates, s.err
}

// fakeStore is an in-memory Store implementing only the pipeline seam.
type fakeStore struct {
	health             []SourceHealth
	events             []Event
	candidates         []Candidate
	updates            []Candidate
	saveHealthErr      error
	upsertEventErr     error
	updateCandidateErr error
}

func (s *fakeStore) PruneCandidates(ctx context.Context, now time.Time) error { return nil }

func (s *fakeStore) SaveSourceHealth(ctx context.Context, health SourceHealth) error {
	if s.saveHealthErr != nil {
		return s.saveHealthErr
	}
	s.health = append(s.health, health)
	return nil
}

func (s *fakeStore) SourceHealth(ctx context.Context) ([]SourceHealth, error) {
	return s.health, nil
}

func (s *fakeStore) UpsertEvent(ctx context.Context, event Event) error {
	if s.upsertEventErr != nil {
		return s.upsertEventErr
	}
	s.events = append(s.events, event)
	return nil
}

func (s *fakeStore) UpcomingEvents(ctx context.Context, from time.Time) ([]Event, error) {
	return s.events, nil
}

func (s *fakeStore) AllEvents(ctx context.Context) ([]Event, error) { return s.events, nil }

func (s *fakeStore) UpsertCandidate(ctx context.Context, candidate Candidate) error {
	if candidate.Status == "" {
		candidate.Status = CandidatePending
	}
	if candidate.Verification == "" {
		candidate.Verification = CandidateUnverified
	}
	s.candidates = append(s.candidates, candidate)
	return nil
}

func (s *fakeStore) Candidates(ctx context.Context, includeRejected bool) ([]Candidate, error) {
	return s.candidates, nil
}

func (s *fakeStore) Candidate(ctx context.Context, rawURL string) (Candidate, error) {
	for _, candidate := range s.candidates {
		if candidate.URL == rawURL {
			return candidate, nil
		}
	}
	return Candidate{}, errors.New("not found")
}

func (s *fakeStore) UpdateCandidate(ctx context.Context, candidate Candidate) error {
	if s.updateCandidateErr != nil {
		return s.updateCandidateErr
	}
	s.updates = append(s.updates, candidate)
	return nil
}

func (s *fakeStore) CandidateCounts(ctx context.Context) (map[string]int, error) {
	return map[string]int{CandidatePending: len(s.candidates)}, nil
}

// fakeVerifier records calls and returns a canned verified candidate.
type fakeVerifier struct {
	calls []Candidate
}

func (v *fakeVerifier) VerifyCandidate(ctx context.Context, candidate Candidate) Candidate {
	v.calls = append(v.calls, candidate)
	verified := candidate
	verified.Verification = CandidateVerified
	verified.EventTitle = "Verified " + candidate.Title
	verified.Confidence = "high"
	return verified
}

func TestPipelineSkipsDisabledSource(t *testing.T) {
	store := &fakeStore{}
	source := &stubSource{name: "ics-test", enabled: false}
	app := New(Config{}, store, []Source{source}, nil)
	if err := app.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.fetchCalls != 0 {
		t.Fatalf("Fetch called %d times for disabled source", source.fetchCalls)
	}
	if len(store.health) != 1 || store.health[0].State != "disabled" || store.health[0].Name != "ics-test" {
		t.Fatalf("health = %#v", store.health)
	}
	if len(store.events) != 0 || len(store.candidates) != 0 {
		t.Fatalf("disabled source stored data: events=%d candidates=%d", len(store.events), len(store.candidates))
	}
}

func TestPipelineSourceErrorFailsSync(t *testing.T) {
	store := &fakeStore{}
	source := &stubSource{name: "ics-test", enabled: true, err: errors.New("fetch failed")}
	app := New(Config{}, store, []Source{source}, nil)
	err := app.Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ics-test") {
		t.Fatalf("expected aggregated sync error, got %v", err)
	}
	if len(store.health) != 1 || store.health[0].State != "error" {
		t.Fatalf("health = %#v", store.health)
	}
}

func TestPipelineDiscoveryErrorIsDegradedNotFailure(t *testing.T) {
	store := &fakeStore{}
	source := &stubSource{name: "discovery-test", enabled: true, err: errors.New("search failed")}
	app := New(Config{}, store, []Source{source}, nil)
	if err := app.Sync(context.Background()); err != nil {
		t.Fatalf("discovery failure must not fail Sync: %v", err)
	}
	if len(store.health) != 1 || store.health[0].State != "degraded" {
		t.Fatalf("health = %#v", store.health)
	}
}

func TestPipelineHealthySourceStoresEventsAndHealth(t *testing.T) {
	store := &fakeStore{}
	startsAt := time.Now().UTC().Add(24 * time.Hour)
	source := &stubSource{name: "ics-test", enabled: true, events: []Event{
		{Source: "ics-test", SourceID: "1", Title: "Meetup", StartsAt: startsAt, EndsAt: startsAt.Add(time.Hour), Status: StatusConfirmed},
	}}
	app := New(Config{}, store, []Source{source}, nil)
	if err := app.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 1 || store.events[0].Title != "Meetup" {
		t.Fatalf("events = %#v", store.events)
	}
	if len(store.health) != 1 || store.health[0].State != "healthy" || store.health[0].LastSuccess == nil {
		t.Fatalf("health = %#v", store.health)
	}
}

func TestPipelineNilVerifierStoresUnverified(t *testing.T) {
	store := &fakeStore{}
	source := &stubSource{name: "discovery-test", enabled: true, candidates: []Candidate{
		{Source: "discovery-test", URL: "https://example.test/a", Title: "Event A"},
	}}
	app := New(Config{}, store, []Source{source}, nil)
	if err := app.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.candidates) != 1 || store.candidates[0].Verification != CandidateUnverified {
		t.Fatalf("candidates = %#v", store.candidates)
	}
}

func TestPipelineVerifierRunsSkipsAndTruncates(t *testing.T) {
	store := &fakeStore{}
	verifier := &fakeVerifier{}
	var candidates []Candidate
	preScheduled := Candidate{Source: "discovery-test", URL: "https://example.test/scheduled", Title: "Pre-scheduled"}
	preScheduled.StartTime = time.Now().UTC().Add(48 * time.Hour)
	candidates = append(candidates, preScheduled)
	for i := 0; i < 9; i++ {
		candidates = append(candidates, Candidate{Source: "discovery-test", URL: string(rune('a'+i)) + "https://example.test/" + string(rune('a'+i)), Title: "Event " + string(rune('a'+i))})
	}
	source := &stubSource{name: "discovery-test", enabled: true, candidates: candidates}
	app := New(Config{}, store, []Source{source}, verifier)
	if err := app.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(verifier.calls) != 7 {
		t.Fatalf("verifier calls = %d, want 7 (batch truncated to 8, pre-scheduled skipped)", len(verifier.calls))
	}
	for _, call := range verifier.calls {
		if !call.StartTime.IsZero() {
			t.Fatalf("verifier called on pre-scheduled candidate: %#v", call)
		}
	}
	if len(store.candidates) != 8 {
		t.Fatalf("stored candidates = %d, want 8", len(store.candidates))
	}
	if store.candidates[0].Verification != CandidateUnverified {
		t.Fatalf("pre-scheduled candidate must stay unverified: %#v", store.candidates[0])
	}
	for _, stored := range store.candidates[1:] {
		if stored.Verification != CandidateVerified || stored.EventTitle == "" {
			t.Fatalf("candidate not verified: %#v", stored)
		}
	}
}

func TestPipelineStoreErrorsAggregate(t *testing.T) {
	t.Run("SaveSourceHealth error", func(t *testing.T) {
		store := &fakeStore{saveHealthErr: errors.New("health write failed")}
		source := &stubSource{name: "ics-test", enabled: true}
		app := New(Config{}, store, []Source{source}, nil)
		if err := app.Sync(context.Background()); err == nil {
			t.Fatal("expected aggregated error from SaveSourceHealth failure")
		}
	})
	t.Run("UpsertEvent error", func(t *testing.T) {
		store := &fakeStore{upsertEventErr: errors.New("event write failed")}
		startsAt := time.Now().UTC().Add(24 * time.Hour)
		source := &stubSource{name: "ics-test", enabled: true, events: []Event{
			{Source: "ics-test", SourceID: "1", Title: "Meetup", StartsAt: startsAt, Status: StatusConfirmed},
		}}
		app := New(Config{}, store, []Source{source}, nil)
		err := app.Sync(context.Background())
		if err == nil {
			t.Fatal("expected aggregated error from UpsertEvent failure")
		}
	})
}
