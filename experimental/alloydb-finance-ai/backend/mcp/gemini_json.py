"""Vertex AI Gemini generateContent helper for JSON-shaped responses."""

from __future__ import annotations

import json
import logging
from typing import Any, Optional

import requests
from google.auth import default
from google.auth.exceptions import DefaultCredentialsError
from google.auth.transport.requests import Request as GoogleAuthRequest

logger = logging.getLogger(__name__)


def generate_json(
    *,
    project_id: str,
    location: str,
    model: str,
    prompt: str,
    temperature: float = 0.1,
    response_schema: Optional[dict[str, Any]] = None,
    timeout: int = 30,
) -> dict[str, Any]:
    """
    Call Vertex AI Gemini with JSON output. Returns parsed dict from model text.

    Raises:
        RuntimeError: on HTTP errors, empty response, or invalid JSON.
    """
    if not project_id.strip():
        raise RuntimeError("GOOGLE_CLOUD_PROJECT is required for Vertex AI routing")
    if not location.strip():
        raise RuntimeError("GOOGLE_CLOUD_LOCATION is required for Vertex AI routing")

    url = (
        f"https://{location}-aiplatform.googleapis.com/v1/"
        f"projects/{project_id}/locations/{location}/publishers/google/models/"
        f"{model}:generateContent"
    )
    generation_config: dict[str, Any] = {
        "temperature": temperature,
        "responseMimeType": "application/json",
    }
    if response_schema is not None:
        generation_config["responseSchema"] = response_schema

    payload: dict[str, Any] = {
        "contents": [{"role": "user", "parts": [{"text": prompt}]}],
        "generationConfig": generation_config,
    }
    try:
        credentials, _ = default(scopes=["https://www.googleapis.com/auth/cloud-platform"])
        credentials.refresh(GoogleAuthRequest())
        token = credentials.token
    except DefaultCredentialsError as e:
        raise RuntimeError(
            "Google ADC credentials not found. Run `gcloud auth application-default login` "
            "for local dev or configure a service account in runtime."
        ) from e
    except Exception as e:
        raise RuntimeError(f"Failed to acquire Google access token: {e}") from e

    resp = requests.post(
        url,
        json=payload,
        timeout=timeout,
        headers={"Authorization": f"Bearer {token}"},
    )
    try:
        resp.raise_for_status()
    except requests.RequestException as e:
        body_snippet = (resp.text or "")[:1000]
        logger.warning("Gemini request failed: %s; body=%s", e, body_snippet)
        raise RuntimeError(f"Gemini request failed: {e}; body={body_snippet}") from e

    body: dict[str, Any] = resp.json()
    candidates = body.get("candidates") or []
    if not candidates:
        logger.warning("Gemini returned no candidates: %s", body)
        raise RuntimeError("Gemini returned no candidates")

    parts = (candidates[0].get("content") or {}).get("parts") or []
    if not parts or "text" not in parts[0]:
        logger.warning("Gemini candidate missing text: %s", body)
        raise RuntimeError("Gemini response missing text")

    text = parts[0]["text"]
    try:
        return json.loads(text)
    except json.JSONDecodeError as e:
        logger.warning("Gemini returned non-JSON text: %s", text[:500])
        raise RuntimeError("Gemini returned invalid JSON") from e
