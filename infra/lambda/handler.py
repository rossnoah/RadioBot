"""RadioBot backup authorizer.

Receives {"key": "recordings/YYYYMMDD/foo.wav", "size": 12345} from the device,
authenticates via the x-backup-secret header, enforces a daily upload quota in
DynamoDB, and returns a presigned S3 POST bound to that exact key and size.
The device never holds AWS credentials.
"""
import base64
import hmac
import json
import os
import re
import time

import boto3
from botocore.exceptions import ClientError

BUCKET_NAME = os.environ["BUCKET_NAME"]
QUOTA_TABLE = os.environ["QUOTA_TABLE"]
DEVICE_SECRET = os.environ["DEVICE_SECRET"]
KEY_PREFIX = os.environ.get("KEY_PREFIX", "radiobot/")
MAX_UPLOADS_PER_DAY = int(os.environ.get("MAX_UPLOADS_PER_DAY", "50000"))
MAX_BYTES_PER_DAY = int(os.environ.get("MAX_BYTES_PER_DAY", str(2 * 1024**3)))
MAX_OBJECT_BYTES = int(os.environ.get("MAX_OBJECT_BYTES", str(512 * 1024**2)))
URL_EXPIRY_SECONDS = 300

# Only recordings under a date folder, DB snapshots, or config files. Anything
# else is rejected, so even with the secret nothing outside these shapes can
# land in the bucket.
KEY_PATTERN = re.compile(
    r"^(recordings/\d{8}/[A-Za-z0-9._-]{1,200}\.wav"
    r"|db/[A-Za-z0-9._-]{1,200}\.db(\.gz)?"
    r"|config/[A-Za-z0-9._-]{1,200}\.yaml)$"
)

s3 = boto3.client("s3")
dynamodb = boto3.client("dynamodb")


def _response(status, body):
    return {
        "statusCode": status,
        "headers": {"Content-Type": "application/json"},
        "body": json.dumps(body),
    }


def _check_quota(size):
    """Atomically count this upload against today's quota. Returns True if allowed."""
    day = time.strftime("%Y-%m-%d", time.gmtime())
    try:
        dynamodb.update_item(
            TableName=QUOTA_TABLE,
            Key={"pk": {"S": f"quota#{day}"}},
            # Condition can't do arithmetic, so the byte ceiling is checked as
            # "bytes already used <= (daily max - this upload's size)".
            UpdateExpression=(
                "ADD uploads :one, bytes_used :size "
                "SET expires_at = if_not_exists(expires_at, :exp)"
            ),
            ConditionExpression=(
                "attribute_not_exists(uploads) "
                "OR (uploads < :max_uploads AND bytes_used <= :bytes_headroom)"
            ),
            ExpressionAttributeValues={
                ":one": {"N": "1"},
                ":size": {"N": str(size)},
                ":exp": {"N": str(int(time.time()) + 3 * 86400)},
                ":max_uploads": {"N": str(MAX_UPLOADS_PER_DAY)},
                ":bytes_headroom": {"N": str(MAX_BYTES_PER_DAY - size)},
            },
        )
        return True
    except ClientError as e:
        if e.response["Error"]["Code"] == "ConditionalCheckFailedException":
            return False
        raise


def handler(event, context):
    headers = {k.lower(): v for k, v in (event.get("headers") or {}).items()}
    provided = headers.get("x-backup-secret", "")
    if not hmac.compare_digest(provided.encode(), DEVICE_SECRET.encode()):
        return _response(403, {"error": "forbidden"})

    body = event.get("body") or "{}"
    if event.get("isBase64Encoded"):
        body = base64.b64decode(body)
    try:
        payload = json.loads(body)
    except (ValueError, TypeError):
        return _response(400, {"error": "invalid JSON body"})

    key = payload.get("key")
    size = payload.get("size")

    if not isinstance(key, str) or not KEY_PATTERN.match(key):
        return _response(400, {"error": "invalid key"})
    if not isinstance(size, int) or size < 1 or size > MAX_OBJECT_BYTES:
        return _response(400, {"error": f"size must be 1..{MAX_OBJECT_BYTES} bytes"})

    if not _check_quota(size):
        return _response(429, {"error": "daily upload quota exceeded"})

    full_key = KEY_PREFIX + key
    post = s3.generate_presigned_post(
        Bucket=BUCKET_NAME,
        Key=full_key,
        Conditions=[
            {"key": full_key},
            ["content-length-range", size, size],
        ],
        ExpiresIn=URL_EXPIRY_SECONDS,
    )
    return _response(200, {"url": post["url"], "fields": post["fields"]})
