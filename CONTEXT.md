# Event Radar

Event Radar is a local-first event aggregator: it imports configured iCalendar
feeds and optional discovery providers into SQLite, optionally verifies
discovered candidates through Gemini, and serves one calendar feed with an
optional email digest. Candidates are never published automatically.

## Language

### Events

**Anchored event**:
An event imported from a configured ICS feed, published without review.
_Avoid_: trusted event, feed event

**Calendar feed**:
The served ICS containing the full event history, past and future.
_Avoid_: calendar, subscription

**Publish**:
To make an event visible in the calendar feed. Only anchored events and
approved candidates are published.

### Candidates

**Candidate**:
A discovered event lead awaiting verification or review. Never published
automatically.
_Avoid_: suggestion, lead

**Discovery**:
Finding candidates through search providers or Gemini queries.
_Avoid_: scraping, searching

**Verification**:
Checking a candidate's exact event page for an explicit date, venue, and
evidence, producing a verified candidate.
_Avoid_: validation, AI check

**Evidence**:
Verbatim quotes from the event page proving the date and location.
_Avoid_: proof, citation

**Verifier**:
The module that performs verification. Gemini is the only verifier today.
_Avoid_: checker

**Review**:
Human moderation of candidates on the review page: approve, reject, or restore.
_Avoid_: moderation

**Approval**:
Accepting a verified candidate, which publishes it as a tentative event.
_Avoid_: confirmation

### Operations

**Sync**:
One pipeline run: prune stale candidates, fetch every source, record source
health, verify and store results.
_Avoid_: refresh, update

**Source**:
An anchored ICS feed or a discovery provider.
_Avoid_: provider, connector

**Source health**:
Per-source state after a sync: healthy, degraded, error, or disabled.

**Digest**:
The opt-in email summary of upcoming events.
