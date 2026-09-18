package auth

import "strings"

// Allowlist decides which authenticated identities may use the admin UI.
// All three kinds of entries are optional; every entry is trimmed and
// lowercased at construction so comparisons are case-insensitive.
type Allowlist struct {
	emails  map[string]bool
	domains map[string]bool
	subs    map[string]bool
}

// NewAllowlist builds an Allowlist from configured entries. An empty list
// means "no entries of this kind".
func NewAllowlist(emails, domains, subs []string) Allowlist {
	allow := Allowlist{emails: map[string]bool{}, domains: map[string]bool{}, subs: map[string]bool{}}
	for _, email := range emails {
		if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
			allow.emails[email] = true
		}
	}
	for _, domain := range domains {
		if domain = strings.ToLower(strings.TrimSpace(domain)); domain != "" {
			allow.domains[domain] = true
		}
	}
	for _, sub := range subs {
		if sub = strings.TrimSpace(sub); sub != "" {
			allow.subs[sub] = true
		}
	}
	return allow
}

// Permits reports whether the given identity passes the allowlist. A matching
// subject bypasses the email checks entirely; otherwise the email must be
// verified and either match an allowed address or belong to an allowed domain.
// With all lists empty nothing is permitted — configuration is responsible for
// requiring at least one entry when auth is enabled.
func (a Allowlist) Permits(sub, email string, emailVerified bool) bool {
	if sub != "" && a.subs[sub] {
		return true
	}
	if !emailVerified || email == "" {
		return false
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if a.emails[email] {
		return true
	}
	_, domain, found := strings.Cut(email, "@")
	if !found || domain == "" {
		return false
	}
	return a.domains[domain]
}
