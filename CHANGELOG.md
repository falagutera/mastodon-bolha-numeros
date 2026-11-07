# Changelog

All notable changes to numeros. Versions follow [SemVer](https://semver.org/); images are
published as `registry.gutocarvalho.net/images/numeros:<version>-<build>`.

## 0.2.2 — 2026-11-07

- Each backup module keeps its card even when its first round fails ("no data this round"), and the cards stay in two columns, so the section doesn't change shape.
- The stylesheet URL carries the version, so a new version's styles show at once instead of after the browser's cache expires.

## 0.2.1 — 2026-11-07

- Databasus: a canceled backup no longer counts as the last one (it said "failed" until the next run); "last" is the newest backup that completed or failed.

## 0.2.0 — 2026-11-07

Backup section, and nothing about bolha.us built in.

- **Backup:** one card per backup tool, with retention, daily time (in the page's timezone), the last run and how it went, how many backups are kept and their sizes. Modules, picked and ordered with `NUMEROS_BACKUP_SOURCES`:
  - `velero`: a schedule's TTL and cron, its backups and the volume sizes of the newest completed one, through the Kubernetes API (read-only role in Velero's namespace).
  - `databasus`: a database's backup config and logical backups, with a viewer account.
  A failing module keeps its previous card and is listed as stale.
- **Any instance:** name, how the page refers to the instance, headline, media storage name, footer link and backup note come from the config (`NUMEROS_NAME`, `NUMEROS_US`, `NUMEROS_HEADLINE`, `NUMEROS_MEDIA_STORAGE`, `NUMEROS_PARENT_*`, `NUMEROS_BACKUP_NOTE`). The README says how external PostgreSQL, Redis and search are covered.

## 0.1.3 — 2026-11-07

- The top numbers follow the server section's layout: one-line labels, the detail on the line below ("in the last 30 days · 12%", "known across the fediverse"), every tile the same height, every number the same size. The start date shows month and year.

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
