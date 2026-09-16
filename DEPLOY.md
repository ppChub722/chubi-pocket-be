# Deployment runbook

End-to-end deploy of `chubi-pocket-be` to a VPS, behind Caddy + auto Let's Encrypt TLS.

## Environments

| Environment | Where it runs | Domain | Notes |
|---|---|---|---|
| **local** | Your laptop (`docker-compose.yml`) | `localhost` | Plain HTTP, dev defaults |
| **dev** | VPS (`docker-compose.deploy.yml`) | Free DuckDNS subdomain | HTTPS, internal-only users |
| **prod** | VPS (`docker-compose.deploy.yml`) | Real domain you own | HTTPS, public users |

Architecture: backend on VPS, frontend distributed as APK installed directly on user devices. No web frontend, no FE deployment.

This runbook covers **dev** and **prod** — they share the same compose file and only differ in `.env` values (domain, secrets, `APP_ENV`).

Total time on a fresh box: ~30 min if DNS is ready.

---

## 0. Prerequisites

- A VPS — Ubuntu 22.04 LTS recommended (24.04 also fine), 1 vCPU / 2 GB RAM minimum
- A domain pointing at the VPS:
  - **dev**: free DuckDNS subdomain — register at https://www.duckdns.org (GitHub login)
  - **prod**: real domain you own (Namecheap, Cloudflare Registrar, etc.)
- SSH key on your local machine

---

## 1. Provision and harden the VPS

SSH in as `root` first time. Run these in order.

### 1.1 Create a non-root user

```bash
adduser deploy
usermod -aG sudo deploy
mkdir -p /home/deploy/.ssh
cp ~/.ssh/authorized_keys /home/deploy/.ssh/
chown -R deploy:deploy /home/deploy/.ssh
chmod 700 /home/deploy/.ssh
chmod 600 /home/deploy/.ssh/authorized_keys
```

Test from your laptop in a new terminal: `ssh deploy@<vps-ip>` — must work before continuing.

### 1.2 Lock down SSH

Edit `/etc/ssh/sshd_config`:

```
PermitRootLogin no
PasswordAuthentication no
```

Then `sudo systemctl restart ssh`.

### 1.3 Firewall

```bash
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw --force enable
sudo ufw status
```

Postgres (5432) is **not** opened — it's bound to `127.0.0.1` inside the compose network only.

### 1.4 Install Docker + compose plugin

```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker deploy
# log out + back in so the group takes effect
```

Verify: `docker version` and `docker compose version` should both succeed without sudo.

---

## 2. DNS

Point a record at the VPS public IP.

### Dev (DuckDNS)

1. Sign in at https://www.duckdns.org
2. Pick a subdomain (e.g. `chubipocket-dev`)
3. Set the IP to your VPS public IP, hit "update"

Verify:

```bash
dig chubipocket-dev.duckdns.org +short
# → should print your VPS IP
```

### Prod (real domain)

Add an A record at your registrar:

```
api.chubipocket.com  →  <vps-ip>
```

Wait for propagation (usually < 5 min, can be up to TTL).

```bash
dig api.chubipocket.com +short
```

---

## 3. Upload the code

Two options.

### Option A — git clone (simplest if repo is on GitHub)

```bash
ssh deploy@<vps-ip>
cd ~
git clone <repo-url> chubi-pocket-be
cd chubi-pocket-be
```

### Option B — rsync from your laptop

```powershell
# From the project root on your machine
rsync -avz --exclude '.git' --exclude '.env' --exclude 'backups' `
  ./chubi-pocket-be/ deploy@<vps-ip>:~/chubi-pocket-be/
```

---

## 4. Configure secrets

Run the interactive setup script. Pass `dev` or `prod` to pick the right template.

```bash
cd ~/chubi-pocket-be
chmod +x scripts/setup-vps.sh

# Dev VPS
./scripts/setup-vps.sh dev

