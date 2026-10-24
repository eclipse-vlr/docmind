# DocMind

Go backend: загрузка TXT/Markdown/PDF → извлечение текста → embeddings → поиск
по выбранным документам. PostgreSQL + pgvector, отдельный worker, Docker Compose.
Ответы на вопросы с генерацией текста пока не реализованы.

```powershell
if (-not (Test-Path .env)) { Copy-Item .env.example .env }
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
curl.exe http://localhost:8080/readyz
curl.exe -F 'file=@example.txt' http://localhost:8080/api/v1/documents
curl.exe http://localhost:8080/api/v1/documents/UUID
# После статуса ready:
docker compose --env-file .env -f deploy/docker-compose.yml exec api /docmind-search -documents UUID -question 'PostgreSQL' -top-k 5
# Повторная индексация:
docker compose --env-file .env -f deploy/docker-compose.yml exec api /docmind-reindex -document UUID
```

Нужен Docker Desktop с Linux containers. API: `localhost:8080`, БД: `localhost:55432`.
Есть список `GET /api/v1/documents?limit=20&offset=0` и `DELETE /api/v1/documents/UUID`.
Исходники, задания и фрагменты хранятся в volumes и переживают перезапуск.

По умолчанию `EMBEDDING_PROVIDER=fake`: детерминированный поиск по совпадению слов,
без сети, ключей и семантики синонимов. Для OpenAI укажите в `.env`
`EMBEDDING_PROVIDER=openai` и `OPENAI_API_KEY`: текст отправляется провайдеру,
запросы платные. Worker и поиск должны использовать один провайдер.
После смены провайдера выполните reindex нужных документов.

Состояния: `queued` → `processing` → `ready` / `failed`.
`ready` означает, что текст и vectors сохранены одной транзакцией.
Lease, номер попытки и generation блокируют запись устаревшего worker;
временные ошибки повторяются до трёх попыток. Reindex заменяет фрагменты без дублей.
Сбой после запроса embeddings может привести к повторному платному вызову.

Лимиты: исходник 10 MiB, текст 2 MiB, 4096 фрагментов по 1000 символов
с перекрытием 200. PDF: текстовый слой без OCR, номера страниц сохраняются.
Для PDF действуют ограничения памяти 512 MiB и CPU 20 секунд.
Локальный однопользовательский MVP без авторизации.

При обновлении с версии без embeddings сначала остановите старые workers:
`docker compose --env-file .env -f deploy/docker-compose.yml stop worker`,
затем выполните обычную команду `up -d --build` выше. Миграция переиндексирует
готовые документы. `down` сохраняет данные; `down -v` удаляет их.

Проверки (unit, vet, race, изолированные PostgreSQL integration):

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml --profile tools run --build --rm integration
```
