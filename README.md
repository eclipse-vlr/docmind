# DocMind

Backend на Go для загрузки документов и будущих ответов по их содержимому (RAG).
Стек: Go, net/http, PostgreSQL, pgvector, Docker.

Работают загрузка TXT/Markdown/PDF до 10 MiB, метаданные, список и удаление.
Worker извлекает текст TXT/Markdown/PDF и сохраняет фрагменты в PostgreSQL.
Файлы, задания и фрагменты сохраняются после перезапуска.
На текущем этапе `ready` означает, что текст извлечён и фрагменты записаны.
Embeddings, семантический поиск и ответы на вопросы появятся на этапах 4–5.

## Запуск

Нужен Docker Desktop с Linux containers. Команды для PowerShell:

```powershell
if (-not (Test-Path .env)) { Copy-Item .env.example .env }
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
curl.exe http://localhost:8080/healthz
curl.exe -F 'file=@example.txt' http://localhost:8080/api/v1/documents
curl.exe http://localhost:8080/api/v1/documents/UUID_ИЗ_ОТВЕТА
```

Порты по умолчанию: API — 8080, БД — 55432. Настройки — в `.env.example`.
Файлы хранятся в закрытом Docker volume; `docker compose down` сохраняет данные.
Compose запускает миграции, API и отдельный worker. Worker имеет доступ к файлам
только для чтения; удаление исходников и отложенную очистку выполняет API.

## Обработка документов

Состояния: `queued` → `processing` → `ready` либо `failed`.
При временном сбое документ возвращается в `queued`; причина доступна в
`error_message` без внутренних ошибок и текста документа.

Каждый worker обрабатывает один документ за раз. PostgreSQL выдаёт задания
через `FOR UPDATE SKIP LOCKED`, lease и растущий номер попытки. После падения
процесса другой worker заберёт задание по истечении lease. Просроченный worker
не сможет записать результат. По умолчанию доступны три попытки с задержками
1 и 2 секунды. Последняя аварийно прерванная попытка переводится в `failed`
после истечения lease. Фрагменты и статус `ready` фиксируются одной транзакцией;
повторное завершение не создаёт дубликаты. DELETE согласован с завершением
worker и удаляет фрагменты и задания каскадно.

TXT и Markdown должны содержать UTF-8. PDF разбирает Poppler (`pdftotext`),
только текстовый слой, без OCR. Номер страницы сохраняется у каждого PDF-фрагмента;
TXT/Markdown имеют `page_number = NULL`. Пустые и повреждённые документы
получают `failed`. Текст разбивается по 1000 Unicode-символов с перекрытием 200;
фрагменты PDF не пересекают границы страниц. Markdown обрабатывается как текст.

Лимиты: исходник — 10 MiB, извлечённый текст — 2 MiB, фрагменты — 4096.
Для PDF действуют ограничения памяти процесса 512 MiB и CPU 20 секунд;
контейнер worker ограничен 768 MiB, одним CPU и 64 процессами.
Poppler и `prlimit` входят в Docker-образ worker и тестовый образ.

Настройки worker в `.env.example`: `WORKER_POLL_INTERVAL=1s`,
`WORKER_JOB_TIMEOUT=30s`, `WORKER_LEASE_DURATION=60s`, `WORKER_MAX_ATTEMPTS=3`.
Lease должен превышать таймаут задания плюс два `DB_TIMEOUT`.
SIGTERM отменяет текущую обработку; неподтверждённое задание восстанавливается
по истечении lease. Для нескольких worker:

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml up -d --scale worker=2
docker compose --env-file .env -f deploy/docker-compose.yml logs -f worker
```

Для запуска без Docker нужны Go 1.27.2+, PostgreSQL с pgvector, `pdftotext`
и `prlimit` из `poppler-utils` и `util-linux` (Linux). После настройки переменных
окружения выполните `go run ./cmd/migrate`, затем в разных терминалах
`go run ./cmd/api` и `go run ./cmd/worker`. В Windows используйте Docker или WSL.

## Проверки

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml --profile tools run --build --rm integration
```

Выполняются unit-тесты, `go vet`, проверка гонок и интеграционные тесты PostgreSQL.
С Go и установленными Poppler/`prlimit`:

```powershell
go vet ./...
go test -race -count=1 ./...
# Для integration-тестов укажите TEST_DATABASE_URL тестовой БД с pgvector.
$env:TEST_DATABASE_URL = 'postgres://docmind:docmind_dev@127.0.0.1:55432/docmind?sslmode=disable'
go test -race -count=1 -timeout=90s -v ./tests/integration
```

Без `TEST_DATABASE_URL` интеграционные тесты пропускаются. С ним каждый тест
создаёт и удаляет отдельную схему; пользователь БД должен иметь право CREATE SCHEMA.
Проверяются конкурирующие workers, lease, backoff, восстановление после падения,
отмена обработки и удаление документа во время завершения worker.

Локальный однопользовательский MVP без авторизации.
Авария при загрузке может оставить бесхозный файл; очистка после DELETE восстанавливается автоматически.
