# Deploying to Fly.io and Neon

Yorm runs as one Fly machine (the Go server, which also serves the frontend), with a 1 GB Fly volume for uploaded maps and pictures and a Neon Postgres database. It costs a few dollars a month.

## One-time setup

### 1. Neon (the database)

1. Sign up at https://neon.tech.
2. Create a project: any name, the default Postgres version, region **AWS US East 1 (N. Virginia)** (next to Fly's `iad`).
3. On the project dashboard, click **Connect**, turn **Connection pooling off**, and copy the connection string. It looks like:
   `postgresql://neondb_owner:…@ep-something-123456.us-east-1.aws.neon.tech/neondb?sslmode=require`

   Use the direct connection, not the pooled one (whose host has `-pooler` in it): the server uses prepared statements, which Neon's pooler doesn't support.

Keep the string private. It contains the database password.

### 2. Fly (the server)

Install the CLI and sign in (Fly asks for a card):

```sh
curl -L https://fly.io/install.sh | sh     # then follow its note to add fly to your PATH
fly auth login                             # or: fly auth signup
```

Create the app, its volume, and its secrets, from the repo's root:

```sh
fly apps create yorm                       # if the name is taken, pick another and put it in fly.toml
fly volumes create yorm_data --region iad --size 1 --yes
fly secrets set --stage DATABASE_URL='<the Neon connection string>'
fly secrets set --stage YORM_TOKEN_SECRET="$(openssl rand -base64 48)"
```

`YORM_TOKEN_SECRET` signs everyone's join tokens. Never reuse the development one, and don't change it later: that signs everyone out of every session.

### 3. Deploy

```sh
fly deploy
```

The first deploy builds the image on Fly's builders (a few minutes), runs the database migrations, and starts the server. Then open `https://<app>.fly.dev`.

## Later

- **Deploy a new version:** `fly deploy`.
- **Logs:** `fly logs`.
- **Status:** `fly status`. The machine sleeps when nobody's connected and wakes on the next visit (the first page load then takes a few seconds).
- **Backups:** Neon keeps a restore history for the database. Fly snapshots the volume daily and keeps 5 days of snapshots.
- **Your own domain:** point a DNS record at the app, then `fly certs add play.example.com`.

Keep it to **one machine** (`fly scale count 1`): each session lives in that machine's memory, and two machines would fight over the same sessions.
