# Backend Deployment — VPS (systemd + nginx)

The Go backend runs as a systemd service on a Debian/Ubuntu VPS, fronted by nginx
on port 80. TLS is terminated by the hosting provider (no certbot on the VPS).
The Python audio-processor runs separately on Modal.

- **VPS**: `vps-ac8da96b` (access via `dalang shell vps-ac8da96b` or `dalang exec vps-ac8da96b "<cmd>"`)
- **Service unit**: `/etc/systemd/system/cantus-backend.service`
- **Binary**: `/usr/local/bin/cantus`
- **Env file**: `/opt/cantus/backend.env`
- **Cookies**: `/opt/cantus/secrets/youtube-cookies.txt`
- **Cache scratch**: `/var/lib/cantus/tmp/cache`
- **nginx vhost**: `/etc/nginx/sites-available/cantus`
- **Runs as**: `root` (no dedicated service user — fine for single-tenant VPS)

---

## Part A — From-scratch provisioning

Run once on a fresh VPS.

### 1. Install system dependencies

```bash
apt update
apt install -y \
  ca-certificates curl unzip git \
  ffmpeg rubberband-cli \
  python3 python3-venv python3-pip \
  nginx
```

### 2. Install yt-dlp (isolated venv)

```bash
python3 -m venv /opt/ytdlp
/opt/ytdlp/bin/pip install --upgrade pip yt-dlp
ln -sf /opt/ytdlp/bin/yt-dlp /usr/local/bin/yt-dlp
yt-dlp --version
```

### 3. Install Deno (mandatory for yt-dlp YouTube extraction)

YouTube's JS challenges can't be solved by yt-dlp's built-in regex interpreter
anymore. Without Deno, every download fails with `Sign in to confirm you're not
a bot.`

```bash
curl -fsSL https://github.com/denoland/deno/releases/latest/download/deno-x86_64-unknown-linux-gnu.zip -o /tmp/deno.zip
unzip -q /tmp/deno.zip -d /usr/local/bin
rm /tmp/deno.zip
deno --version
```

### 4. Create runtime directories

```bash
mkdir -p /opt/cantus/secrets /var/lib/cantus/tmp/cache
chmod 700 /opt/cantus/secrets
```

### 5. Drop the YouTube cookies file

Export cookies from your browser (e.g. with the "Get cookies.txt LOCALLY"
extension) and copy them up:

```bash
# from your laptop
dalang shell vps-ac8da96b   # then paste contents into /opt/cantus/secrets/youtube-cookies.txt
# or scp via the provider if available
chmod 600 /opt/cantus/secrets/youtube-cookies.txt
```

### 6. Write the env file

```bash
cat > /opt/cantus/backend.env <<'EOF'
PORT=8080
ALLOWED_ORIGINS=https://cantus.pages.dev
MAX_CONCURRENT_JOBS=3
VIDEO_ID_SIGNING_KEY=<openssl rand -hex 32 — generate fresh, never reuse>

STORAGE_BACKEND=r2
R2_ACCOUNT_ID=...
R2_ACCESS_KEY_ID=...
R2_SECRET_ACCESS_KEY=...
R2_BUCKET=cantus-cache
R2_PRESIGN_TTL_SECONDS=600

PROCESSOR_URL=https://<your-modal-app>.modal.run
PROCESSOR_TIMEOUT_SECONDS=180

CACHE_DIR=/var/lib/cantus/tmp/cache
AUDIO_TMP_DIR=/var/lib/cantus/tmp

YT_DLP_COOKIES_PATH=/opt/cantus/secrets/youtube-cookies.txt
EOF
chmod 600 /opt/cantus/backend.env
```

**Critical**: keep `PROCESSOR_URL` on a single line — no wrapping. If a literal
newline ends up mid-URL, the backend silently falls back to
`http://localhost:8090` and every `/api/preview-stems` and `/api/generate` will
return 502 with `connection refused`. Verify with:

```bash
grep PROCESSOR_URL /opt/cantus/backend.env | cat -A   # only one trailing $ allowed
```

### 7. Build + upload the Go binary

On your laptop, inside `backend/`:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o cantus ./cmd/server

# upload (use whatever transport your provider exposes; example uses dalang)
dalang shell vps-ac8da96b
# in the shell, paste-receive or use the provider's file UI to drop the binary
# at /usr/local/bin/cantus
chmod 755 /usr/local/bin/cantus
```

### 8. systemd unit

```bash
cat > /etc/systemd/system/cantus-backend.service <<'EOF'
[Unit]
Description=Cantus Go backend
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
EnvironmentFile=/opt/cantus/backend.env
ExecStart=/usr/local/bin/cantus
WorkingDirectory=/var/lib/cantus
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now cantus-backend
systemctl status cantus-backend --no-pager
```

### 9. nginx reverse proxy

```bash
cat > /etc/nginx/sites-available/cantus <<'EOF'
server {
    listen 80 default_server;
    server_name _;

    # Provider terminates TLS, then proxies to this VPS over HTTP.
    set_real_ip_from 0.0.0.0/0;
    real_ip_header X-Forwarded-For;

    # SSE needs these — without them /api/status/:jobId buffers and breaks.
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 300s;
    proxy_send_timeout 300s;

    client_max_body_size 50m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Connection "";
    }
}
EOF

