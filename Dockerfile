FROM golang:1.27.3-bookworm AS source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM source AS test
RUN go vet ./... && go test -race -count=1 ./...

FROM source AS build
RUN CGO_ENABLED=0 go build -trimpath -o /out/docmind-api ./cmd/api \
    && CGO_ENABLED=0 go build -trimpath -o /out/docmind-migrate ./cmd/migrate \
    && mkdir -p /out/storage

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/docmind-api /docmind-api
COPY --from=build /out/docmind-migrate /docmind-migrate
COPY --from=build --chown=65532:65532 /out/storage/ /data/documents/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/docmind-api"]
