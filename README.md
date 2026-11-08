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
- **`#nonumeros` in the bio** takes anyone out at the next round, also from other servers, whose
  bios are HTML (`#<span>nonumeros</span>`).
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
  `__Host-` cookie for 4 hours; the forms (public page, sign out) carry a token tied to it. Signing
  out clears the cookie; a copy made before keeps working until it expires (there is no server-side
  session list), and it only ever shows that person's own numbers.
- **Only the person sees it**, unless they open a **public page** at `/@username`. Off by default.
  The public page only shows the people who talk to them under the main page's rules
  (discoverable, no `#nonumeros`, in good standing); its counts only cover what anyone can already
  see on the instance (public and unlisted posts, not local-only, and the favs they got; favs the
  person gives stay off, Mastodon keeps them private); it only counts mentions made in public or
  unlisted posts (not local-only ones), so it never tells who sends the person direct,
  followers-only or local-only messages; only
  links from posts the main page could
  show, and no follower numbers when they hide their social graph. `#nonumeros` in the bio, a
  limited or moved account, or a renamed one take it down within 15 minutes (the public cache),
  whatever the setting; a slow database never makes it serve older numbers than that.
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
- **More, only on `/minhaconta`** (0.5.0), never on the public page: their media of the last 30 days
  (photos, videos, GIFs, audio, how many have alt text, the space everything they uploaded takes,
  and a grid of the newest photos, leaving out direct messages and anything behind a content
  warning); their 5 best posts by favs and boosts and the one with most replies (no direct
  messages; a post behind a warning shows only the warning); how they post (public, unlisted,
  followers-only, local-only, direct; content warnings, edits, self-deleting posts, replies against
  new posts, and how many direct messages came in, the count only); when (a weekday × hour map,
  streaks over 90 days), languages, apps and hashtags (not from posts behind a warning); who they fav and boost the most, their new
  followers, and the accounts they follow for over 30 days that didn't fav, boost or mention them
  in that time; their polls and the votes on them, the polls they voted on, and their bookmarks
  with the servers they come from. Who blocked or muted them, reports, unfollows and who they
  exchange direct messages with are left out on purpose. A part whose table the database user
  can't read (an older grant list) is just empty.

## Install

numeros is one container that needs a read-only user on the Mastodon database. Everything else
is optional. The image is public: `registry.gutocarvalho.net/images/numeros:<tag>`, where the tag
is a version (`0.5.0`), a version and build (`0.5.0-1325`) or `latest`. It is distroless, runs as
user 65532, listens on `:8080` and is built for `linux/amd64`; on ARM, build it from this repo
(`docker build -t numeros .`, it only needs Docker). Five steps:

### 1. The database user

[`docs/database.sql`](docs/database.sql) creates the user `numeros`, makes it read-only
(`default_transaction_read_only`, 60 s `statement_timeout`) and grants
`SELECT` on what it reads and nothing else: whole tables where everything is readable anyway,
and only the needed columns of `accounts` (no signing keys), `users` (no e-mail, password hash or 2FA), `oauth_applications`
(no client secrets) and `reports` (no comments). Run it as a superuser on the Mastodon database,
then set the password interactively (so it stays out of the shell history):

```sh
psql -d mastodon_production -f docs/database.sql
psql -d mastodon_production -c '\password numeros'
```

It is safe to run again, and that is also how you upgrade: new versions may read new tables
(0.5.0 added `status_stats`, `tags`, `statuses_tags`, `bookmarks` and `poll_votes`, and narrowed
`accounts`, `users`, `oauth_applications` and `reports` to columns). The last query lists what the user can
read, table by table.

It works through pgBouncer in transaction mode (simple protocol, one connection for the hourly
round and one for visits). Point `NUMEROS_DB_HOST`/`_PORT` at pgBouncer if you have one; if
pgBouncer has its own user list (`auth_file`), add `numeros` there too.

There is nothing to populate: numeros has no database of its own. It reads Mastodon's tables,
keeps the last round in memory (and in `NUMEROS_SNAPSHOT`, if set) and the first round runs
right after it starts.

### 2. The admin token (optional)

For versions and space used (Mastodon, Ruby, PostgreSQL, Redis, search, media), give numeros a
token that can only read the dashboard:

1. In *Administration → Roles*, create a role with only **View Dashboard**.
2. Give it to an account (a bot such as @numeros is a good fit).
3. As that account, in *Preferences → Development*, create an application with the `admin:read`
   scope and use its access token as `NUMEROS_ADMIN_TOKEN`.

Without it the page shows the database's own version and size and leaves the rest out.

### 3. Sign-in, for `/minhaconta` (optional)

