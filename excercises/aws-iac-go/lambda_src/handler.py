"""
Lambda handler: fetch → validate → transform → store → metrics.

Environment variables (injected by the Go IaC runner):
  DYNAMODB_TABLE_NAME  - target DynamoDB table name
  EXTERNAL_API_URL     - URL of the external data source
  MAX_RETRIES          - maximum number of HTTP attempts (default: 3)
  AWS_REGION           - AWS region
"""

import json
import logging
import os
import time
import boto3
import urllib.request
import urllib.error
from datetime import datetime, timezone

# ── Logging setup (JSON structured logs for CloudWatch Insights) ────────────
logger = logging.getLogger()
logger.setLevel(logging.INFO)


def log(level: str, step: str, message: str, **extra):
    """Emit a single structured JSON log line."""
    record = {
        "time": datetime.now(timezone.utc).isoformat(),
        "level": level,
        "step": step,
        "message": message,
        **extra,
    }
    print(json.dumps(record))


# ── Configuration ────────────────────────────────────────────────────────────
TABLE_NAME  = os.environ.get("DYNAMODB_TABLE_NAME", "iac-data-store")
API_URL     = os.environ.get("EXTERNAL_API_URL", "https://jsonplaceholder.typicode.com/posts")
MAX_RETRIES = int(os.environ.get("MAX_RETRIES", "3"))
AWS_REGION  = os.environ.get("AWS_REGION", "us-east-1")

dynamo = boto3.resource("dynamodb", region_name=AWS_REGION)
cw     = boto3.client("cloudwatch",  region_name=AWS_REGION)


# ── 1. Health check ──────────────────────────────────────────────────────────
def health_check() -> bool:
    """Verify that the DynamoDB table is reachable and in ACTIVE status."""
    try:
        client = boto3.client("dynamodb", region_name=AWS_REGION)
        resp = client.describe_table(TableName=TABLE_NAME)
        status = resp["Table"]["TableStatus"]
        log("INFO", "health_check", f"Table {TABLE_NAME} → {status}", status=status)
        return status == "ACTIVE"
    except Exception as exc:
        log("ERROR", "health_check", f"Health check failed: {exc}", error=str(exc))
        return False


# ── 2. Fetch with retry and rate-limit handling ──────────────────────────────
def fetch_data(url: str) -> list:
    """Fetch data from the external API with retry and exponential backoff."""
    last_error = None

    for attempt in range(1, MAX_RETRIES + 1):
        log("INFO", "fetch", f"Attempt {attempt}/{MAX_RETRIES}", url=url)
        try:
            req = urllib.request.Request(
                url,
                headers={"Accept": "application/json", "User-Agent": "iac-lambda/1.0"},
            )
            with urllib.request.urlopen(req, timeout=10) as resp:
                # Handle rate limiting (HTTP 429)
                if resp.status == 429:
                    retry_after = int(resp.headers.get("Retry-After", 5))
                    log("WARN", "fetch", f"Rate limited — waiting {retry_after}s")
                    time.sleep(retry_after)
                    continue

                data = json.loads(resp.read().decode("utf-8"))
                log("INFO", "fetch", f"Fetched {len(data)} records", count=len(data))
                return data

        except urllib.error.HTTPError as exc:
            last_error = exc
            if exc.code == 429:
                wait = 2 ** attempt
                log("WARN", "fetch", f"HTTP 429 — waiting {wait}s", attempt=attempt)
                time.sleep(wait)
            else:
                wait = 2 ** attempt
                log("ERROR", "fetch", f"HTTP {exc.code}: {exc.reason}", attempt=attempt, wait=wait)
                time.sleep(wait)

        except Exception as exc:
            last_error = exc
            wait = 2 ** attempt
            log("ERROR", "fetch", f"Network error: {exc}", attempt=attempt, wait=wait)
            time.sleep(wait)

    raise RuntimeError(f"Fetch failed after {MAX_RETRIES} attempts: {last_error}")


