# IMDS Compatibility

This document describes how the emulator matches real AWS IMDS behavior.

## Token Endpoint

The `/latest/api/token` endpoint requires `PUT` with an `X-aws-ec2-metadata-token-ttl-seconds` header (numeric). Requests with `X-Forwarded-For` are rejected (SSRF protection). Non-PUT methods return 405.

## HTTP Method Enforcement

All metadata endpoints (everything except `/latest/api/token`) only accept `GET`. Non-GET requests return 405. The token endpoint only accepts `PUT`.

## Response Headers

Every response includes `Server: EC2ws` and `Content-Type: text/plain`, matching real IMDS behavior. These are set in the `logRequest` middleware.

## Instance Identity Document

The `/latest/dynamic/instance-identity/document` endpoint returns a JSON object with deterministic field ordering (struct-based marshaling). `devpayProductCodes` and `marketplaceProductCodes` are `null` (not empty arrays). `pendingTime` is set to the server start time as an ISO 8601 timestamp. `accountId` defaults to `123456789012` and is configurable via the `-account-id` flag.

## IAM Info

The `/latest/meta-data/iam/info` endpoint returns a proper IMDS-format JSON with `Code`, `LastUpdated`, `InstanceProfileArn`, and `InstanceProfileId` fields, rather than the raw instance-profile map.

## MAC Address Filtering

The `/latest/meta-data/network/interfaces/macs` endpoint filters out loopback interfaces (no hardware address) and virtual interfaces (`docker*`, `veth*`), matching real IMDS which only lists actual ENI MAC addresses.
