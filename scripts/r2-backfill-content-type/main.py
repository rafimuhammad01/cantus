"""Backfill Content-Type metadata on existing R2 objects.

Objects uploaded before the Go Commit fix and the Python upload_from_path fix
were stored without a Content-Type header, causing R2 to serve them as
application/octet-stream. Safari refuses to decode audio blobs with a non-
audio/* Content-Type, which broke in-browser playback.

This script paginates all objects in the bucket (or a key prefix), derives the
expected Content-Type from each key's extension, and rewrites the metadata via
CopyObject (MetadataDirective=REPLACE) for any object whose current Content-Type
doesn't match. No bytes are moved — R2 updates the metadata record in place.

Usage:
    # Requires boto3. Install if not already present:
    #   pip install boto3
    # Or activate the audio-processor-gpu venv which includes boto3 if present.

    # Dry-run (safe — no writes):
    python scripts/r2-backfill-content-type/main.py --dry-run

    # Dry-run for a specific prefix:
    python scripts/r2-backfill-content-type/main.py --dry-run --prefix abc123xyz01/

    # Live run:
    python scripts/r2-backfill-content-type/main.py

Credentials are read from environment variables (never hardcode):
    R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_BUCKET
"""

from __future__ import annotations

import argparse
import mimetypes
import os
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

import boto3
from botocore.config import Config


def _require_env(*names: str) -> dict[str, str]:
    missing = [n for n in names if not os.environ.get(n)]
    if missing:
        sys.exit(f"Missing required environment variables: {', '.join(missing)}")
    return {n: os.environ[n] for n in names}


def _make_client(account_id: str, access_key: str, secret_key: str):
    return boto3.client(
        "s3",
        endpoint_url=f"https://{account_id}.r2.cloudflarestorage.com",
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        region_name="auto",
        config=Config(signature_version="s3v4"),
    )


def _paginate_objects(client, bucket: str, prefix: str):
    paginator = client.get_paginator("list_objects_v2")
    kwargs = {"Bucket": bucket}
    if prefix:
        kwargs["Prefix"] = prefix
    for page in paginator.paginate(**kwargs):
        yield from page.get("Contents", [])


def main() -> None:
    parser = argparse.ArgumentParser(description="Backfill Content-Type on R2 objects.")
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print what would change without making any writes.",
    )
    parser.add_argument(
        "--prefix",
        default="",
        metavar="PREFIX",
        help="Only process objects under this key prefix (default: all).",
    )
    parser.add_argument(
        "--bucket",
        default="",
        metavar="NAME",
        help="Override R2_BUCKET env var.",
    )
    parser.add_argument(
        "--workers",
        type=int,
        default=10,
        metavar="N",
        help="Parallel worker threads for HEAD + CopyObject calls (default: 10).",
    )
    args = parser.parse_args()

    env = _require_env("R2_ACCOUNT_ID", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_BUCKET")
    bucket = args.bucket or env["R2_BUCKET"]

    client = _make_client(env["R2_ACCOUNT_ID"], env["R2_ACCESS_KEY_ID"], env["R2_SECRET_ACCESS_KEY"])

    counts = {"ok": 0, "skip_correct": 0, "skip_unknown": 0, "fixed": 0, "dry": 0}

    def process(key: str) -> tuple[str, str]:
        expected_ct, _ = mimetypes.guess_type(key)
        if not expected_ct:
            return ("skip_unknown", f"SKIP (unknown ext): {key}")

        head = client.head_object(Bucket=bucket, Key=key)
        current_ct = head.get("ContentType", "")

        if current_ct == expected_ct:
            return ("skip_correct", f"SKIP (already correct): {key}  [{current_ct}]")

        if args.dry_run:
            return ("dry", f"DRY: would fix: {key}  {current_ct!r} → {expected_ct!r}")

        # CopyObject with MetadataDirective=REPLACE rewrites metadata in
        # place — no byte movement, no re-upload of object data.
        client.copy_object(
            Bucket=bucket,
            Key=key,
            CopySource={"Bucket": bucket, "Key": key},
            MetadataDirective="REPLACE",
            ContentType=expected_ct,
        )
        return ("fixed", f"FIXED: {key}  {current_ct!r} → {expected_ct!r}")

    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        futures = [
            ex.submit(process, obj["Key"])
            for obj in _paginate_objects(client, bucket, args.prefix)
        ]
        for fut in as_completed(futures):
            kind, line = fut.result()
            counts[kind] += 1
            print(line)

    print()
    print("Summary:")
    print(f"  already correct : {counts['skip_correct']}")
    print(f"  unknown ext     : {counts['skip_unknown']}")
    if args.dry_run:
        print(f"  would fix       : {counts['dry']}")
    else:
        print(f"  fixed           : {counts['fixed']}")


if __name__ == "__main__":
    main()
