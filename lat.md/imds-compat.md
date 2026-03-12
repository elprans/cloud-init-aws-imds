# IMDS Compatibility

This document describes how the emulator matches real AWS IMDS behavior.

## Token Endpoint

The `/latest/api/token` endpoint requires `PUT` with an `X-aws-ec2-metadata-token-ttl-seconds` header (numeric). The TTL value is echoed back in the response header `X-Aws-Ec2-Metadata-Token-Ttl-Seconds`, as required by the aws-sdk-go-v2 IMDS client. Requests with `X-Forwarded-For` are rejected (SSRF protection). Non-PUT methods return 405.

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

## Server Architecture

The emulator uses a struct-based `Server` that holds all state: `InstanceDataSource`, `NetworkInfo`, `BlockDeviceSource`, IAM credentials (protected by `sync.RWMutex`), options, and start time. All handlers are methods on `*Server`. The `Handler()` method returns an `http.Handler` with all routes registered on a dedicated `ServeMux` (not `http.DefaultServeMux`). Dependency injection via interfaces (`NetworkInfo`, `BlockDeviceSource`, `InstanceDataSource`) enables fully isolated, deterministic tests without global state or real system dependencies.

## SDK Compatibility Testing

The `cmd/sdk_compat_test.go` file validates compatibility by using the real `aws-sdk-go-v2/feature/ec2/imds` client against the emulator via `httptest.Server`. All happy-path endpoint testing goes through the SDK: `GetMetadata` covers every `/latest/meta-data/*` path (ami-id, instance-id, instance-type, hostnames, IPs, MAC, macs, block devices, IAM credentials, tags, autoscaling, services), plus typed methods `GetIAMInfo`, `GetInstanceIdentityDocument`, `GetRegion`, `GetDynamicData`, and the full IMDSv2 token flow. Direct unit tests in `cmd/main_test.go` only cover edge cases the SDK cannot exercise: token validation errors, middleware behavior, missing/null data paths, struct marshaling, and the `getEndpoints` helper.
