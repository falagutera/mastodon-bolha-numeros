# AGENTS.md

Notes for coding agents (and people) changing numeros. The README says what the app does and how
to install it; this file says how to change it without breaking the promises the README makes.

## What it is

One Go binary, no JavaScript framework, no ORM. It reads a Mastodon database **read-only** (as a
role with column-level grants) and the admin API, once an hour, and serves:

- `/` — the instance's public numbers (a snapshot, rebuilt each round);
- `/minhaconta` — a signed-in person's own numbers (OAuth on the instance);
- `/@user` — an opt-in public page with a subset of those numbers.

## Commands

```sh
gofmt -l .            # must print nothing
go vet ./...
go test ./...         # without NUMEROS_TEST_DB_HOST the database tests skip themselves
```

With a database (what CI runs). Pick a free port: the tests **drop and recreate the `public`
schema** of whatever they reach, so chain with `&&` and never point them at a shared Postgres
(they refuse a database whose name does not end in `_test`).

```sh
P=55491   # any free port
docker run -d --name numeros-pg-$P -p $P:5432 -e POSTGRES_PASSWORD=pg -e POSTGRES_DB=numeros_test postgres:17 &&
  until docker exec numeros-pg-$P pg_isready -U postgres -q; do sleep 1; done &&
  NUMEROS_TEST_DB_HOST=127.0.0.1 NUMEROS_TEST_DB_PORT=$P go test ./...

# then every query again as the app's role, with only the grants of docs/database.sql (no psql needed on the host):
docker exec -i numeros-pg-$P psql -q -U postgres -d numeros_test -v password="'test'" -f - < docs/database.sql &&
  NUMEROS_TEST_DB_HOST=127.0.0.1 NUMEROS_TEST_DB_PORT=$P NUMEROS_TEST_APP_PASSWORD=test go test -run TestReadsAsTheAppUser ./...

docker rm -f numeros-pg-$P
```

Test variables: `NUMEROS_TEST_DB_HOST`, `_PORT` (5432), `_NAME` (numeros_test), `_USER` (postgres),
`_PASSWORD` (pg), `NUMEROS_TEST_APP_PASSWORD` (the `numeros` role, for `TestReadsAsTheAppUser`).

To look at the rendered own page: `mkdir -p /tmp/numeros-dump && NUMEROS_DUMP=/tmp/numeros-dump go test -run TestAccountPages ./...`
(a **directory**: it writes `me.html` and `numeros.css` there).

## Map

| File | What |
|---|---|
| `main.go` | config from env, wiring, the hourly loop |
| `collector.go`, `snapshot.go` | one round: runs the queries, writes the snapshot JSON |
| `store.go` | the public queries; shared SQL filters `localOK`, `remoteOK`, `postOK` |
| `admin.go`, `backup.go` | admin API (versions, space) and backup sources (Velero, Databasus) |
| `auth.go` | OAuth sign-in (state, PKCE), session cookie, form tokens |
| `me.go`, `mepage.go` | per-account numbers, their cache (`meCache`), the `/minhaconta` and `/@user` handlers |
| `more.go`, `moreview.go` | the extra cards of `/minhaconta` (queries, then view model) |
| `pages.go` | the opt-in list of public pages (a file on the volume) |
| `server.go` | routes, templates, security headers (CSP) |
| `i18n.go` | every text, in `pt` and `en` |
| `templates/` | `page.html` (public), `me.html` (account pages), `numeros.css` |
| `docs/database.sql` | the role and **every grant**; `docs/kubernetes.yaml` an example deployment |
| `snapshot.go` `snowflake()`, `me.go` `bounds` | turn times into `statuses.id` ranges (Mastodon ids are time-ordered) |

Tests: `numeros_test.go` (`testSchema`, the slice of Mastodon's schema the tests build; public
page and privacy rules), `me_test.go` (sign-in and pages against a fake instance: `TestAccountPages`
with fake numbers from `testMore()`; `TestMeQueries`), `more_test.go` (`TestMoreQueries`, SQL
fixtures for the extra cards; `TestReadsAsTheAppUser`), `backup_test.go`, `i18n_test.go`,
`changelog_test.go`.

## Rules that must hold

Privacy is the product. A change that weakens one of these is a bug, even if a test does not catch it yet.

