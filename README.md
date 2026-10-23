# DocMind

Backend на Go для загрузки документов и будущих ответов по их содержимому (RAG).
Стек: Go, net/http, PostgreSQL, pgvector, Docker.

Работают загрузка TXT/Markdown/PDF до 10 MiB, метаданные, список и удаление.
Файлы и задания сохраняются после перезапуска. Worker и RAG пока в разработке;
документы остаются в статусе `queued`.

## Запуск

Нужен Docker Desktop с Linux containers. Команды для PowerShell:

```powershell
if (-not (Test-Path .env)) { Copy-Item .env.example .env }
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
curl.exe http://localhost:8080/healthz
curl.exe -F 'file=@example.txt' http://localhost:8080/api/v1/documents
```

Порты по умолчанию: API — 8080, БД — 55432. Настройки — в `.env.example`.
Файлы хранятся в закрытом Docker volume; `docker compose down` сохраняет данные.

## Проверки

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml --profile tools run --build --rm integration
```

Выполняются unit-тесты, `go vet`, проверка гонок и интеграционные тесты PostgreSQL.

Локальный однопользовательский MVP без авторизации. Полный разбор PDF ещё не реализован.
Авария при загрузке может оставить бесхозный файл; очистка после DELETE восстанавливается автоматически.
