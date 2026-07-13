FROM golang:1.25-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 \
    GOOS="${TARGETOS:-linux}" \
    GOARCH="${TARGETARCH:-amd64}" \
    go build -trimpath -ldflags="-s -w" -o /out/s3-copy-gateway .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:b7bb25d9f7c31d2bdd1982feb4dafcaf137703c7075dbe2febb41c24212b946f

LABEL org.opencontainers.image.source="https://github.com/mss-boot-ai/s3-copy-gateway" \
      org.opencontainers.image.description="Minimal S3 CopyObject compatibility gateway" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build --chown=65532:65532 /out/s3-copy-gateway /s3-copy-gateway
COPY --chown=65532:65532 LICENSE /licenses/LICENSE

USER 65532:65532
EXPOSE 8080

ENTRYPOINT ["/s3-copy-gateway"]
