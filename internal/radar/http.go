package radar

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

func (r *Radar) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", r.handleHealth)
	mux.HandleFunc("GET /metrics", r.handleMetrics)
	mux.HandleFunc("GET /status", r.handleStatus)
	mux.HandleFunc("GET /calendar/", r.handleCalendar)
	mux.HandleFunc("GET /admin", r.handleAdmin)
	mux.HandleFunc("POST /admin/candidate", r.handleAdminCandidate)
	return mux
}

func (r *Radar) handleHealth(writer http.ResponseWriter, request *http.Request) {
	health, err := r.Health(request.Context())
	if err != nil {
		http.Error(writer, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	for _, source := range health {
		if source.Enabled && source.State == "error" {
			http.Error(writer, "one or more enabled sources failed", http.StatusServiceUnavailable)
			return
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
}

func (r *Radar) handleStatus(writer http.ResponseWriter, request *http.Request) {
	health, err := r.Health(request.Context())
	if err != nil {
		http.Error(writer, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	candidates, err := r.CandidateCounts(request.Context())
	if err != nil {
		http.Error(writer, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"sources": health, "candidates": candidates})
}

func (r *Radar) handleMetrics(writer http.ResponseWriter, request *http.Request) {
	events, err := r.Events(request.Context())
	if err != nil {
		http.Error(writer, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	health, err := r.Health(request.Context())
	if err != nil {
		http.Error(writer, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	candidates, err := r.CandidateCounts(request.Context())
	if err != nil {
		http.Error(writer, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = writer.Write([]byte("# HELP event_radar_upcoming Number of upcoming events.\n# TYPE event_radar_upcoming gauge\nevent_radar_upcoming " + strconvItoa(len(events)) + "\n"))
	for status, count := range candidates {
		_, _ = writer.Write([]byte(`event_radar_candidates{status="` + escapeMetric(status) + `"} ` + strconvItoa(count) + "\n"))
	}
	for _, source := range health {
		state := 0
		if source.State == "healthy" {
			state = 1
		}
		_, _ = writer.Write([]byte(`event_radar_source_healthy{source="` + escapeMetric(source.Name) + `"} ` + strconvItoa(state) + "\n"))
	}
}

func (r *Radar) handleCalendar(writer http.ResponseWriter, request *http.Request) {
	token := strings.TrimPrefix(request.URL.Path, "/calendar/")
	if !strings.HasSuffix(token, ".ics") || strings.TrimSuffix(token, ".ics") != r.config.FeedToken {
		http.NotFound(writer, request)
		return
	}
	events, err := r.Events(request.Context())
	if err != nil {
		http.Error(writer, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write([]byte(RenderICS(r.config, events)))
}

func (r *Radar) adminAuthorized(request *http.Request) bool {
	if r.config.AdminToken == "" {
		return false
	}
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if queryToken := request.URL.Query().Get("token"); queryToken != "" {
		token = queryToken
	}
	if request.Method == http.MethodPost && token == "" {
		token = request.FormValue("token")
	}
	if token == "" {
		if cookie, err := request.Cookie("radar_admin_session"); err == nil {
			expected := r.adminSessionValue()
			return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(expected)) == 1
		}
	}
	if token == "" {
		return false
	}
	authorized := subtle.ConstantTimeCompare([]byte(token), []byte(r.config.AdminToken)) == 1
	return authorized
}

func (r *Radar) adminSessionValue() string {
	mac := hmac.New(sha256.New, []byte(r.config.AdminToken))
	_, _ = mac.Write([]byte("event-radar-admin-session-v1"))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func (r *Radar) handleAdmin(writer http.ResponseWriter, request *http.Request) {
	if !r.adminAuthorized(request) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="`+r.config.AppName+` admin"`)
		http.Error(writer, "admin token required", http.StatusUnauthorized)
		return
	}
	candidates, err := r.Candidates(request.Context(), false)
	if err != nil {
		http.Error(writer, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if request.URL.Query().Get("token") != "" {
		http.SetCookie(writer, &http.Cookie{Name: "radar_admin_session", Value: r.adminSessionValue(), Path: "/admin", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: request.TLS != nil})
	}
	token := html.EscapeString(request.URL.Query().Get("token"))
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte("<!doctype html><meta charset=utf-8><title>" + html.EscapeString(r.config.AppName) + " review</title><style>body{font:16px system-ui;max-width:1000px;margin:2rem auto}article{border:1px solid #ccc;padding:1rem;margin:1rem 0}small{color:#666}form{display:inline}button{margin:.25rem}</style><h1>Pending review</h1>"))
	if len(candidates) == 0 {
		_, _ = writer.Write([]byte("<p>Nothing needs review.</p>"))
	}
	for _, candidate := range candidates {
		_, _ = writer.Write([]byte("<article><h2>" + html.EscapeString(candidate.EventTitleOrTitle()) + "</h2>"))
		_, _ = writer.Write([]byte("<p><b>" + html.EscapeString(candidate.Status) + "</b> / " + html.EscapeString(candidate.Verification) + " · score " + fmt.Sprint(candidate.Score) + "</p>"))
		_, _ = writer.Write([]byte("<p>" + html.EscapeString(candidate.Snippet) + "</p><p><a href=\"" + html.EscapeString(candidate.URL) + "\">source</a></p>"))
		if !candidate.StartTime.IsZero() {
			temporalStatus := "future"
			if !candidate.StartTime.After(time.Now()) {
				temporalStatus = "PAST — cannot be approved"
			}
			_, _ = writer.Write([]byte("<p><b>Suggested date:</b> " + html.EscapeString(candidate.StartTime.Format(time.RFC1123)) + " · <b>" + temporalStatus + "</b><br>" + html.EscapeString(candidate.Location) + "</p>"))
			_, _ = writer.Write([]byte("<p>Date evidence: " + html.EscapeString(candidate.DateEvidence) + "<br>Location evidence: " + html.EscapeString(candidate.LocationEvidence) + "</p>"))
		} else {
			_, _ = writer.Write([]byte("<p><b>Suggested date:</b> not verified</p>"))
		}
		_, _ = writer.Write([]byte("<form method=post action=\"/admin/candidate\"><input type=hidden name=token value=\"" + token + "\"><input type=hidden name=url value=\"" + html.EscapeString(candidate.URL) + "\"><button name=action value=approve>Approve</button><button name=action value=reject>Reject</button><button name=action value=restore>Restore</button></form></article>"))
	}
}

func (c Candidate) EventTitleOrTitle() string {
	if c.EventTitle != "" {
		return c.EventTitle
	}
	return c.Title
}

func (r *Radar) handleAdminCandidate(writer http.ResponseWriter, request *http.Request) {
	if !r.adminAuthorized(request) {
		http.Error(writer, "admin token required", http.StatusUnauthorized)
		return
	}
	if err := request.ParseForm(); err != nil {
		http.Error(writer, "invalid form", http.StatusBadRequest)
		return
	}
	candidate, err := r.Candidate(request.Context(), request.FormValue("url"))
	if err != nil {
		http.Error(writer, "candidate not found", http.StatusNotFound)
		return
	}
	switch request.FormValue("action") {
	case "approve":
		if candidate.Verification != CandidateVerified || candidate.EventTitle == "" || candidate.StartTime.IsZero() || !candidate.StartTime.After(time.Now()) || candidate.Location == "" || candidate.EvidenceURL == "" || candidate.DateEvidence == "" || candidate.LocationEvidence == "" {
			http.Error(writer, "candidate is not verified and cannot be approved", http.StatusBadRequest)
			return
		}
		event := Event{Source: "reviewed-" + candidate.Source, SourceID: candidate.URL, Title: candidate.EventTitle, Description: candidate.Description, Location: candidate.Location, URL: candidate.EvidenceURL, StartsAt: candidate.StartTime, EndsAt: candidate.EndTime, Status: StatusTentative, Anchor: false, Score: candidate.Score}
		if event.EndsAt.IsZero() {
			event.EndsAt = event.StartsAt.Add(2 * time.Hour)
		}
		if err := r.store.UpsertEvent(request.Context(), event); err != nil {
			http.Error(writer, "could not approve candidate", http.StatusInternalServerError)
			return
		}
		candidate.Status = CandidateApproved
		candidate.ReviewedAt = timePtr(time.Now().UTC())
	case "reject":
		candidate.Status = CandidateRejected
		candidate.ReviewedAt = timePtr(time.Now().UTC())
	case "restore":
		candidate.Status = CandidatePending
		candidate.ReviewedAt = timePtr(time.Now().UTC())
	default:
		http.Error(writer, "unknown action", http.StatusBadRequest)
		return
	}
	if err := r.UpdateCandidate(request.Context(), candidate); err != nil {
		http.Error(writer, "could not update candidate", http.StatusInternalServerError)
		return
	}
	http.Redirect(writer, request, "/admin?token="+request.FormValue("token"), http.StatusSeeOther)
}

func timePtr(value time.Time) *time.Time { return &value }

func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}
	const digits = "0123456789"
	var buffer [20]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = digits[value%10]
		value /= 10
	}
	return string(buffer[position:])
}

func escapeMetric(value string) string { return strings.ReplaceAll(value, `"`, `\"`) }
