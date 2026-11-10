# Changelog

All notable changes to numeros. Versions follow [SemVer](https://semver.org/); images are
published as `registry.gutocarvalho.net/images/numeros:<version>-<build>`.

## 0.5.3 — 2026-11-10

- **Backup cards:** with the two cards side by side, a value no longer breaks across lines under a long label ("hoje às 01:00 · 10 min" split, with the dot left on its own); the label wraps instead. On a phone, where the cards stack, a long value still wraps and the dot stays on its first line.
- Look from `open/gcn/ui` 0.1.6: the numbers in a row of tiles line up when a label takes two lines.

## 0.5.2 — 2026-11-08

- The look now comes from `open/gcn/ui` (`assets/style.css`, a synced copy, and `VISUAL.md`): `/numeros.css` serves it followed by numeros's own rules (`templates/numeros.css`). The page looks the same; numeros was the reference the shared look came from.

## 0.5.1 — 2026-11-08

- **Runs with a plain Docker volume:** the image now has `/data` owned by its user, so a new volume mounted there can hold the snapshot and the public pages list (before, `docker run -v numeros-data:/data` failed with "permission denied"; Kubernetes was fine thanks to `fsGroup`).
- **Install guide tested end to end:** the README walks through the database user, the optional admin token and sign-in, and running it with `docker run`, next to Mastodon's own `docker-compose.yml`, or on Kubernetes with the new `docs/kubernetes.yaml`; plus what the usual errors mean. Two variables were missing from the table: `NUMEROS_DB_SSLMODE` and `NUMEROS_LISTEN`.

## 0.5.0 — 2026-11-08

- **Much more on `/minhaconta`**, only for you, never on the public page:
  - **Your media** of the last 30 days: photos, videos, GIFs and audio, how many have alt text, the space everything you uploaded takes, and your newest photos (not from direct messages, nor anything behind a content warning).
  - **Your best posts:** the 5 with most favs and boosts and the one with most replies (a post behind a warning shows only the warning); your most voted poll.
  - **How you post:** public, unlisted, followers-only, local-only and direct; content warnings, edits, self-deleting posts, replies against new posts, direct messages received (the count only); your languages, apps and hashtags (none from posts behind a warning).
  - **When you post:** a weekday × hour map of the last 30 days, your streak of days posting and the longest one, days with a post in the last 90.
  - **The other side:** who you fav and boost the most (favs on direct messages left out), your new followers, and the accounts you follow for over 30 days that didn't fav, boost or mention you in that time.
  - **Polls and bookmarks:** polls you created and their votes, polls you voted on, posts you bookmarked and the servers they come from.
- **The database user reads less:** only the columns it uses of `accounts` (never the accounts' signing keys), `users` (no e-mail, password hash or 2FA secret), `oauth_applications` (no client secrets) and `reports` (no comments). A test runs every query as that user to prove the grants are enough.
- The extra cards need five more tables (`status_stats`, `tags`, `statuses_tags`, `bookmarks`, `poll_votes`); without the grant that part is just empty. `docs/database.sql` creates or updates the database user in one go, and the README now walks through the whole install.

## 0.4.2 — 2026-11-08

- **The bolha menu shows up here too:** the pages now let same-origin scripts run, so the floating "● bolha" button the edge adds (as on the other bolha.us apps) loads. The pages themselves still carry no script.

## 0.4.1 — 2026-11-08

- **"24 horas" instead of "Hoje":** the shortest period is now the last 24 hours, on the main page and on `/minhaconta`. "Today since midnight" was nearly empty right after midnight and looked like missing data.
- **Backup cards show the last backup that completed.** A failed or partial run no longer shows as "failed" here: the page is about the instance, and backup failures are for the monitoring.

## 0.4.0 — 2026-11-08

- **Your numbers (`/minhaconta`):** sign in with the instance and see your own numbers for today, 7 and 30 days: posts, boosts and favs received and given, mentions, followers gained, people you started following, who talks to you the most (local and remote) and the links you shared. Computed on the visit from your own rows and kept for 5 minutes.
- **Public page, if you want one:** from `/minhaconta` you can open `/@you`, which anyone can see. It only shows the people who talk to you if their profile is discoverable and has no `#nonumeros`, counts only public and unlisted posts and boosts and what they got (never direct, followers-only or local-only ones), leaves out the favs you give (private in Mastodon), only links from public posts, and leaves follower numbers out if you hide who follows you. Off by default; closing it, `#nonumeros` in your bio or a limited account takes it down at once.
- Sign-in keeps nothing of yours: the instance's token is revoked right after it says who you are, and the session is a signed cookie. Off unless `NUMEROS_CLIENT_ID` is set.
- **`#nonumeros` from other servers:** their bios come as HTML, where Mastodon writes the tag as `#<span>nonumeros</span>`, so the opt-out never matched them. It does now, on every page.
- Visits use their own database connection, one query at a time with a 5-second limit, so they never slow the hourly round.

## 0.3.1 — 2026-11-07

- The card of people from other servers who mention the instance the most is now "Menções externas" / "External mentions".

## 0.3.0 — 2026-11-07

- **Platform versions:** `NUMEROS_PLATFORM=kubernetes rancher` adds Kubernetes (with the distribution: RKE2, K3s, EKS, GKE) and Rancher to the versions table, read from the cluster.
- **Velero card:** how many Kubernetes objects the last backup kept (the whole cluster's), and "volume data" for the instance's size, which is only what its volumes hold.

## 0.2.3 — 2026-11-07

- Velero: a 429 from the Kubernetes API (priority and fairness) is retried, waiting what Retry-After says, instead of losing the card for the round.

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
