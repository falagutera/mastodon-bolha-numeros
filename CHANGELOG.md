# Changelog

All notable changes to numeros. Versions follow [SemVer](https://semver.org/); images are
published as `registry.gutocarvalho.net/images/numeros:<version>-<build>`.

## 0.1.2 — 2026-11-07

Layout fixes after a first look on bolha.us.

- The top numbers line up: labels that wrap keep two lines, and the date fits on one.
- New users and reports get a heading each, with short labels (week, month, 6 months, year).
- Links in three columns, with room for long titles (up to three lines) and long addresses.
- Custom emoji shortcodes (":neovim:") are left out of display names.
- Photos from accounts a moderator marked as always sensitive stay out, like sensitive posts already did.

## 0.1.1 — 2026-11-07

Fixes from the first round on bolha.us.

- Apps are counted by name: each install of a mobile app registers its own OAuth application, so counting by id left out every app but the web one.
- Ruby shows only its version number, not the whole `ruby -v` line.
- The search version is labelled as the Elasticsearch-compatible API version OpenSearch reports.

## 0.1.0 — 2026-11-07

First version: bolha.us in numbers, on one page, in Portuguese and English.

- Tops of 12 people, for today, 7 days and 30 days: most toots, boosts and favs received, new followers, people followed, and people from other servers who talk to bolha.us the most.
- Latest links, photos and polls from public posts.
- Server: users, active users, posts, known servers, age; new users and reports per week, month, 6 months and year; apps, languages and the servers that talk to us the most.
- Software versions and space used, from the instance's own admin dashboard (with a dashboard-only token); search index size from OpenSearch.
- Collected once an hour from the database (read-only user, one connection, 60 s per query) and kept in a snapshot: a visit never touches the database. `/numeros.json` has the same data.
- Privacy: only discoverable accounts in good standing appear; people who hide their social graph stay out of followers/following; `#nonumeros` in the bio takes anyone out; content only from public posts with no content warning, no sensitive media, not local-only and not expiring; an app only shows up when at least 3 people use it.
