# Munich AI profile

This is the original deployment profile for Event Radar. It is deliberately
kept outside the generic defaults and contains Munich- and AI-specific feeds,
location aliases, search queries, and Gemini prompts.

Copy `config.env` to a local environment file, replace the feed and admin
tokens, and add provider credentials only through your secret manager or shell
environment. The profile does not include the optional one-off seed event.

Candidates discovered by SearXNG or Gemini are not published automatically.
Use the authenticated `/admin` page to review verified candidates before
approval.
