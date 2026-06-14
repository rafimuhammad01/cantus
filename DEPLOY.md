# Deploy cheatsheet

Three pieces ship independently:

| Piece | Target | Tool |
|---|---|---|
| Go backend + bgutil sidecar | AWS EC2 (t3.micro free tier) | docker compose |
| Python GPU service | Modal A10G | `modal deploy` |
| Frontend | Cloudflare Pages | git push |

Storage is Cloudflare R2 (S3 API). DNS/edge is Cloudflare in front of EC2.

---

## 1. Cloudflare R2

1. Create an R2 bucket, e.g. `cantus-cache`.
2. Create an R2 API token scoped to that bucket, R/W. Save the Account ID, Access Key ID, Secret Access Key.

---

## 2. Modal GPU service

```bash
cd audio-processor-gpu
pip install modal
modal token new          # one-time auth

modal run seed_models.py # downloads the BS-Roformer ckpt into the Volume (~640 MB, one-time)
# Copy the printed sha256 into EXPECTED_SHA256 in seed_models.py, then commit.

modal deploy modal_app.py
# Note the URL it prints; that's PROCESSOR_URL for the Go backend.
```

Cost guardrails are already in `modal_app.py` (`min_containers=0`, `scaledown_window=30`, `timeout=120`, `enable_memory_snapshot=True`). Add a $25 spend alert in Modal Settings → Billing.

---

## 3. EC2 (Go backend + bgutil sidecar)

### One-time setup

1. Launch a t3.micro Amazon Linux 2023 (or Ubuntu 24.04) instance. Free tier OK.
2. Security group: inbound TCP 22 from your IP, TCP 80/443 from Cloudflare IP ranges only (or 0.0.0.0/0 if Cloudflare proxying is on and you trust Cloudflare to terminate TLS).
3. SSH in. Install Docker:
   ```bash
   sudo dnf install -y docker        # Amazon Linux 2023
   sudo systemctl enable --now docker
   sudo usermod -aG docker ec2-user
   # Log out + back in for the group change to apply.
   ```
4. Install Docker Compose plugin (`docker compose` v2).
5. Install Caddy or nginx if you want TLS terminated on the box; otherwise let Cloudflare handle TLS and run plain :8080 behind the CF proxy.

### Deploy

```bash
git clone <repo> /opt/cantus && cd /opt/cantus
cp backend/.env.example backend/.env
# Edit backend/.env — required values:
#   VIDEO_ID_SIGNING_KEY=$(openssl rand -hex 32)
#   STORAGE_BACKEND=r2
#   R2_ACCOUNT_ID=...
#   R2_ACCESS_KEY_ID=...
#   R2_SECRET_ACCESS_KEY=...
#   R2_BUCKET=cantus-cache
#   PROCESSOR_URL=https://<your-modal-deploy>.modal.run
#   ALLOWED_ORIGINS=https://<your-pages-domain>
# YT_DLP_POT_BASE_URL is set automatically by docker-compose.

docker compose build
docker compose up -d
docker compose logs -f backend
curl http://localhost:8080/health   # → {"status":"ok"}
```

### Updating

```bash
cd /opt/cantus
git pull
docker compose build backend
docker compose up -d backend
```

### Watch for

- yt-dlp 429 / bot-challenge errors → check `docker compose logs bgutil` for the PoT sidecar; YouTube may be rate-limiting the EC2 IP. Fallback if persistent: Hetzner VPS (deferred).
- R2 unexpected egress costs — egress *from* R2 is free, but check if your Modal region is causing extra hops.
- Modal cold start > 30s — bump `scaledown_window` in `modal_app.py` if usage warrants warm idle.

---

## 4. Cloudflare Pages (frontend)

1. Create a Pages project linked to the repo.
2. Build command: `npm run build`. Output dir: `frontend/dist`. Root dir: `frontend`.
3. Set env var: `VITE_API_BASE_URL=https://<your-cf-proxied-ec2-domain>` (only needed if `frontend/src/services/api.ts` is updated to use it; today it uses relative `/api/*` paths and assumes same-origin via CF proxy).
4. Add a Pages-Functions or Workers route that proxies `/api/*` from the Pages domain to EC2 (or use CF's "Origin Rules" / "Workers" to rewrite). Same-origin avoids CORS entirely.

---

## What is deliberately deferred

- Cloudflare Turnstile + WAF rate limits.
- Session-bound HMAC sig payload.
- Hetzner VPS fallback for yt-dlp.
- R2 LRU eviction.

These are anti-abuse hardening; layer them on after you confirm the happy path works end-to-end.
