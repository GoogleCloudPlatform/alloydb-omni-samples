# Vertex AI Migration Guide (from Google AI Studio API key flow)

This guide explains exactly how to switch this project from:

- Gemini Developer API / AI Studio (`generativelanguage.googleapis.com` + `GEMINI_API_KEY`)

to:

- Vertex AI (`aiplatform.googleapis.com` + Google Cloud auth / ADC)

---

## 1) What changed in this repo

The Customer routing path now uses Vertex AI authentication and endpoint routing.

- Uses `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`, and `GEMINI_MODEL`
- Uses ADC (Application Default Credentials) inside backend container
- Does **not** require `GEMINI_API_KEY` for Customer routing

You already received updated:

- `.env`
- `docker-compose.yml`

This doc focuses on teammate setup and troubleshooting.

---

## 2) Prerequisites

Each teammate needs:

1. Google Cloud account with access to the target project
2. `gcloud` CLI installed and logged in
3. Docker Desktop running
4. IAM permissions in project

Recommended IAM roles:

- `roles/aiplatform.user` (required to call Gemini on Vertex)
- `roles/serviceusage.serviceUsageAdmin` (only needed if they must enable APIs themselves)

---

## 3) One-time Google Cloud setup

Run on host machine (not inside Docker):

```bash
gcloud auth login
gcloud auth application-default login
gcloud config set project YOUR_PROJECT_ID
```

Why both?

- `gcloud auth login` authenticates CLI user actions.
- `gcloud auth application-default login` creates ADC credentials used by app code.

---

## 4) Project/API checks

If you have permission, ensure Vertex API is enabled:

```bash
gcloud services enable aiplatform.googleapis.com --project YOUR_PROJECT_ID
```

Verify it is enabled:

```bash
gcloud services list --enabled --filter=aiplatform.googleapis.com --project YOUR_PROJECT_ID
```

If you get `PERMISSION_DENIED`, ask a project admin to:

1. Enable `aiplatform.googleapis.com`
2. Grant you `roles/aiplatform.user`

---

## 5) Required `.env` values

Set these values in local `.env`:

```env
GOOGLE_CLOUD_PROJECT=YOUR_PROJECT_ID
GOOGLE_CLOUD_LOCATION=us-central1
GEMINI_MODEL=gemini-2.5-flash
```

Other existing values (`DATABASE_URL`, `JWT_SECRET`, etc.) remain unchanged.

---

## 6) Docker + ADC behavior

Backend container uses host ADC credentials through compose mount:

- Host path: `${HOME}/.config/gcloud`
- Container path: `/root/.config/gcloud`
- `GOOGLE_APPLICATION_CREDENTIALS` points to:
  `/root/.config/gcloud/application_default_credentials.json`

So each teammate must run `gcloud auth application-default login` on their own machine.
Also verify this file exists on the host before starting Docker:
`${HOME}/.config/gcloud/application_default_credentials.json`

---

## 7) Start the app

From repo root:

```bash
docker compose down
docker compose up --build
```

---

## 8) Verify Vertex works before UI testing (recommended)

Run this from host shell:

```bash
ACCESS_TOKEN="$(gcloud auth print-access-token)"
PROJECT_ID="YOUR_PROJECT_ID"
LOCATION="us-central1"

curl -sS -X POST \
  "https://${LOCATION}-aiplatform.googleapis.com/v1/projects/${PROJECT_ID}/locations/${LOCATION}/publishers/google/models/gemini-2.5-flash:generateContent" \
  -H "Authorization: Bearer ${ACCESS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"contents":[{"role":"user","parts":[{"text":"Say hello in one short sentence."}]}]}'
```

If that works, Customer routing should work once backend is running.

---

## 9) Common errors and fixes

### `Google ADC credentials not found`

Cause:

- Backend container cannot find ADC file.

Fix:

1. Run `gcloud auth application-default login` on host
2. Rebuild/restart compose

---

### `403 PERMISSION_DENIED` with `aiplatform.endpoints.predict`

Cause:

- Your identity lacks Vertex prediction permission.

Fix:

- Ask admin to grant `roles/aiplatform.user` in project.

---

### `PERMISSION_DENIED` enabling API

Cause:

- Missing Service Usage permission.

Fix:

- Ask admin to enable API, or grant `roles/serviceusage.serviceUsageAdmin`.

---

### `400 INVALID_ARGUMENT: Please use a valid role: user, model`

Cause:

- Request payload missing role in `contents`.

Fix:

- Use Vertex payload format with `role: "user"` (already fixed in repo).

---

### `429 RESOURCE_EXHAUSTED` / quota issues

Cause:

- Project quotas/rate limits reached.

Fix:

- Check Vertex quotas and billing in Google Cloud project.

---

## 10) Developer checklist (quick)

Each teammate should complete this checklist once:

- [ ] `gcloud auth login`
- [ ] `gcloud auth application-default login`
- [ ] `gcloud config set project YOUR_PROJECT_ID`
- [ ] Confirm Vertex API enabled (or ask admin)
- [ ] Confirm IAM has `roles/aiplatform.user`
- [ ] Set `.env` vars: project/location/model
- [ ] `docker compose up --build`
- [ ] Test Customer tab "Ask"

---

## 11) Notes on API keys

- For Vertex path in this project, do **not** use `GEMINI_API_KEY`.
- Any legacy AI Studio code paths (if still present) may still reference API keys.
- Customer router path now uses Vertex auth and endpoint.
