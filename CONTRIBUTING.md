# Contributing

Thanks for helping improve `s3-copy-gateway`.

## Development setup

Install Go 1.25 or newer, clone the repository, and run:

```bash
make ci
```

Docker changes should also be checked with:

```bash
docker build -t s3-copy-gateway:dev .
```

## Pull requests

- Keep changes focused on the CopyObject compatibility boundary.
- Add or update tests for behavior changes.
- Preserve S3-compatible error responses and response-path timeouts.
- Do not commit credentials, production endpoints, object names, packet
  captures, or logs from real systems.
- Update the README when configuration or externally visible behavior changes.

Before opening a pull request, ensure `make ci` passes from a clean checkout.

Maintainers should follow [RELEASING.md](RELEASING.md) and must not reuse or
move a published release tag.

## Reporting security issues

Do not open a public issue for a suspected vulnerability. Follow
[SECURITY.md](SECURITY.md) instead.
