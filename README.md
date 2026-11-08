# numeros

A one-page view of any Mastodon instance in numbers: who posts the most, what goes
around, and how the server is doing inside. Runs at [numeros.bolha.us](https://numeros.bolha.us)
for bolha.us, in Portuguese (`/`) and English (`/en/`). AGPL-3.0.

- **Tops of 12**, for today, 7 days and 30 days: most toots, boosts and favs received,
  new followers, people followed, and people from other servers who talk to the instance the most.
- **Content:** latest links, photos and polls from public posts.
- **Server:** users, active users, posts, known servers and age; new users and reports per
  week, month, 6 months and year; apps, languages, and the servers that talk to the instance the most.
- **Your numbers** (`/minhaconta`), after signing in with the instance, and a public
  `/@you` page for whoever turns it on (see [Your numbers](#your-numbers)).
- **Software and space:** versions and space used, as the admin dashboard reports them
  (Mastodon, Ruby, PostgreSQL, Redis, search, libvips, FFmpeg; database, Redis, media), plus the
  search index size from OpenSearch/Elasticsearch.
- **Backup** (optional, pluggable): retention, schedule, last run and sizes, one card per backup tool.
- `/numeros.json` serves the same data.

## Privacy

Ranking people in public is the delicate part, so:

- Only **discoverable** accounts in good standing appear (not suspended, silenced, moved,
  memorial, bot or unconfirmed). The same goes for accounts from other servers.
- People who **hide their social graph** stay out of the followers and following tops.
- **`#nonumeros` in the bio** takes anyone out at the next round.
- Content only from **public** posts without a content warning or sensitive media, not
  local-only and not expiring (when the instance has those columns).
- An **app** only shows up when at least 3 people used it, so a personal app's name doesn't say who posts with it.
- Reports are only counted. The page asks search engines not to index it.

## Your numbers

`/minhaconta` (`/en/minhaconta`) shows a person their own numbers after they sign in with the
instance: posts, boosts and favs received and given, mentions, followers gained and people
followed, for today, 7 and 30 days; who talks to them the most; the links they shared.

- **Sign-in** is OAuth with PKCE and the `profile` scope, the same flow as circles: the token is
  only used to ask who the person is and is revoked at once. The session is an HMAC-signed,
  `__Host-` cookie for 4 hours; the forms (public page, sign out) carry a token tied to it.
- **Only the person sees it**, unless they open a **public page** at `/@username`. Off by default.
  The public page only shows the people who talk to them under the main page's rules
  (discoverable, no `#nonumeros`, in good standing) and only counts mentions made in public or
  unlisted posts, so it never tells who sends the person direct or followers-only messages; only
  links from posts the main page could
  show, and no follower numbers when they hide their social graph. `#nonumeros` in the bio, a
  limited or moved account, or a renamed one take it down at once, whatever the setting.
- A `/@name` that isn't on the list of open pages is a 404 without a query, so names can't be
  probed through the database. The list is a JSON file (`NUMEROS_PAGES_FILE`) with account ids and
  names only; put it on a persistent volume.
- **Database load:** numbers are computed on the visit from the account's own rows, through the
  `(account_id, id)` indexes; notifications and favourites start at an id found by binary search on
  the primary key (cached for an hour). On bolha.us the busiest account (156k notifications) takes
  ~230 ms. Visits have their own connection, one computation at a time, 5 s per query; numbers are
  kept 5 minutes for `/minhaconta` and 15 for public pages.
- Received boosts, favs and mentions come from notifications, so what the person muted,
  blocked or filtered is left out, as in their Mastodon.

## Install

| Variable | Default | |
|---|---|---|
| `NUMEROS_INSTANCE` | `https://bolha.us` | public URL of the instance |
| `NUMEROS_INTERNAL_URL` | | reach the instance inside the cluster (e.g. `http://web.bolha-us.svc.cluster.local:3000`); the public Host is kept |
| `NUMEROS_MEDIA_URL` | `$INSTANCE/system` | where media and avatars are served (`S3_ALIAS_HOST`) |
| `NUMEROS_DB_HOST` / `_PORT` / `_NAME` / `_USER` | `127.0.0.1` / `5432` / `mastodon_production` / `numeros` | |
| `NUMEROS_DB_PASSWORD` | | required |
| `NUMEROS_ADMIN_TOKEN` | | `admin:read` token of an account whose role can only **view the dashboard**: versions, space used, active users. Without it, the database's own version and size |
| `NUMEROS_SEARCH_URL` / `_USER` / `_PASSWORD` / `_PREFIX` | | search cluster, for the index size (a monitor-only user is enough) |
| `NUMEROS_TIMEZONE` | `America/Sao_Paulo` | "today" and the times shown |
| `NUMEROS_EVERY` / `NUMEROS_OFFSET` | `1h` / `7m` | collect every hour at :07 |
| `NUMEROS_SNAPSHOT` | | file to keep the last snapshot across restarts |
| `NUMEROS_PLATFORM` | | `kubernetes`, `rancher`: platform versions (see below) |
| `NUMEROS_CLIENT_ID` / `_SECRET` | | an OAuth app on the instance (scope `profile`, redirect `$PUBLIC_URL/auth/callback`); empty: no `/minhaconta` |
| `NUMEROS_PUBLIC_URL` | | this app's URL (`https://numeros.bolha.us`), for sign-in |
| `NUMEROS_SESSION_SECRET` | | at least 32 random bytes; signs the session cookie |
| `NUMEROS_PAGES_FILE` | | who opened a public page, on a persistent volume (`/data/public.json`) |

### Your instance

Nothing about bolha.us is built in; the page takes its names from the config.

| Variable | Default | |
|---|---|---|
| `NUMEROS_NAME` | the instance's host | used in the title, the header and the texts (`{name}`) |
| `NUMEROS_US` / `NUMEROS_US_EN` | `NUMEROS_NAME` | how the page refers to the instance in "servers talking to …" ("a bolha" / "the bubble") |
| `NUMEROS_HEADLINE` / `_EN` | "{name} em <em>números</em>." | the `<h1>`, as HTML |
| `NUMEROS_MEDIA_STORAGE` | | shown as "Media (X)", e.g. `Wasabi` |
| `NUMEROS_PARENT_NAME` / `_URL` | | "part of …" link in the footer |
| `NUMEROS_BACKUP_NOTE` / `_EN` | | a note under the backup cards |

### External services

Versions and sizes of PostgreSQL, Redis, the search cluster and the media come from the
instance's own admin dashboard (`NUMEROS_ADMIN_TOKEN`), which reports whatever Mastodon is
connected to: managed or external PostgreSQL, Redis and Elasticsearch/OpenSearch work as they
are. The only direct reads are the database itself (read-only user) and, if set,
`NUMEROS_SEARCH_URL` for the index size.

### Platform

`NUMEROS_PLATFORM` adds the platform to the versions table: `kubernetes` reads the cluster's
version (any service account may; the distribution shows when the version says it: RKE2, K3s, EKS,
GKE), and `rancher` reads Rancher's `server-version` setting, which needs `get` on that one
`settings.management.cattle.io` object (a ClusterRole with `resourceNames: [server-version]`).

### Backup modules

`NUMEROS_BACKUP_SOURCES` lists the backup cards, in order (`velero databasus`); empty, the
section is left out. Each module reads its own variables, and every card's texts can be
changed with `NUMEROS_<MODULE>_TITLE`, `_TITLE_EN` and `_SUB`.

| Module | Variables | Reads |
|---|---|---|
| `velero` | `NUMEROS_VELERO_SCHEDULE` (required), `_NAMESPACE` (`velero`), `_APP_NAMESPACE` (the instance's namespace, for its volume size) | the schedule's TTL and cron, its backups, and the volume sizes of the newest completed one, through the Kubernetes API with the pod's service account: give it `get`/`list` on `schedules`, `backups` and `podvolumebackups` (`velero.io`) in Velero's namespace |
| `databasus` | `NUMEROS_DATABASUS_URL`, `_EMAIL`, `_PASSWORD`, `_DATABASE` (the database's name in Databasus) | the database's backup config and its logical backups, with a `WORKSPACE_VIEWER` account |

A new tool is one type with `collect(ctx) (*Backup, error)` and an entry in `backupModules`
(`backup.go`); the page only knows about the `Backup` it returns (retention in days or as a
count, daily time, last run, count, sizes).

The database user only needs `SELECT` on: `accounts`, `account_stats`, `users`, `statuses`,
`notifications`, `favourites`, `follows`, `preview_cards`, `preview_cards_statuses`,
`media_attachments`, `polls`, `reports`, `oauth_applications`, `instances` and, for `/minhaconta`,
`mentions`. Make it read-only:

```sql
CREATE ROLE numeros LOGIN PASSWORD '...';
ALTER ROLE numeros SET default_transaction_read_only = on;
ALTER ROLE numeros SET statement_timeout = '60s';
GRANT SELECT ON accounts, account_stats, users, statuses, notifications, favourites, follows,
  preview_cards, preview_cards_statuses, media_attachments, polls, reports, oauth_applications, instances,
  mentions TO numeros;
```

It works through pgBouncer in transaction mode (simple protocol, one connection).

## Architecture

```
            every hour (:07)                     visits
 Mastodon DB ──read-only──▶ collector ──▶ snapshot ◀── page / numeros.json
 admin API  ──dashboard──▶     │          (memory + file)
 OpenSearch ──_cat──────▶      │
```

- The collector is the only thing touching the database: one connection, one read-only
  transaction per block, 60 s per query. A visit only reads the last snapshot.
- Periods are ranges on primary keys, never scans on `created_at` of the big tables: status ids
  are snowflakes (the time is in the id), and the per-account counts go through the
  `(account_id, id)` index. Boosts and mentions received come from `notifications`, much
  smaller than `statuses`. A round on bolha.us (43 M statuses) takes a few seconds of database time.
- A block that fails keeps the previous round's numbers and the page says which.
- No JavaScript: the period switch is CSS. The CSP allows only our stylesheet and images from
  the instance and its media host.

## Testing and security

```sh
go test ./...                                    # unit and page tests
NUMEROS_TEST_DB_HOST=127.0.0.1 NUMEROS_TEST_DB_PORT=5432 NUMEROS_TEST_DB_PASSWORD=pg go test ./...
```

The database test builds the parts of Mastodon's schema numeros reads and checks the privacy
rules above against real queries (non-discoverable, opted-out, bot and suspended accounts, hidden
graphs, content warnings, sensitive media, local-only and expiring posts, single-person apps).
Another one checks the per-account numbers: periods, the 30-day bound, filtered notifications,
suspended and remote accounts, and what the public page may show. The page tests run the whole
sign-in against a fake instance (state, PKCE, token revoked, forged cookies, form tokens) and check
that a public page only shows what it should and goes away when it must.
CI runs gofmt, vet, the tests against PostgreSQL 17, govulncheck and the shared security scanners.

Versions are `X.Y.Z-BUILD` (VERSION + pipeline); every version needs its CHANGELOG entry,
which is also the `/release-notes` page.

## Bot account

The app's account on bolha.us, @numeros, is marked as a bot and uses [`docs/bot-avatar.png`](docs/bot-avatar.png) as its avatar, in the style of the other bolha.us bots.