Register an OAuth app on the instance and generate the session key:

```sh
curl -s https://your.instance/api/v1/apps \
  -F client_name=numeros -F scopes=profile \
  -F redirect_uris=https://numeros.your.instance/auth/callback \
  -F website=https://numeros.your.instance          # prints client_id and client_secret
openssl rand -base64 48                              # NUMEROS_SESSION_SECRET
```

Set `NUMEROS_CLIENT_ID`, `NUMEROS_CLIENT_SECRET`, `NUMEROS_SESSION_SECRET`, `NUMEROS_PUBLIC_URL`
(this app's https URL, no path) and `NUMEROS_PAGES_FILE` on a persistent volume. Without
`NUMEROS_CLIENT_ID` there is no `/minhaconta` and nothing about sign-in runs.

### 4. Run it

numeros has to reach PostgreSQL (or pgBouncer), and PostgreSQL has to let `numeros` in: if
`pg_hba.conf` lists users or addresses, add a line for it, e.g.
`host mastodon_production numeros 172.16.0.0/12 scram-sha-256` for Docker's networks. A managed
database that requires TLS needs `NUMEROS_DB_SSLMODE=require`.

**Docker.** `/data` keeps the last round across restarts (and the public pages list, with sign-in):

```sh
docker run -d --name numeros --restart unless-stopped -p 8080:8080 -v numeros-data:/data \
  -e NUMEROS_INSTANCE=https://your.instance \
  -e NUMEROS_MEDIA_URL=https://files.your.instance \
  -e NUMEROS_DB_HOST=db.internal -e NUMEROS_DB_PASSWORD=... \
  -e NUMEROS_SNAPSHOT=/data/snapshot.json \
  registry.gutocarvalho.net/images/numeros:0.5.1
```

Mastodon's own `docker-compose.yml` keeps PostgreSQL on an internal network with no published
port: add `--network <project>_internal_network` (`docker network ls` shows its name) and use
`NUMEROS_DB_HOST=db`, the service name. Or as a service in that same file:

```yaml
  numeros:
    image: registry.gutocarvalho.net/images/numeros:0.5.1
    restart: unless-stopped
    networks: [internal_network, external_network]  # the database, and the admin API / sign-in
    ports: ["127.0.0.1:8080:8080"]
    volumes: [numeros-data:/data]
    env_file: numeros.env   # NUMEROS_DB_HOST=db, NUMEROS_DB_PASSWORD=..., NUMEROS_INSTANCE=...
# and under the top-level volumes:  numeros-data: {}
```

A bind mount instead of a named volume has to belong to the app's user:
`sudo chown 65532:65532 /srv/numeros`. Add the variables of steps 2 and 3 as you need them, and
put your TLS reverse proxy in front of `:8080`.

**Kubernetes.** [`docs/kubernetes.yaml`](docs/kubernetes.yaml) has the namespace, a small volume,
the deployment (non-root, read-only root filesystem, `fsGroup` so it can write the volume, one
replica: the pages list is a file) and the service. Change the lines marked `CHANGE`, then:

```sh
kubectl create namespace numeros
kubectl -n numeros create secret generic numeros-env --from-literal=NUMEROS_DB_PASSWORD='...'
kubectl apply -f docs/kubernetes.yaml
kubectl -n numeros logs deploy/numeros   # collected in …, stale: []
```

The comment on the secret in that file shows the optional keys (admin token, sign-in). Route your
ingress or gateway to the `numeros` service on port 80. bolha.us's own manifests, with network
policies and the backup section's RBAC, are in
[open/gcn/k8s-config](https://gitlab.gutocarvalho.net/open/gcn/k8s-config) under `apps/numeros`.

### 5. Check it

- `curl -s localhost:8080/healthz` answers `ok`, and `/` shows the page.
- The log shows the first round (`collected in …, stale: []`) within a minute; a block the user can't
  read says which table, and the page says which part is out of date. After a restart there may be
  no new round until the next hour: the last one was kept in `NUMEROS_SNAPSHOT`.
- `snapshot: … permission denied` in the log: `/data` isn't writable by user 65532 (see step 4).
- `password authentication failed` or `no pg_hba.conf entry`: step 1's password, or the
  `pg_hba.conf` line of step 4.
- With sign-in on, the log says `sign-in on: N public pages`, and `/minhaconta` shows
  "Sign in with …".
- If `/minhaconta` logs `left out: permission denied`, run step 1 again: the user is missing a
  grant of a newer version.

### All variables

| Variable | Default | |
|---|---|---|
| `NUMEROS_INSTANCE` | `https://bolha.us` | public URL of the instance |
| `NUMEROS_INTERNAL_URL` | | reach the instance inside the cluster (e.g. `http://web.bolha-us.svc.cluster.local:3000`); the public Host is kept |
| `NUMEROS_MEDIA_URL` | `$INSTANCE/system` | where media and avatars are served (`S3_ALIAS_HOST`) |
| `NUMEROS_DB_HOST` / `_PORT` / `_NAME` / `_USER` | `127.0.0.1` / `5432` / `mastodon_production` / `numeros` | |
| `NUMEROS_DB_PASSWORD` | | required |
| `NUMEROS_DB_SSLMODE` | `disable` | `require`, `verify-full`… for a database that wants TLS |
| `NUMEROS_LISTEN` | `:8080` | address to listen on |
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

#### Your instance

Nothing about bolha.us is built in; the page takes its names from the config.

| Variable | Default | |
|---|---|---|
| `NUMEROS_NAME` | the instance's host | used in the title, the header and the texts (`{name}`) |
| `NUMEROS_US` / `NUMEROS_US_EN` | `NUMEROS_NAME` | how the page refers to the instance in "servers talking to …" ("a bolha" / "the bubble") |
| `NUMEROS_HEADLINE` / `_EN` | "{name} em <em>números</em>." | the `<h1>`, as HTML |
| `NUMEROS_MEDIA_STORAGE` | | shown as "Media (X)", e.g. `Wasabi` |
| `NUMEROS_PARENT_NAME` / `_URL` | | "part of …" link in the footer |
| `NUMEROS_BACKUP_NOTE` / `_EN` | | a note under the backup cards |

#### External services

Versions and sizes of PostgreSQL, Redis, the search cluster and the media come from the
instance's own admin dashboard (`NUMEROS_ADMIN_TOKEN`), which reports whatever Mastodon is
connected to: managed or external PostgreSQL, Redis and Elasticsearch/OpenSearch work as they
are. The only direct reads are the database itself (read-only user) and, if set,
`NUMEROS_SEARCH_URL` for the index size.

#### Platform

`NUMEROS_PLATFORM` adds the platform to the versions table: `kubernetes` reads the cluster's
version (any service account may; the distribution shows when the version says it: RKE2, K3s, EKS,
GKE), and `rancher` reads Rancher's `server-version` setting, which needs `get` on that one
`settings.management.cattle.io` object (a ClusterRole with `resourceNames: [server-version]`).

#### Backup modules

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
- No JavaScript of its own: the period switch is CSS. The CSP allows our stylesheet, images from
  the instance and its media host, and same-origin scripts only so that a bar a reverse proxy
  injects (bolha.us's menu, `/__bolha/bar.js`) can load; every route sends `nosniff` and none
  returns a script type.

## Testing and security

```sh
go test ./...                                    # unit and page tests
NUMEROS_TEST_DB_HOST=127.0.0.1 NUMEROS_TEST_DB_PORT=5432 NUMEROS_TEST_DB_PASSWORD=pg go test ./...
# a throwaway PostgreSQL for that: docker run -d --name numeros-pg -p 5432:5432 -e POSTGRES_PASSWORD=pg -e POSTGRES_DB=numeros_test postgres:17
# then, the grants are enough: every query again as the numeros user
psql -h 127.0.0.1 -U postgres -d numeros_test -v password="'test'" -f docs/database.sql
NUMEROS_TEST_DB_HOST=127.0.0.1 NUMEROS_TEST_APP_PASSWORD=test go test -run TestReadsAsTheAppUser ./...
```

The database test builds the parts of Mastodon's schema numeros reads and checks the privacy
rules above against real queries (non-discoverable, opted-out, bot and suspended accounts, hidden
graphs, content warnings, sensitive media, local-only and expiring posts, single-person apps).
Another one checks the per-account numbers (and one more the extra cards of 0.5.0, including the
page staying up when a newer grant is missing): periods, the 30-day bound, filtered notifications,
suspended and remote accounts, and what the public page may show. The page tests run the whole
sign-in against a fake instance (state, PKCE, token revoked, forged cookies, form tokens) and check
that a public page only shows what it should and goes away when it must.
CI runs gofmt, vet, the tests against PostgreSQL 17, govulncheck and the shared security scanners.

Versions are `X.Y.Z-BUILD` (VERSION + pipeline); every version needs its CHANGELOG entry,
which is also the `/release-notes` page.

## Bot account

The app's account on bolha.us, @numeros, is marked as a bot and uses [`docs/bot-avatar.png`](docs/bot-avatar.png) as its avatar, in the style of the other bolha.us bots.