# Prod VPS
./scripts/setup-vps.sh prod
```

The script:
- Prompts for `DOMAIN`, `DB_USER` (default), `DB_NAME` (default), `DB_PASSWORD`, `JWT_SECRET`
- Hitting Enter on a secret prompt auto-generates one and prints it ONCE — save those in your password manager
- Sets `APP_ENV` automatically (`development` for dev, `production` for prod)
- Writes `.env` with `chmod 600`
- Refuses to overwrite an existing `.env` — pass `--force` if that's what you want

For pasting from a password manager (e.g. re-deploying on a new VPS), just paste at each prompt instead of hitting Enter.

---

## 5. Deploy

```bash
docker compose -f docker-compose.deploy.yml up -d --build
docker compose -f docker-compose.deploy.yml ps
```

Expected: `db` healthy, `migrate` exited with code 0, `app` running, `caddy` running.

Tail Caddy while it fetches the cert (first boot only, ~30s):

```bash
docker logs -f chubi_pocket_caddy
```

Look for `certificate obtained successfully`.

---

## 6. Verify

```bash
# From the VPS
curl http://localhost/health                           # Caddy redirects to https
curl https://<your-domain>/health                      # → {"status":"ok"}

# From your laptop
curl https://<your-domain>/health
```

End-to-end smoke (works on dev or prod):

```bash
curl -X POST https://<your-domain>/api/v1/auth/register `
  -H "Content-Type: application/json" `
  -d '{"email":"smoke@test.com","password":"TestPassword123","display_name":"smoke"}'
```

---

## 7. Day-2 operations

### Logs

```bash
docker logs -f chubi_pocket_app
docker logs -f chubi_pocket_caddy
```

JSON log rotation is configured (10 MB × 3 files per service).

### Redeploy after code change

```bash
cd ~/chubi-pocket-be
git pull                                                  # or rsync
docker compose -f docker-compose.deploy.yml up -d --build app
```

`db` and `caddy` are not rebuilt unless their config changes. `migrate` re-runs and applies any new migrations idempotently.

### Database shell

```bash
docker exec -it chubi_pocket_db psql -U chubadmin -d chubi_pocket_db
```

### Backup (manual)

```bash
mkdir -p ~/backups
docker exec chubi_pocket_db pg_dump -U chubadmin chubi_pocket_db `
  > ~/backups/chubi_$(date +%Y%m%d_%H%M%S).sql
```

For dev VPS, the provider's free "Manual Backup" snapshot option is enough. Cron a `pg_dump` weekly on prod once you have real users.

### Stop / start / wipe

```bash
docker compose -f docker-compose.deploy.yml stop          # graceful stop, keep data
docker compose -f docker-compose.deploy.yml start         # bring back up
docker compose -f docker-compose.deploy.yml down          # remove containers, KEEP volumes
docker compose -f docker-compose.deploy.yml down -v       # WIPE everything incl. DB data
```

---

## 8. APK side

Once the API is live at `https://<your-domain>`, build the APK with that as the API base URL. Android requires HTTPS — there is no fallback for cleartext to a public domain.

**Two APK builds are reasonable:**
- A **dev APK** pointing at `https://chubipocket-dev.duckdns.org` (for your testing)
- A **prod APK** pointing at `https://api.chubipocket.com` (for users)

Use Flutter build flavors or `--dart-define=API_BASE_URL=...` to switch.

---

## Future work (deferred from initial deploy)

- [ ] Nightly `pg_dump` cron + offsite copy (e.g. rclone to B2/S3) — prod only
- [ ] Rate limit `/auth/login` and `/auth/register` (Gin middleware) — before opening prod to public
- [ ] Uptime monitor on `/health` (UptimeRobot, Better Stack, etc.) — prod only
- [ ] CI/CD: build image in GitHub Actions, push to GHCR, `docker compose pull` on the VPS
- [ ] Monitoring / error tracking (Sentry)

---

## Troubleshooting

**Caddy can't get a cert** → DNS not propagated yet, or port 80 blocked. `dig` the domain, check `ufw status`. DuckDNS sometimes takes a minute after the IP update.

**`migrate` exits non-zero** → check `docker logs chubi_pocket_migrate`. Most often a dirty migration; see [COMMANDS.md](COMMANDS.md) "force version" recipe.

**`app` exits immediately** → `docker logs chubi_pocket_app`. Usually a missing env var or DB connection issue. Confirm `.env` has all required keys.

**`502 Bad Gateway` from Caddy** → `app` container isn't healthy. `docker compose -f docker-compose.deploy.yml ps` and check the app's logs.

**Can't reach API from APK** → confirm the APK is built with `https://` URL (not `http://`). Android blocks cleartext to public domains.
