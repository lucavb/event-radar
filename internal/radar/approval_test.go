package radar

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fixedClock makes "in the past" deterministic for approval tests.
var fixedClock = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

// verifiedCandidate is a candidate that passes the publish gate.
func verifiedCandidate() Candidate {
	return Candidate{
		Source:           "gemini-discovery",
		URL:              "https://example.test/event",
		Title:            "AI Night",
		EventTitle:       "Munich AI Night",
		Verification:     CandidateVerified,
		StartTime:        fixedClock.Add(48 * time.Hour),
		Location:         "Munich",
		EvidenceURL:      "https://example.test/event",
		DateEvidence:     "20 September 2026 18:00",
		LocationEvidence: "Munich",
		Score:            7,
	}
}

func storeWithCandidate(t *testing.T, candidate Candidate) *fakeStore {
	t.Helper()
	store := &fakeStore{}
	if err := store.UpsertCandidate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestApprovalGateRejectsEachFailedCondition(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
		mutate   func(candidate *Candidate)
	}{
		{name: "not verified", sentinel: ErrNotVerified, mutate: func(c *Candidate) { c.Verification = CandidateUnverified }},
		{name: "missing verified event title", sentinel: ErrMissingEventTitle, mutate: func(c *Candidate) { c.EventTitle = "" }},
		{name: "missing verified start time", sentinel: ErrMissingStartTime, mutate: func(c *Candidate) { c.StartTime = time.Time{} }},
		{name: "start time in the past", sentinel: ErrInPast, mutate: func(c *Candidate) { c.StartTime = fixedClock.Add(-time.Hour) }},
		{name: "missing verified location", sentinel: ErrMissingLocation, mutate: func(c *Candidate) { c.Location = "" }},
		{name: "missing evidence URL", sentinel: ErrMissingEvidenceURL, mutate: func(c *Candidate) { c.EvidenceURL = "" }},
		{name: "missing date evidence", sentinel: ErrMissingDateEvidence, mutate: func(c *Candidate) { c.DateEvidence = "" }},
		{name: "missing location evidence", sentinel: ErrMissingLocationEvidence, mutate: func(c *Candidate) { c.LocationEvidence = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := verifiedCandidate()
			test.mutate(&candidate)
			store := storeWithCandidate(t, candidate)
			_, err := ApproveCandidate(context.Background(), store, fixedClock, candidate.URL)
			if !errors.Is(err, test.sentinel) {
				t.Fatalf("gate error = %v, want %v", err, test.sentinel)
			}
			if len(store.publishes) != 0 {
				t.Fatalf("failed gate must not publish: %#v", store.publishes)
			}
		})
	}
}

func TestApprovalPublishesTentativeUnanchoredEvent(t *testing.T) {
	store := storeWithCandidate(t, verifiedCandidate())
	approved, err := ApproveCandidate(context.Background(), store, fixedClock, "https://example.test/event")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != CandidateApproved {
		t.Fatalf("candidate status = %q, want approved", approved.Status)
	}
	if approved.ReviewedAt == nil || !approved.ReviewedAt.Equal(fixedClock) {
		t.Fatalf("reviewed at = %v, want fixed clock %v", approved.ReviewedAt, fixedClock)
	}
	if len(store.publishes) != 1 {
		t.Fatalf("published events = %d, want 1", len(store.publishes))
	}
	event := store.publishes[0].event
	if event.Status != StatusTentative {
		t.Fatalf("event status = %q, want tentative", event.Status)
	}
	if event.Anchor {
		t.Fatal("approved candidate must publish a non-anchored event")
	}
	if event.Source != "reviewed-gemini-discovery" || event.SourceID != "https://example.test/event" {
		t.Fatalf("event source mapping = %#v", event)
	}
	if event.Title != "Munich AI Night" || event.URL != "https://example.test/event" {
		t.Fatalf("event title/url mapping = %#v", event)
	}
	if !event.StartsAt.Equal(fixedClock.Add(48 * time.Hour)) {
		t.Fatalf("event start time = %v, want %v", event.StartsAt, fixedClock.Add(48*time.Hour))
	}
	if event.Location != "Munich" || event.Score != 7 {
		t.Fatalf("event location/score mapping = %#v", event)
	}
	if event.EndsAt.IsZero() {
		t.Fatal("event end time must default to two hours after start")
	}
	if !event.EndsAt.Equal(event.StartsAt.Add(2 * time.Hour)) {
		t.Fatalf("event end time = %v, want start plus two hours", event.EndsAt)
	}
	if store.publishes[0].candidate.Status != CandidateApproved {
		t.Fatalf("approval not published with the candidate: %#v", store.publishes[0].candidate)
	}
	if len(store.events) != 0 || len(store.updates) != 0 {
		t.Fatalf("approval must go through the single atomic publish, got events=%d updates=%d", len(store.events), len(store.updates))
	}
}

func TestApprovalUsesProvidedEndTime(t *testing.T) {
	candidate := verifiedCandidate()
	candidate.EndTime = candidate.StartTime.Add(90 * time.Minute)
	store := storeWithCandidate(t, candidate)
	if _, err := ApproveCandidate(context.Background(), store, fixedClock, candidate.URL); err != nil {
		t.Fatal(err)
	}
	if !store.publishes[0].event.EndsAt.Equal(candidate.EndTime) {
		t.Fatalf("event end time = %v, want candidate end time %v", store.publishes[0].event.EndsAt, candidate.EndTime)
	}
}

func TestRejectMarksCandidateRejected(t *testing.T) {
	store := storeWithCandidate(t, verifiedCandidate())
	rejected, err := RejectCandidate(context.Background(), store, fixedClock, "https://example.test/event")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != CandidateRejected {
		t.Fatalf("candidate status = %q, want rejected", rejected.Status)
	}
	if rejected.ReviewedAt == nil || !rejected.ReviewedAt.Equal(fixedClock) {
		t.Fatalf("reviewed at = %v, want fixed clock %v", rejected.ReviewedAt, fixedClock)
	}
	if len(store.publishes) != 0 {
		t.Fatal("rejecting must not publish an event")
	}
	if len(store.updates) != 1 || store.updates[0].Status != CandidateRejected {
		t.Fatalf("rejection not persisted through UpdateCandidate: %#v", store.updates)
	}
}

func TestRestoreReturnsCandidateToPendingReview(t *testing.T) {
	rejected := verifiedCandidate()
	rejected.Status = CandidateRejected
	store := storeWithCandidate(t, rejected)
	restored, err := RestoreCandidate(context.Background(), store, fixedClock, rejected.URL)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != CandidatePending {
		t.Fatalf("candidate status = %q, want pending", restored.Status)
	}
	if restored.ReviewedAt == nil || !restored.ReviewedAt.Equal(fixedClock) {
		t.Fatalf("reviewed at = %v, want fixed clock %v", restored.ReviewedAt, fixedClock)
	}
	if len(store.updates) != 1 || store.updates[0].Status != CandidatePending {
		t.Fatalf("restore not persisted through UpdateCandidate: %#v", store.updates)
	}
}

func TestApprovalMissingCandidateIsNotFound(t *testing.T) {
	store := &fakeStore{}
	_, err := ApproveCandidate(context.Background(), store, fixedClock, "https://example.test/missing")
	if !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("lookup error = %v, want ErrCandidateNotFound", err)
	}
}
