# Kubernetes Deployment Guide

All commands assume you are in the project root (`alloyfinance-ai/`).

---

## Prerequisites (one-time)

```bash
# Authenticate with GCP
gcloud auth login
gcloud config set project alloydbbd

# Connect kubectl to your cluster
gcloud container clusters get-credentials alloydb-demo --zone=us-central1-a

# Allow Docker to push to GCR
gcloud auth configure-docker
```

---

## First-time setup

### 1. Start the AlloyDB cluster (if scaled to 0)

```bash
gcloud container clusters resize alloydb-demo \
  --node-pool=default-pool \
  --num-nodes=2 \
  --zone=us-central1-a

kubectl get pods -n alloydb   # wait until Running
```

### 2. Create the app namespace

```bash
kubectl apply -f k8s/namespace.yaml
```

### 3. Build and push the backend image

```bash
docker build -t gcr.io/alloydbbd/backend:latest ./backend
docker push gcr.io/alloydbbd/backend:latest
```

### 4. Deploy the backend to get its external IP

```bash
kubectl apply -f k8s/backend-deployment.yaml
```

Wait for the LoadBalancer IP (takes ~1-2 min):

```bash
kubectl get svc backend -n app --watch
# Copy the EXTERNAL-IP once it appears, e.g. 34.68.xxx.xxx
```

### 5. Build and push the frontend image

Replace `<backend-ip>` with the IP from the previous step.

```bash
docker build \
  --build-arg VITE_API_URL=http://<backend-ip>:8000 \
  --build-arg VITE_GOOGLE_CLIENT_ID=<your-google-client-id> \
  -t gcr.io/alloydbbd/frontend:latest ./frontend

docker push gcr.io/alloydbbd/frontend:latest
```

### 6. Deploy the frontend to get its external IP

```bash
kubectl apply -f k8s/frontend-deployment.yaml
```

```bash
kubectl get svc frontend -n app --watch
# Copy the EXTERNAL-IP, e.g. 35.193.xxx.xxx
```

### 7. Fill in and apply secrets

Encode each value (run these one at a time, copy the output):

```bash
echo -n "postgresql://alloydbadmin:<alloydb-admin-password>@al-my-cluster-rw-ilb.alloydb.svc.cluster.local:5432/postgres" | base64
echo -n "<your-google-client-id>" | base64
echo -n "<your-jwt-secret>" | base64
echo -n "http://<frontend-ip>" | base64   # CORS_ORIGINS = frontend LoadBalancer IP
```

Paste each encoded value into [k8s/secrets.yaml](k8s/secrets.yaml) replacing the `<base64-encoded-value>` placeholders, then:

```bash
kubectl apply -f k8s/secrets.yaml
```

### 8. Restart the backend to pick up secrets

```bash
kubectl rollout restart deployment/backend -n app
```

### 9. Verify everything is running

```bash
kubectl get pods -n app
kubectl get svc -n app
```

Open `http://<frontend-ip>` in your browser. The app should load and talk to the backend.

---

## Updating code

### Backend change

```bash
docker build -t gcr.io/alloydbbd/backend:latest ./backend
docker push gcr.io/alloydbbd/backend:latest
kubectl rollout restart deployment/backend -n app
kubectl rollout status deployment/backend -n app   # watch progress
```

### Frontend change

The frontend API URL is baked into the image at build time, so rebuilding is required for any code change:

```bash
docker build \
  --build-arg VITE_API_URL=http://<backend-ip>:8000 \
  --build-arg VITE_GOOGLE_CLIENT_ID=<your-google-client-id> \
  -t gcr.io/alloydbbd/frontend:latest ./frontend

docker push gcr.io/alloydbbd/frontend:latest
kubectl rollout restart deployment/frontend -n app
kubectl rollout status deployment/frontend -n app
```

### Secrets change (e.g. rotate JWT_SECRET)

1. Re-encode the new value: `echo -n "newvalue" | base64`
2. Update [k8s/secrets.yaml](k8s/secrets.yaml)
3. `kubectl apply -f k8s/secrets.yaml`
4. `kubectl rollout restart deployment/backend -n app`

---

## Debugging

```bash
# Check pod logs
kubectl logs -n app deployment/backend
kubectl logs -n app deployment/frontend

# Describe a pod for events/errors
kubectl describe pod -n app -l app=backend

# Shell into the backend container
kubectl exec -it -n app deployment/backend -- /bin/bash
```

---

## Save money: scale cluster to 0

```bash
gcloud container clusters resize alloydb-demo \
  --node-pool=default-pool \
  --num-nodes=0 \
  --zone=us-central1-a
```

Data persists. When you want to bring it back, scale to 2 and re-run step 1.

---

## GitHub Actions CI/CD

Every push to `main` automatically builds both Docker images, pushes them to GCR, and restarts the GKE deployments.

### One-time GCP setup

```bash
# Enable Container Registry
gcloud services enable containerregistry.googleapis.com --project=alloydbbd

# Create a service account for GitHub Actions
gcloud iam service-accounts create github-actions --project=alloydbbd

# Grant required roles
gcloud projects add-iam-policy-binding alloydbbd \
  --member="serviceAccount:github-actions@alloydbbd.iam.gserviceaccount.com" \
  --role="roles/storage.admin"

gcloud projects add-iam-policy-binding alloydbbd \
  --member="serviceAccount:github-actions@alloydbbd.iam.gserviceaccount.com" \
  --role="roles/container.developer"

# Create and download the JSON key
gcloud iam service-accounts keys create key.json \
  --iam-account=github-actions@alloydbbd.iam.gserviceaccount.com
# Paste the contents of key.json as the GCP_SA_KEY GitHub secret, then delete key.json
rm key.json
```

### Required GitHub Secrets

Add these in **Settings → Secrets and variables → Actions**:

| Secret | Value |
|--------|-------|
| `GCP_SA_KEY` | Contents of the JSON key created above |
| `BACKEND_EXTERNAL_IP` | Backend LoadBalancer external IP (e.g. `34.68.xxx.xxx`) — run `kubectl get svc backend -n app` to find it |
| `VITE_GOOGLE_CLIENT_ID` | Your Google OAuth client ID |

### What the workflow does

On every push to `main` (`.github/workflows/deploy.yml`):
1. Authenticates to GCP using `GCP_SA_KEY`
2. Builds and pushes `gcr.io/alloydbbd/backend:latest`
3. Builds and pushes `gcr.io/alloydbbd/frontend:latest` with `VITE_API_URL=http://<BACKEND_EXTERNAL_IP>:8000` baked in
4. Runs `kubectl rollout restart` on both deployments and waits for them to become ready

---

## Files reference

| File | Purpose |
|------|---------|
| [k8s/namespace.yaml](k8s/namespace.yaml) | Creates the `app` namespace |
| [k8s/secrets.yaml](k8s/secrets.yaml) | DB URL, Google Client ID, JWT secret, CORS origins |
| [k8s/backend-deployment.yaml](k8s/backend-deployment.yaml) | FastAPI deployment + LoadBalancer service on port 8000 |
| [k8s/frontend-deployment.yaml](k8s/frontend-deployment.yaml) | Nginx/React deployment + LoadBalancer service on port 80 |
