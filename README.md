# DocMind

Go backend: TXT/Markdown/PDF → embeddings → поиск → ответ с источниками.
PostgreSQL + pgvector, отдельный worker, Docker Compose. Генерация — Alice AI LLM
через Yandex AI Studio; по умолчанию включён локальный fake-режим без сети и оплаты.

## Запуск

Нужен Docker Desktop с Linux containers. PowerShell:

```powershell
if (-not (Test-Path .env)) { Copy-Item .env.example .env }
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
curl.exe http://localhost:8080/readyz
curl.exe -F 'file=@example.txt' http://localhost:8080/api/v1/documents
curl.exe http://localhost:8080/api/v1/documents/UUID
# После статуса ready:
$body = @{ question = 'PostgreSQL'; document_ids = @('UUID') } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/api/v1/questions' `
  -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($body))
```

API: `localhost:8080`, БД: `localhost:55432`. Есть список
`GET /api/v1/documents?limit=20&offset=0` и `DELETE /api/v1/documents/UUID`.
Ответ: `answer` и `sources` с `document_id`, `chunk_id`, `page`, `snippet`.
При нехватке данных сервис сообщает об этом и возвращает `sources: []`.

## Alice AI

Для реального провайдера задайте в `.env`:

```dotenv
CHAT_PROVIDER=yandex
EMBEDDING_PROVIDER=yandex
YANDEX_API_KEY=ваш_API_ключ
YANDEX_FOLDER_ID=идентификатор_каталога
```

Нужны активный платёжный аккаунт и права `ai.languageModels.user` на каталог.
Вопросы и найденные фрагменты отправляются Яндексу, вызовы платные.
Используются [Alice AI LLM](https://aistudio.yandex.ru/ru/docs/ai-studio/quickstart/)
и [embeddings v2](https://aistudio.yandex.ru/en/docs/ai-studio/concepts/embeddings):
`text-embeddings-v2-doc/` для документов, `text-embeddings-v2-query/` для вопросов,
256 измерений. API принимает один текст за HTTP-запрос; пачка выполняется последовательно.
Для больших документов увеличьте `WORKER_JOB_TIMEOUT` и `WORKER_LEASE_DURATION`;
учитывайте квоты провайдера. Chat-вызовы автоматически не повторяются.

`fake` ищет совпадения слов и цитирует фрагмент, без понимания синонимов.
При смене embeddings нужен reindex; при смене только chat — нет:

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml exec api /docmind-reindex -document UUID
```

Перед обновлением прежнего индекса остановите `api` и `worker` командой Compose `stop`,
затем выполните `up -d --build`. Миграция 005 очищает несовместимые векторы,
сохраняет исходники и тексты и ставит документы в очередь. `failed` требует явного reindex.
Откат к прежнему бинарнику требует восстановления БД из резервной копии.

## Ограничения и тесты

Локальный однопользовательский MVP без авторизации. Исходник — 10 MiB, текст — 2 MiB,
до 4096 фрагментов по 1000 символов с overlap 200. PDF без OCR, с номерами страниц.
Данные переживают перезапуск; `down` сохраняет volumes, `down -v` удаляет их.
Lease и generation блокируют старого worker; временные ошибки — до трёх попыток.
Повтор embeddings может тарифицироваться снова. Ключи и тексты не пишутся в логи.

Вопрос — 4000 байт UTF-8 и 1–20 UUID; body — 16 KiB; контекст — 5 фрагментов/16 KiB;
ответ — 8 KiB, до двух вопросов одновременно. `QUESTION_TIMEOUT=20s` охватывает body,
поиск и ответ и должен быть меньше `HTTP_WRITE_TIMEOUT`. Ошибки: 400/404/408/409/413/415,
сбои модели — 502/503/504; формат `{"error":{"code":"...","message":"..."}}`.
Модель возвращает короткие ID источников, сервер проверяет их и подставляет реальные
chunks. Это подтверждает происхождение ссылок, но не истинность утверждений модели.

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml --profile tools run --build --rm integration
```

Проверка включает unit, vet, race и изолированные PostgreSQL integration-тесты.