ln -sf /etc/nginx/sites-available/cantus /etc/nginx/sites-enabled/cantus
rm -f /etc/nginx/sites-enabled/default
nginx -t && systemctl reload nginx
```

### 10. Verify

```bash
curl -fsS http://localhost:8080/health   # direct
curl -fsS http://localhost/health         # via nginx
# from your laptop, hit the provider's HTTPS URL
```

---

## Part B — Deploying new changes

Day-to-day workflow: build the binary locally, ship it, restart the service.

### 1. Build locally

From the repo root on your laptop:

```bash
cd backend
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o cantus ./cmd/server
ls -lh cantus   # sanity check; ~12 MB
```

### 2. Ship it to the VPS

Use whatever file transport `dalang` exposes (UI upload, `dalang cp`, or paste
via `dalang shell`). Land it at `/usr/local/bin/cantus`. Keep ownership/perms
intact — `root:root 755`.

If you only have `dalang shell`, the quickest path is base64 piping:

```bash
# laptop
base64 backend/cantus > /tmp/cantus.b64
# paste contents into a shell on the VPS:
dalang shell vps-ac8da96b
# inside the VPS:
cat > /tmp/cantus.b64 <<'EOF'
<paste>
EOF
base64 -d /tmp/cantus.b64 > /usr/local/bin/cantus
chmod 755 /usr/local/bin/cantus
rm /tmp/cantus.b64
```

### 3. Restart the service

```bash
dalang exec vps-ac8da96b "systemctl restart cantus-backend && sleep 2 && systemctl is-active cantus-backend && curl -fsS http://localhost:8080/health"
```

Expected output: `active` then `{"status":"ok"}`.

### 4. Tail logs to confirm

```bash
dalang exec vps-ac8da96b "journalctl -u cantus-backend -n 50 --no-pager"
```

Look for `backend listening` (or equivalent) and no fatal lines.

---

## Frontend

Frontend deploys are unchanged — `git push origin master` and Cloudflare Pages
rebuilds from `frontend/`. Nothing to do on the VPS.

---

## Modal (Python audio-processor)

Deploys separately, not via this flow:

```bash
cd audio-processor-gpu
modal deploy modal_app.py
```

The printed `*.modal.run` URL goes into `PROCESSOR_URL` in `/opt/cantus/backend.env`
on the VPS. Update it and `systemctl restart cantus-backend` if the URL changes.

---

## Common operations

| Task | Command |
|---|---|
| Tail live logs | `dalang exec vps-ac8da96b "journalctl -u cantus-backend -f"` |
| Restart backend | `dalang exec vps-ac8da96b "systemctl restart cantus-backend"` |
| Reload nginx | `dalang exec vps-ac8da96b "nginx -t && systemctl reload nginx"` |
| Rotate signing key | edit `/opt/cantus/backend.env`, restart; outstanding sigs invalidate (users re-search) |
| Update yt-dlp | `dalang exec vps-ac8da96b "/opt/ytdlp/bin/pip install --upgrade yt-dlp"` |
| Refresh cookies | overwrite `/opt/cantus/secrets/youtube-cookies.txt`, no restart needed |
| Update Modal URL | edit `PROCESSOR_URL` in env file, `systemctl restart cantus-backend` |

---

## Gotchas

- **No Deno = no YouTube**. If extraction starts failing with bot-detection
  errors after a system update wiped `/usr/local/bin/deno`, reinstall it (step 3).
- **`PROCESSOR_URL` line wrap**: a stray newline in the env file silently routes
  to `localhost:8090`. Verify with `grep PROCESSOR_URL /opt/cantus/backend.env | cat -A`.
- **Cookies expire**. When yt-dlp starts 403'ing for everything, re-export from
  the browser and overwrite the file.
- **Memory**: backend idles ~12 MB but spikes during rubberband shifts. With
  ~2 GB RAM total, keep `MAX_CONCURRENT_JOBS` ≤ 3.
- **SSE buffering off in nginx is mandatory**. Without it, `/api/status/:jobId`
  hangs until the job completes instead of streaming progress.
- **TLS is handled upstream** by the provider. Don't run certbot on the VPS;
  the nginx config listens on plain port 80.
- **Binary is static** (`CGO_ENABLED=0`). No glibc version mismatches between
  build and run hosts.
