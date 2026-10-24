.PHONY: fmt vet test test-docker test-integration build run worker migrate up down logs

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./...

test-docker:
	docker build --target test -t docmind-test .

test-integration:
	docker compose --env-file .env -f deploy/docker-compose.yml --profile tools run --build --rm integration

build:
	go build -o bin/docmind-api ./cmd/api
	go build -o bin/docmind-worker ./cmd/worker

run:
	go run ./cmd/api

worker:
	go run ./cmd/worker

migrate:
	go run ./cmd/migrate

up:
	docker compose --env-file .env -f deploy/docker-compose.yml up -d --build

down:
	docker compose --env-file .env -f deploy/docker-compose.yml down

logs:
	docker compose --env-file .env -f deploy/docker-compose.yml logs -f api
