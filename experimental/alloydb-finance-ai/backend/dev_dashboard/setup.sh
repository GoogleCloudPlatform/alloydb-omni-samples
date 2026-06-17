#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$(dirname "$SCRIPT_DIR")"
PROJECT_DIR="$(dirname "$BACKEND_DIR")"
VENV="$BACKEND_DIR/venv"

# ── Preflight ────────────────────────────────────────────────────────────────

if [ ! -f "$PROJECT_DIR/.env" ]; then
  echo "ERROR: $PROJECT_DIR/.env not found."
  echo "Create it with DATABASE_URL, GOOGLE_CLIENT_ID, and JWT_SECRET set."
  exit 1
fi

if [ ! -d "$VENV" ]; then
  echo "ERROR: venv not found at $VENV"
  echo "Run: python3 -m venv venv && pip install -r requirements.txt"
  exit 1
fi

# ── Kill any existing server on port 8000 ───────────────────────────────────

if lsof -ti tcp:8000 &>/dev/null; then
  echo "Port 8000 in use — stopping existing process..."
  lsof -ti tcp:8000 | xargs kill -9
  sleep 1
fi

# ── Start FastAPI backend ────────────────────────────────────────────────────

echo "Starting AlloyFinance backend on http://localhost:8000 ..."
source "$VENV/bin/activate"
set -a; source "$PROJECT_DIR/.env"; set +a
# host.docker.internal only resolves inside containers; remap to localhost when running natively
DATABASE_URL="${DATABASE_URL//host.docker.internal/127.0.0.1}"
export DATABASE_URL
cd "$BACKEND_DIR"
uvicorn app.main:app --host 0.0.0.0 --port 8000 --reload &
UVICORN_PID=$!

# Wait for the server to be ready
echo -n "Waiting for server"
for i in {1..20}; do
  if curl -sf http://localhost:8000/health &>/dev/null; then
    echo " ready."
    break
  fi
  echo -n "."
  sleep 1
done

if ! curl -sf http://localhost:8000/health &>/dev/null; then
  echo ""
  echo "ERROR: Backend did not start within 20 seconds."
  kill $UVICORN_PID 2>/dev/null
  exit 1
fi

# ── Generate a long-lived dev JWT from .env secrets ─────────────────────────

DEV_TOKEN=$(python3 - <<'PYEOF'
import os, sys
try:
    import jwt
    from datetime import datetime, timezone, timedelta
    secret = os.environ.get("JWT_SECRET", "")
    if not secret:
        sys.exit(1)
    token = jwt.encode(
        {"sub": "0", "email": "dev@local", "exp": datetime.now(timezone.utc) + timedelta(days=365)},
        secret,
        algorithm="HS256",
    )
    print(token)
except Exception as e:
    sys.stderr.write(f"Token generation failed: {e}\n")
    sys.exit(1)
PYEOF
)

# ── Open the dashboard ───────────────────────────────────────────────────────

echo "Opening dev dashboard..."

if [ -z "$DEV_TOKEN" ]; then
  echo "WARNING: Could not generate dev token — authenticated endpoints will return 401."
fi
DASHBOARD_URL="file://$SCRIPT_DIR/observe.html?token=$DEV_TOKEN"

if command -v open &>/dev/null; then
  open "$DASHBOARD_URL"
elif command -v xdg-open &>/dev/null; then
  xdg-open "$DASHBOARD_URL"
else
  echo "Open manually: $DASHBOARD_URL"
fi

echo ""
echo "Dev dashboard running. Backend PID: $UVICORN_PID"
echo "Press Ctrl+C to stop the backend server."

# Keep script alive so Ctrl+C cleanly stops uvicorn
trap "echo ''; echo 'Stopping backend...'; kill $UVICORN_PID 2>/dev/null" INT TERM
wait $UVICORN_PID
