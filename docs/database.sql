-- numeros: the read-only database user. Run as a superuser (or the database owner)
-- on the Mastodon database. Safe to run again: it only adds what is missing, so it is
-- also how you upgrade (new versions may read new tables).
--
--   psql -d mastodon_production -f docs/database.sql
--   psql -d mastodon_production -c '\password numeros'     -- a new user: set its password
--
-- -v password="'...'" sets it in the same run, but leaves it in the shell history.
-- Without it, an existing user keeps its password.

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'numeros') THEN
    CREATE ROLE numeros LOGIN;
  END IF;
END $$;

\if :{?password}
ALTER ROLE numeros PASSWORD :password;
\endif

-- Never writes, never runs long. (No CONNECTION LIMIT: behind pgBouncer the pool keeps idle
-- server connections, more than the two numeros opens.)
ALTER ROLE numeros SET default_transaction_read_only = on;
ALTER ROLE numeros SET statement_timeout = '60s';
ALTER ROLE numeros CONNECTION LIMIT -1;

SELECT format('GRANT CONNECT ON DATABASE %I TO numeros', current_database()) \gexec
GRANT USAGE ON SCHEMA public TO numeros;

-- The main page (the hourly round). Whole tables where every column may be read anyway;
-- only the columns numeros uses where the table holds secrets (accounts: each local
-- account's signing key; users: e-mail, password hash, 2FA; oauth_applications: client
-- secrets; reports: the reporters' comments).
REVOKE SELECT ON accounts, users, oauth_applications, reports FROM numeros;
GRANT SELECT (id, username, domain, display_name, avatar_file_name, url, uri, note, created_at, actor_type,
  discoverable, hide_collections, memorial, moved_to_account_id, silenced_at, suspended_at, sensitized_at)
  ON accounts TO numeros;
GRANT SELECT ON account_stats, statuses, notifications, favourites, follows,
  preview_cards, preview_cards_statuses, media_attachments, polls, instances TO numeros;
GRANT SELECT (account_id, approved, confirmed_at, disabled, created_at, current_sign_in_at) ON users TO numeros;
GRANT SELECT (id, name) ON oauth_applications TO numeros;
GRANT SELECT (id, created_at) ON reports TO numeros;

-- /minhaconta (0.4.0): mentions; the extra cards (0.5.0): best posts, hashtags, bookmarks,
-- poll votes. Leave these out and those parts of /minhaconta are simply empty.
GRANT SELECT ON mentions, status_stats, tags, statuses_tags, bookmarks, poll_votes TO numeros;

-- What it ended up with:
SELECT table_name, CASE WHEN count(*) FILTER (WHERE t) > 0 THEN 'all columns' ELSE string_agg(column_name, ', ') END
  FROM (SELECT table_name, true AS t, NULL AS column_name FROM information_schema.role_table_grants
          WHERE grantee = 'numeros' AND privilege_type = 'SELECT'
        UNION ALL
        SELECT table_name, false, column_name FROM information_schema.column_privileges
          WHERE grantee = 'numeros' AND privilege_type = 'SELECT'
          AND table_name NOT IN (SELECT table_name FROM information_schema.role_table_grants
                                 WHERE grantee = 'numeros' AND privilege_type = 'SELECT')) g
  GROUP BY 1 ORDER BY 1;
