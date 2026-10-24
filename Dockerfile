# syntax=docker/dockerfile:1
FROM golang:1.27.3-bookworm AS source
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=proxy_ca \
    if [ -s /run/secrets/proxy_ca ]; then export SSL_CERT_FILE=/run/secrets/proxy_ca; fi; \
    go mod download
COPY . .

FROM source AS test
RUN apt-get update && apt-get install -y --no-install-recommends poppler-utils util-linux \
    && rm -rf /var/lib/apt/lists/*
RUN go vet ./... && go test -race -count=1 ./...

FROM source AS build
RUN CGO_ENABLED=0 go build -trimpath -o /out/docmind-api ./cmd/api \
    && CGO_ENABLED=0 go build -trimpath -o /out/docmind-migrate ./cmd/migrate \
    && CGO_ENABLED=0 go build -trimpath -o /out/docmind-worker ./cmd/worker \
    && mkdir -p /out/storage

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/docmind-api /docmind-api
COPY --from=build /out/docmind-migrate /docmind-migrate
COPY --from=build --chown=65532:65532 /out/storage/ /data/documents/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/docmind-api"]

FROM debian:bookworm-slim AS worker
RUN apt-get update && apt-get install -y --no-install-recommends poppler-utils util-linux ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/docmind-worker /docmind-worker
COPY --from=build --chown=65532:65532 /out/storage/ /data/documents/
USER 65532:65532
ENTRYPOINT ["/docmind-worker"]

FROM runtime AS final
