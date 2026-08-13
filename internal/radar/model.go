package radar

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

type EventStatus string

const (
	StatusConfirmed EventStatus = "CONFIRMED"
	StatusTentative EventStatus = "TENTATIVE"
)

type Event struct {
	UID         string
	Source      string
	SourceID    string
	Title       string
	Description string
	Location    string
	URL         string
	StartsAt    time.Time
	EndsAt      time.Time
	Status      EventStatus
	Anchor      bool
	Score       int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Candidate struct {
	Source           string     `json:"source"`
	Title            string     `json:"title"`
	URL              string     `json:"url"`
	Snippet          string     `json:"snippet"`
	Score            int        `json:"score"`
	Discovered       time.Time  `json:"discovered"`
	Status           string     `json:"status"`
	Verification     string     `json:"verification"`
	EventTitle       string     `json:"event_title,omitempty"`
	StartTime        time.Time  `json:"start_time,omitempty"`
	EndTime          time.Time  `json:"end_time,omitempty"`
	Location         string     `json:"location,omitempty"`
	Description      string     `json:"description,omitempty"`
	EvidenceURL      string     `json:"evidence_url,omitempty"`
	DateEvidence     string     `json:"date_evidence,omitempty"`
	LocationEvidence string     `json:"location_evidence,omitempty"`
	Confidence       string     `json:"confidence,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	VerifiedAt       *time.Time `json:"verified_at,omitempty"`
	ReviewedAt       *time.Time `json:"reviewed_at,omitempty"`
	ReviewNote       string     `json:"review_note,omitempty"`
}

const (
	CandidatePending    = "pending"
	CandidateApproved   = "approved"
	CandidateRejected   = "rejected"
	CandidateVerified   = "verified"
	CandidateFailed     = "failed"
	CandidateUnverified = "unverified"
)

type SourceHealth struct {
	Name        string     `json:"name"`
	Enabled     bool       `json:"enabled"`
	State       string     `json:"state"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

func EventUID(source, sourceID, title string, startsAt time.Time) string {
	value := source + "|" + sourceID + "|" + strings.ToLower(strings.TrimSpace(title)) + "|" + startsAt.UTC().Format(time.RFC3339)
	hash := sha256.Sum256([]byte(value))
	return "event-radar-" + hex.EncodeToString(hash[:12])
}

func EventFingerprint(event Event) string {
	value := strings.ToLower(strings.TrimSpace(event.Title)) + "|" +
		event.StartsAt.UTC().Format(time.RFC3339) + "|" +
		strings.ToLower(strings.TrimSpace(event.Location))
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}