# ── 3. Validation and transformation ────────────────────────────────────────
def validate_item(item: dict) -> bool:
    """Check that a record contains all required fields with correct types."""
    required = ("id", "title", "body", "userId")
    for field in required:
        if field not in item:
            log("WARN", "validate", f"Missing field '{field}'", item_id=item.get("id"))
            return False
        if not isinstance(item[field], (str, int)):
            log("WARN", "validate", f"Field '{field}' has wrong type", item_id=item.get("id"))
            return False
    return True


def transform_item(item: dict) -> dict:
    """
    Transform a raw API record into the DynamoDB target format.
    Adds timestamps, normalises all values to strings (required by DynamoDB).
    """
    return {
        "id":           str(item["id"]),                         # partition key
        "timestamp":    datetime.now(timezone.utc).isoformat(),  # sort key
        "title":        str(item["title"]).strip()[:500],         # max 500 chars
        "body":         str(item["body"]).strip()[:2000],
        "user_id":      str(item["userId"]),
        "source_url":   API_URL,
        "processed_at": datetime.now(timezone.utc).isoformat(),
    }


def process_data(raw: list) -> list:
    """Validate and transform a list of records. Returns only valid items."""
    valid, invalid = [], 0

    for item in raw:
        if validate_item(item):
            valid.append(transform_item(item))
        else:
            invalid += 1

    log("INFO", "process", f"Processed: {len(valid)} valid, {invalid} rejected",
        valid=len(valid), invalid=invalid)
    return valid


# ── 4. Store in DynamoDB ─────────────────────────────────────────────────────
def store_data(items: list) -> int:
    """
    Write records to DynamoDB using batch_writer.
    batch_writer automatically handles batches of 25 and retries unprocessed items.
    Returns the number of records written.
    """
    table  = dynamo.Table(TABLE_NAME)
    stored = 0
    with table.batch_writer() as batch:
        for item in items:
            batch.put_item(Item=item)
            stored += 1

    log("INFO", "store", f"Stored {stored} records", table=TABLE_NAME, count=stored)
    return stored


# ── 5. CloudWatch custom metrics ─────────────────────────────────────────────
def put_metric(name: str, value: float, unit: str = "Count"):
    """Publish a single metric to CloudWatch under the 'IaC/Lambda' namespace."""
    try:
        cw.put_metric_data(
            Namespace="IaC/Lambda",
            MetricData=[{
                "MetricName": name,
                "Value":      value,
                "Unit":       unit,
                "Dimensions": [{"Name": "FunctionName",
                                "Value": os.environ.get("AWS_LAMBDA_FUNCTION_NAME", "iac-data-fetcher")}],
            }]
        )
    except Exception as exc:
        log("WARN", "metrics", f"Could not publish metric {name}: {exc}")


# ── Main handler ─────────────────────────────────────────────────────────────
def lambda_handler(event, context):
    start = time.time()
    log("INFO", "start", "Lambda invoked", event=str(event)[:200])

    try:
        # 1. Health check
        if not health_check():
            raise RuntimeError(f"DynamoDB table '{TABLE_NAME}' is not available")

        # 2. Fetch data
        raw_data = fetch_data(API_URL)

        # 3. Process data
        processed = process_data(raw_data)
        if not processed:
            raise ValueError("No valid records after validation")

        # 4. Store data
        stored_count = store_data(processed)

        # 5. Publish metrics
        duration_ms = (time.time() - start) * 1000
        put_metric("ProcessedRecords",    stored_count)
        put_metric("ExecutionDurationMs", duration_ms, "Milliseconds")
        put_metric("Errors",              0)

        log("INFO", "done", "Completed successfully",
            stored=stored_count, duration_ms=round(duration_ms, 2))

        return {
            "statusCode": 200,
            "body": json.dumps({
                "message":     "OK",
                "stored":      stored_count,
                "duration_ms": round(duration_ms, 2),
            }),
        }

    except Exception as exc:
        duration_ms = (time.time() - start) * 1000
        log("ERROR", "fatal", str(exc), duration_ms=round(duration_ms, 2))
        put_metric("Errors", 1)

        return {
            "statusCode": 500,
            "body": json.dumps({"message": "ERROR", "error": str(exc)}),
        }