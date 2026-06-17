"""Configuration and paths for synthetic data generation."""

import os
from pathlib import Path

# Base directory (synthetic-data folder)
BASE_DIR = Path(__file__).resolve().parent

# All generated outputs go here
OUTPUT_DIR = BASE_DIR / "output_data"

# Input prompt file
PROMPT_PATH = BASE_DIR / "transaction_prompt.txt"

# Gemini model and API
MODEL_NAME = "gemini-2.5-flash"
API_KEY = os.environ.get("GEMINI_API_KEY", "")

# Output filenames (written under OUTPUT_DIR)
CSV_FILENAME = "transactions.csv"
JSON_FILENAME = "transactions.json"
RAW_DEBUG_FILENAME = "raw_output.txt"