1. **Read-only, least privilege.** New table or column → add the grant to `docs/database.sql`
   (column-level on `accounts`, `users`, `oauth_applications`, `reports`), add it to `testSchema`,
   and make sure `TestReadsAsTheAppUser` covers the query. Never grant `accounts.private_key`
   or any token, password or e-mail column.
2. **Newer tables are optional.** A query on a table an older install has not granted goes
   through `optional()` (savepoint; only "no grant" and "no table" are swallowed, logged once per
   process), so the page stays up with the card missing.
3. **Public lists use the shared filters.** People: `localOK` / `remoteOK` (discoverable, good
   standing, not a bot, no `#nonumeros`). Posts: `postOK` plus `postExtra` (public, no content
   warning, no sensitive media, not local-only, not expiring). Apps only with 3+ users.
4. **Visibility:** 0 public, 1 unlisted, 2 followers-only, 3 direct, 4 limited (`vis >= 3` = private
   conversation). Direct and limited posts never feed anything another person could see. On the
   person's own page some plain totals count them (with a CW, edited, self-deleting, the
   visibility split) while new posts and replies do not (see `MeMore` in `more.go`); they stay
   out of anything that names or lists: languages, apps, media grid, best posts, top poll.
   Text behind a content warning shows only the warning.
5. **Own page vs public page.** `me.html` renders both. The extra cards are only computed for
   the own page (`mePage` fills `More` only when not public) and the template guards them with
   `{{if and .More (not .Public)}}`. `/@user` exists only while the person opted in. In
   `TestAccountPages` every private label goes in two lists: the own page's `want` list and the
   public page's `not` list (Portuguese labels: the test fetches the pages without `/en`).
6. **No inline script or style.** CSP is `script-src 'self'; style-src 'self'`; put CSS in
   `numeros.css`. (An edge may inject a same-origin `/__bolha/bar.js`; `'self'` covers it.)
7. **Both languages.** Every key in `strs` exists in `pt` and `en` (`TestTextsInBothLanguages`).
8. **Bounded work.** Account queries run in a read-only transaction with a statement timeout, one
   at a time per process (the `meCache` semaphore). Keep new queries index-friendly and windowed
   by `statuses.id` ranges (`snowflake(t)`, `bounds.get`), like the existing ones.

## Adding a number (the usual change)

0. **Check it is not there already.** Grep `i18n.go` for the label you would write (both
   languages) and read the card in `templates/me.html` or `page.html`. The own page already has,
   among others: posting style (new posts, replies, with a CW, edited, self-deleting, DMs
   received), visibility split, heat map by hour, streaks, languages, apps, media, best posts,
   who you fav and boost, new and quiet followers, polls, hashtags, bookmarks.
1. Query in `store.go` (public) or `more.go` (own page), inside the existing transaction; reuse
   a pass that already reads the same rows (e.g. `ownPosts`) instead of a new scan.
2. **Only if the query reads a table or column not granted yet** (check `docs/database.sql`):
   add the grant, add it to `testSchema`, and wrap the query in `optional()` if older installs
   may lack it.
3. Fixture rows and a `check(...)` in `TestMoreQueries` (or the public-page test), including the
   cases that must not count or show: a DM, a CW post, a non-discoverable account.
4. Field in the snapshot or `MeMore`, view in `moreview.go`, markup in the template, texts in
   both languages in `i18n.go`, a fake value in `testMore()`, the label in the `TestAccountPages`
   lists (rule 5).
5. `gofmt`, `go vet`, the tests with a database, and `TestReadsAsTheAppUser` with the grants.

## Releasing

- `VERSION` holds `X.Y.Z`; CI adds the build number (`X.Y.Z-BUILD`). Bump it for user-visible changes.
- Every version needs its `CHANGELOG.md` entry (`TestChangelogHasVersion`); it is also the
  `/release-notes` page, so write it for the people who use the site.
- A change that touches the grants says so in the changelog: installs must re-run `docs/database.sql`.
- Code, comments, commits and the README in English. Keep commits small with a message that says why.

## Security checks

CI runs gofmt, vet, the tests against PostgreSQL 17, govulncheck and the shared scanners (gitleaks, gosec, semgrep, trivy, hadolint, shellcheck,
yamllint, poutine).
Before a release that touches queries, sign-in or templates, also review the change for
authorization and data exposure (who can see this number, on which page) — that is the part
scanners do not see.
