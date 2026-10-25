# DocMind

Go backend: загрузка TXT/Markdown/PDF → извлечение текста → embeddings → поиск
по выбранным документам → ответ с источниками. PostgreSQL + pgvector, отдельный
worker, Docker Compose.

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

## Вопросы по документам

После перехода документов в `ready` отправьте JSON на `POST /api/v1/questions`:

```powershell
$body = @{ question = 'PostgreSQL'; document_ids = @('UUID_ДОКУМЕНТА') } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/api/v1/questions' `
  -ContentType 'application/json; charset=utf-8' -Body ([System.Text.Encoding]::UTF8.GetBytes($body))
```

Ответ содержит `answer` и массив `sources`: `document_id`, `chunk_id`, `page`
(номер страницы PDF или `null`) и `snippet` с реальным текстом фрагмента.
При нехватке данных возвращается «В выбранных документах недостаточно информации
для ответа.» и `sources: []`.

По умолчанию `CHAT_PROVIDER=fake`: вместо генерации возвращается исходный
фрагмент с наибольшим совпадением слов вопроса. Это проверка сквозного сценария
без сети и оплаты; fake не понимает синонимы и не делает смысловые выводы.
Для генерации укажите `CHAT_PROVIDER=openai` и `OPENAI_API_KEY`.
Используется [GPT-5.6 Terra](https://developers.openai.com/api/docs/models/gpt-5.6-terra)
через Responses API с [JSON-схемой ответа](https://developers.openai.com/api/docs/guides/structured-outputs),
`store=false`, `reasoning.effort=low`, максимум 2048 выходных токенов.
Отправляются вопрос и найденные фрагменты. Платные вызовы не повторяются автоматически.
Chat-провайдер выбирается независимо от embeddings; смена только chat не требует reindex.

Документы и вопрос передаются модели как недоверенные данные. Системная инструкция
требует отвечать по контексту и игнорировать инструкции внутри документов.
Модель выбирает короткие ID источников, сервер проверяет их и подставляет метаданные
из результатов поиска. Неизвестный или повторный ID, ответ без источников,
оборванный JSON и отказ провайдера дают безопасную ошибку `502`.
Такая проверка подтверждает происхождение ссылок, но не доказывает смысловую
корректность каждого утверждения модели. Источники соответствуют снимку данных
на момент поиска; последующее удаление или reindex может изменить набор фрагментов.

Ограничения: вопрос — 4000 байт UTF-8, 1–20 уникальных UUID, JSON body — 16 KiB,
контекст — до пяти фрагментов и 16 KiB текста, ответ — до 8 KiB.
На один экземпляр API допускаются два одновременных запроса вопросов.
`QUESTION_TIMEOUT=20s` ограничивает обработку и должен быть меньше
`HTTP_WRITE_TIMEOUT`; `CHAT_HTTP_TIMEOUT=10s` ограничивает вызов chat API.
Дедлайн вопроса действует также во время чтения и закрытия HTTP body.
Он не продлевает более ранний `HTTP_READ_TIMEOUT`: истечение бюджета вопроса
возвращает `504`, отдельного таймаута чтения body — `408`.
При прерывании чтения слот освобождается, незавершённое HTTP/1-соединение закрывается.
Вопросы, тексты документов, ответы и ключи не записываются в логи.

Ошибки: `400` — запрос некорректен; `404` — документ отсутствует;
`408` — отмена запроса или таймаут чтения body;
`409` — документ не готов или требует reindex после смены embeddings;
`413` — body слишком большой; `415` — неподдерживаемый формат;
`502` — некорректный ответ или отказ модели; `503` — временная недоступность
или исчерпан лимит параллельных запросов (`Retry-After: 1`); `504` — таймаут.
Формат ошибок: `{"error":{"code":"...","message":"..."}}`.

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
