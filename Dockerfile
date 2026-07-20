FROM --platform=$BUILDPLATFORM golang:1.25-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 \
    GOOS="${TARGETOS:-linux}" \
    GOARCH="${TARGETARCH:-amd64}" \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/s3-copy-gateway .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b

LABEL org.opencontainers.image.source="https://github.com/mss-boot-ai/s3-copy-gateway" \
      org.opencontainers.image.description="Minimal S3 CopyObject compatibility gateway" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build --chown=65532:65532 /out/s3-copy-gateway /s3-copy-gateway
COPY --chown=65532:65532 LICENSE /licenses/LICENSE

USER 65532:65532
EXPOSE 8080

ENTRYPOINT ["/s3-copy-gateway"]
