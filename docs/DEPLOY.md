# Deploy

Cantus deploys manually across three independent tracks. There is no CI/CD.

| Track | What | Where | How |
|---|---|---|---|
| Frontend | Vue app | Cloudflare Pages | auto on `git push origin master` |
| Backend | Go API | EC2 (Docker) | buildx → GHCR → ssh + compose pull |
| GPU service | Python BS-Roformer + CREPE | Modal | `modal deploy` from `audio-processor-gpu/` |

Storage is Cloudflare R2 (presigned URLs, set `STORAGE_BACKEND=r2` in the EC2 env file).

## Track 1 — Frontend (Cloudflare Pages)

Auto-deploys on push to `master`. ~1–2 min build.

```bash
git push origin master
```

CF Pages watches `frontend/` and runs the build. No further action.

## Track 2 — Backend (EC2 via GHCR)

EC2 host: `54.169.205.65`
SSH key: `~/Downloads/cantus-backend.pem`
Public URL: `https://54-169-205-65.sslip.io` (sslip.io DNS + Caddy + Let's Encrypt, auto-renewed)
Image: `ghcr.io/rafimuhammad01/cantus-backend:latest`
Compose: `/opt/cantus/docker-compose.yml`
Env file: `/opt/cantus/backend/.env`

### Steps

1. **Docker Desktop must be running locally** (buildx uses it). If `docker ps` errors on the socket, start Docker Desktop first.

2. **Build + push linux/amd64 image** — EC2 is amd64; an M-series Mac default build would push arm64 and fail to start.

   ```bash
   cd backend && docker buildx build --platform linux/amd64 \
     -t ghcr.io/rafimuhammad01/cantus-backend:latest --push .
   ```

   Requires prior `docker login ghcr.io`. The `cantus-builder` buildx context is preconfigured.

3. **Pull + recreate on EC2:**

   ```bash
   ssh -i ~/Downloads/cantus-backend.pem ec2-user@54.169.205.65 \
     "cd /opt/cantus && sudo docker compose pull backend && sudo docker compose up -d backend"
   ```

4. **Verify:**

   ```bash
   curl -fsS https://54-169-205-65.sslip.io/health   # expect {"status":"ok"}
   ```

   Inside EC2:

   ```bash
   sudo docker compose -f /opt/cantus/docker-compose.yml logs --tail 5 backend
   ```

   Look for `"backend listening"` on port 8080 with no fatals.

## Track 3 — GPU service (Modal)

Deployed separately, not part of the backend flow. From `audio-processor-gpu/`:

```bash
modal deploy modal_app.py
```

Modal prints a stable URL. If it changes (rare), update `PROCESSOR_URL` in `/opt/cantus/backend/.env` on EC2 and restart the backend.

The BS-Roformer checkpoint is seeded once into the Modal Volume via:

```bash
modal run seed_models.py
```

Subsequent cold starts read from the Volume (~5–10s).

## Gotchas

- **Don't run `--help` against the running container** to "sanity check" — it tries to bind :8080 and fails with `address already in use`. Misleading red herring; use `/health` instead.
- **No `curl`/`wget`/`nc` in the container.** Probe via the host or the sslip.io URL.
- **New env vars** must be added to `/opt/cantus/backend/.env` on EC2 *before* pulling, or the container will fail to start. Diff for new `os.Getenv` calls before deploying.
- **YouTube cookies** live at `/opt/cantus/secrets/youtube-cookies.txt` on EC2 and are mounted into the container; refresh them when yt-dlp starts failing with bot-check errors.
- **Caddy** auto-renews TLS; no manual cert work.
