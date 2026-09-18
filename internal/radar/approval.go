package radar

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Publish-gate outcomes for approving a candidate. One sentinel per condition,
// checked in the order the review handler enforced them before approval moved
// into this module.
var (
	ErrNotVerified             = errors.New("candidate is not verified")
	ErrMissingEventTitle       = errors.New("candidate has no verified event title")
	ErrMissingStartTime        = errors.New("candidate has no verified start time")
	ErrInPast                  = errors.New("candidate start time is in the past")
	ErrMissingLocation         = errors.New("candidate has no verified location")
	ErrMissingEvidenceURL      = errors.New("candidate has no evidence URL")
	ErrMissingDateEvidence     = errors.New("candidate has no date evidence")
	ErrMissingLocationEvidence = errors.New("candidate has no location evidence")
)

// gateError marks a failed publish-gate condition so the review handler can map
// every gate outcome to the same response, while tests can still match the
// individual sentinel with errors.Is.
type gateError struct{ reason error }

func (e gateError) Error() string { return e.reason.Error() }
func (e gateError) Unwrap() error { return e.reason }

func isApprovalGateError(err error) bool {
	var gate gateError
	return errors.As(err, &gate)
}

// Persistence outcomes the review handler maps to its existing responses.
var (
	errCandidateLookup = errors.New("candidate lookup failed")
	errEventPersist    = errors.New("event upsert failed")
	errCandidateUpdate = errors.New("candidate update failed")
)

// ApproveCandidate enforces the publish gate on a candidate under review and,
// on success, publishes it as a tentative, non-anchored event and records the
// approval review metadata. The candidate is loaded by URL through the Store
// interface; now is the injected clock for the "in the past" check and the
// reviewed_at timestamp.
func ApproveCandidate(ctx context.Context, store Store, now time.Time, rawURL string) (Candidate, error) {
	candidate, err := loadCandidate(ctx, store, rawURL)
	if err != nil {
		return Candidate{}, err
	}
	if gateErr := approveGate(candidate, now); gateErr != nil {
		return candidate, gateErr
	}
	event := Event{Source: "reviewed-" + candidate.Source, SourceID: candidate.URL, Title: candidate.EventTitle, Description: candidate.Description, Location: candidate.Location, URL: candidate.EvidenceURL, StartsAt: candidate.StartTime, EndsAt: candidate.EndTime, Status: StatusTentative, Anchor: false, Score: candidate.Score}
	if event.EndsAt.IsZero() {
		event.EndsAt = event.StartsAt.Add(2 * time.Hour)
	}
	if err := store.UpsertEvent(ctx, event); err != nil {
		return candidate, fmt.Errorf("%w: %v", errEventPersist, err)
	}
	candidate.Status = CandidateApproved
	candidate.ReviewedAt = timePtr(now)
	if err := store.UpdateCandidate(ctx, candidate); err != nil {
		return candidate, fmt.Errorf("%w: %v", errCandidateUpdate, err)
	}
	return candidate, nil
}

// RejectCandidate marks the candidate rejected and records the review metadata.
func RejectCandidate(ctx context.Context, store Store, now time.Time, rawURL string) (Candidate, error) {
	candidate, err := loadCandidate(ctx, store, rawURL)
	if err != nil {
		return Candidate{}, err
	}
	candidate.Status = CandidateRejected
	candidate.ReviewedAt = timePtr(now)
	if err := store.UpdateCandidate(ctx, candidate); err != nil {
		return candidate, fmt.Errorf("%w: %v", errCandidateUpdate, err)
	}
	return candidate, nil
}

// RestoreCandidate returns a candidate under review to pending status and
// records the review metadata.
func RestoreCandidate(ctx context.Context, store Store, now time.Time, rawURL string) (Candidate, error) {
	candidate, err := loadCandidate(ctx, store, rawURL)
	if err != nil {
		return Candidate{}, err
	}
	candidate.Status = CandidatePending
	candidate.ReviewedAt = timePtr(now)
	if err := store.UpdateCandidate(ctx, candidate); err != nil {
		return candidate, fmt.Errorf("%w: %v", errCandidateUpdate, err)
	}
	return candidate, nil
}

// loadCandidate fetches the candidate under review by its URL.
func loadCandidate(ctx context.Context, store Store, rawURL string) (Candidate, error) {
	candidate, err := store.Candidate(ctx, rawURL)
	if err != nil {
		return Candidate{}, fmt.Errorf("%w: %v", errCandidateLookup, err)
	}
	return candidate, nil
}

// approveGate enforces the publish gate: only a verified candidate with an
// explicit future date, venue, and evidence can be approved. The conditions
// mirror the review handler verbatim.
func approveGate(candidate Candidate, now time.Time) error {
	switch {
	case candidate.Verification != CandidateVerified:
		return gateError{ErrNotVerified}
	case candidate.EventTitle == "":
		return gateError{ErrMissingEventTitle}
	case candidate.StartTime.IsZero():
		return gateError{ErrMissingStartTime}
	case !candidate.StartTime.After(now):
		return gateError{ErrInPast}
	case candidate.Location == "":
		return gateError{ErrMissingLocation}
	case candidate.EvidenceURL == "":
		return gateError{ErrMissingEvidenceURL}
	case candidate.DateEvidence == "":
		return gateError{ErrMissingDateEvidence}
	case candidate.LocationEvidence == "":
		return gateError{ErrMissingLocationEvidence}
	}
	return nil
}
