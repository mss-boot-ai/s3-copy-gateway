# s3-copy-gateway

[![CI](https://github.com/mss-boot-ai/s3-copy-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/mss-boot-ai/s3-copy-gateway/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/mss-boot-ai/s3-copy-gateway)](LICENSE)

A small, stateless AWS Signature Version 4 gateway for a narrow S3
`CopyObject` compatibility workflow.

The gateway handles each request as follows:

1. Verify the incoming SigV4 request.
2. Check the original source bucket and key with `HeadObject` on a source
   S3-compatible service.
3. If the source object exists, return a valid `CopyObjectResult` immediately.
4. If, and only if, the source returns a structured not-found response, map the
   source and destination buckets and issue `CopyObject` to a target
   S3-compatible service.

> [!IMPORTANT]
> A source hit is an acknowledgement-only path. It does **not** create the
> requested destination object. This service is not a general-purpose S3 copy
> proxy; deploy it only where that compatibility behavior is intentional.

Authentication failures, timeouts, network errors, and upstream 5xx responses
never trigger the fallback copy. This prevents a source outage from being
treated as object absence.

## Features

- Accepts only `PUT /{target-bucket}/{target-key}` requests with an
  `x-amz-copy-source: /{source-bucket}/{source-key}` header.
- Supports path-style and virtual-host-style incoming requests.
- Preserves escaped keys and repeated `/` path segments.
- Requires `x-amz-copy-source` to be included in SigV4 `SignedHeaders`.
- Uses exact bucket admission and mapping rules.
- Supports any explicitly configured S3-compatible target endpoint.
- Includes automatic regional endpoint derivation for OVHcloud Object Storage.
- Uses shared AWS SDK clients, bounded connection pools, no SDK retries, and a
  global concurrency limit.
- Exposes an unauthenticated `/healthz` endpoint.
- Does not read upload bodies or write files to disk.
- Has no database, message broker, queue, or background worker dependency.

Unsupported S3 options, including conditional copies, ranged copies, ACLs,
storage-class changes, encryption overrides, tag replacement, and multipart
copy, return an S3-compatible `NotImplemented` response.

## Quick Start

Copy the example environment file and replace every placeholder:

```bash
cp config/example.env .env
docker build -t s3-copy-gateway .
docker run --rm --env-file .env -p 8080:8080 s3-copy-gateway
```

The health endpoint should then respond with `ok`:

```bash
curl --fail http://127.0.0.1:8080/healthz
```

Terminate TLS at a trusted reverse proxy when exposing the gateway over a
network. SigV4 credentials and headers must not travel over untrusted plaintext
connections.

## Configuration

Configuration is read only from environment variables.

### Required settings

| Variable | Purpose |
| --- | --- |
| `SOURCE_S3_ENDPOINT` | Source S3-compatible endpoint |
| `SOURCE_S3_ACCESS_KEY` | Source S3 access key |
| `SOURCE_S3_SECRET_KEY` | Source S3 secret key |
| `TARGET_S3_ACCESS_KEY` | Target S3 access key |
| `TARGET_S3_SECRET_KEY` | Target S3 secret key |
| `TARGET_S3_ENDPOINT` | Target endpoint; optional only for a supported derived provider such as `ovh` |
| `BUCKET_MAPPINGS_JSON` | Exact request-bucket to target-bucket mapping |

Bucket mappings are applied independently to the copy source and destination:

```json
{
  "source-public": {"bucket": "source-storage", "region": "us-east-1"},
  "destination-public": {"bucket": "destination-storage", "region": "us-east-1"}
}
```

An unmapped bucket returns `NoSuchBucket` before either upstream is called.
`{"*":"*"}` enables unrestricted identity mapping and should be used only in
trusted environments. String values use `TARGET_S3_REGION`, for example
`{"source-public":"source-storage"}`.

### Authentication settings

Incoming SigV4 verification uses the source credentials by default. Separate
incoming credentials can be configured with `S3_ACCESS_KEY` and
`S3_SECRET_KEY`. Credential rotation or multiple clients can use
`S3_CREDENTIALS_JSON` in any of these forms:

```json
{"client-a":"secret-a","client-b":"secret-b"}
```

```json
[{"access_key":"client-a","secret_key":"secret-a"}]
```

`AUTH_ACCEPT_REGIONS` is a comma-separated allowlist and defaults to `*`.
`AUTH_CLOCK_SKEW` defaults to `5m`.

### Runtime defaults

| Variable | Default |
| --- | --- |
| `LISTEN_ADDR` | `:8080` |
| `S3_ADDRESSING_STYLE` | `auto` |
| `SOURCE_S3_REGION` | `us-east-1` |
| `SOURCE_S3_PATH_STYLE` | `true` |
| `TARGET_S3_PROVIDER` | `s3` |
| `TARGET_S3_REGION` | Empty; used as the signing-region fallback for string bucket mappings |
| `TARGET_S3_PATH_STYLE` | `false` |
| `MAX_IN_FLIGHT` | `256` |
| `ACQUIRE_WAIT` | `100ms` |
| `SOURCE_CHECK_TIMEOUT` | `2s` |
| `COPY_TIMEOUT` | `30s` |

Set `PUBLIC_S3_BASE_DOMAIN` when `S3_ADDRESSING_STYLE=virtual`. Source and
target HTTP transport timeouts can be tuned with the `SOURCE_*_TIMEOUT` and
`TARGET_*_TIMEOUT` variables shown in [`config/example.env`](config/example.env).
The same file documents request checksum calculation and response checksum
validation controls; accepted values are `WHEN_REQUIRED` and `WHEN_SUPPORTED`.

### OVHcloud endpoint derivation

Set `TARGET_S3_PROVIDER=ovh` and omit `TARGET_S3_ENDPOINT` to derive
`https://s3.{region}.io.cloud.ovh.net`. Every mapped bucket must then provide a
region, or `TARGET_S3_REGION` must provide the fallback.

## Development

Go 1.25 or newer is required.

```bash
make ci
```

The command checks formatting and module integrity, then runs `go vet`,
Staticcheck, unit tests, race tests, and a production build. GitHub Actions also
runs `govulncheck`, builds the container image without publishing it, and
starts the image to verify the non-root user and `/healthz` endpoint.

## Security

See [SECURITY.md](SECURITY.md) for vulnerability reporting. Never commit real
credentials or deploy the example values.

## License

Licensed under the [Apache License 2.0](LICENSE).
