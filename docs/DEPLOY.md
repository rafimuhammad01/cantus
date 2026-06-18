# Deploy cheatsheet

Three services ship independently. The Go backend now runs on a dalang.io VPS
(not EC2 / Docker anymore). The Python GPU service runs on Modal. The frontend
runs on Cloudflare Pages.

| Piece | Target | Tool |
|---|---|---|
| Go backend | dalang.io VPS (`vps-ac8da96b`) | `go build` + `scp` + `systemctl` |
| Python GPU service | Modal A10G | `modal deploy` |
| Frontend | Cloudflare Pages | `git push` |

Storage is Cloudflare R2 (S3 API). TLS is terminated by dalang's HTTPS edge,
then forwarded to nginx on port 80 of the VPS, then proxied to the Go backend on
:8080.

For from-scratch VPS provisioning (system packages, yt-dlp + Deno, nginx vhost,
systemd unit), see `archive/deploy-vps.md`. This file is the day-to-day cheatsheet.

---

## Backend (Go) — `vps-ac8da96b`

Build locally, ship the binary, restart the service. **Do not scp directly onto
`/usr/local/bin/cantus`** — the kernel returns `ETXTBSY` ("text file busy")
because the running process holds the inode. Always stage in `/tmp` and atomic
swap via `install`.

```bash
# 1. build locally (linux/amd64 static binary)
cd backend
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o out/cantus ./cmd/server
file out/cantus   # must say: ELF 64-bit LSB executable, x86-64

# 2. upload to staging path
dalang scp out/cantus vps-ac8da96b:/tmp/cantus
# (or whichever transport dalang offers; key constraint is NOT /usr/local/bin/cantus)

# 3. atomic swap + restart
dalang exec vps-ac8da96b "install -m 755 /tmp/cantus /usr/local/bin/cantus && systemctl restart cantus-backend && rm /tmp/cantus && sleep 2 && systemctl is-active cantus-backend && curl -fsS http://localhost:8080/health"
```

Expected tail: `active` then `{"status":"ok"}`.

Tail logs:

```bash
dalang exec vps-ac8da96b "journalctl -u cantus-backend -n 50 --no-pager"
```

---

## Audio processor (Python) — Modal

```bash
cd audio-processor-gpu
modal deploy modal_app.py
```

Note the printed `https://*.modal.run` URL. If it changes (e.g. on first
deploy or after rename), update the VPS:

```bash
dalang exec vps-ac8da96b "sed -i 's|^PROCESSOR_URL=.*|PROCESSOR_URL=<new-url>|' /opt/cantus/backend.env && systemctl restart cantus-backend"
```

Verify the URL is on a single line (no embedded newline — that silently makes
the backend fall back to `localhost:8090`):

```bash
dalang exec vps-ac8da96b "grep PROCESSOR_URL /opt/cantus/backend.env | cat -A"
# only one trailing $ allowed
```

Model seeding (one-time, when BS-Roformer ckpt isn't in the Modal Volume yet):

```bash
modal run seed_models.py
```

---

## Frontend — Cloudflare Pages

```bash
git push origin master
```

CF Pages auto-builds from `frontend/` on push. ~1-2 min. No further action.
Vue + Vite, output dir `frontend/dist`.

---

## Deploy everything

When asked to "deploy" without qualification, the order is:

1. **Audio processor** (Modal) — if Python code or `modal_app.py` changed.
   New URL? Update VPS env first.
2. **Backend** (VPS) — if Go code changed.
3. **Frontend** (CF Pages) — `git push origin master` (also pushes any
   backend/audio-processor commits to remote; doesn't trigger their deploys
   though, those are manual above).

Check what changed first:

```bash
git status
git log --oneline origin/master..HEAD
```

Then deploy only the affected pieces. If unclear which piece a commit touched,
the path tells you: `backend/` → VPS, `audio-processor-gpu/` → Modal,
`frontend/` → CF Pages.

---

## Gotchas

- **Text file busy** on backend upload: stage in `/tmp/cantus`, then `install`. Never overwrite `/usr/local/bin/cantus` directly.
- **`status=203/EXEC`** from systemd: binary architecture mismatch (built on Mac without `GOOS=linux GOARCH=amd64`) or missing `chmod 755`. Verify with `file /usr/local/bin/cantus` on the VPS — must say `ELF 64-bit ... x86-64`.
- **`PROCESSOR_URL` line wrap**: stray newline → falls back to `localhost:8090` → every `/api/preview-stems` and `/api/generate` returns 502.
- **30s dalang idle timeout**: any new long-running sync handler must emit keepalive bytes (see `preview_stems.go` pattern) or use async/SSE.
- **Modal URL changes** on first deploy or rename: re-paste into VPS env + restart.
- **CF Pages doesn't deploy backend or Modal** — those are two separate manual steps.

---

## What is deliberately deferred

- Cloudflare Turnstile + WAF rate limits.
- Session-bound HMAC sig payload.
- R2 LRU eviction (relying on bucket lifecycle policy).

These are anti-abuse hardening; layer them on after the happy path is stable.
