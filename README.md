# DocMind

Локальный сервис на Go: загружаете TXT, Markdown или PDF, задаёте вопрос,
получаете ответ с фрагментами источников. Веб-интерфейс со светлой и тёмной
темой, выбором документов и раскрываемыми цитатами.

**Стек:** Go · net/http · PostgreSQL · pgvector · Docker Compose.
API сохраняет файлы и задания; worker извлекает текст и строит индекс.
Вопрос проходит через поиск по выбранным документам и модель ответа.

## Запуск

Нужен Docker Desktop с Linux-контейнерами. Из корня проекта в PowerShell:

```powershell
if (-not (Test-Path .env)) { Copy-Item .env.example .env }
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
```

Откройте [localhost:8080](http://localhost:8080): загрузите документ,
дождитесь статуса «Готов», выберите его и задайте вопрос.
Тема учитывает настройки системы; кнопка в шапке сохраняет ваш выбор.
Анимации отключаются при системной настройке «уменьшить движение».
Порты меняются через `API_PORT` и `POSTGRES_PORT` в `.env`.

По умолчанию `fake`: без ключей и оплаты, ответ — цитата по совпадению слов.
Для Alice AI укажите в `.env` `CHAT_PROVIDER=yandex`, `EMBEDDING_PROVIDER=yandex`,
`YANDEX_API_KEY` и `YANDEX_FOLDER_ID`. Найденные фрагменты и вопросы отправляются
Яндексу; вызовы платные. При смене embeddings нужна переиндексация.

## Проверки

```powershell
node --test tests/frontend/workspace.test.js
docker compose --env-file .env -f deploy/docker-compose.yml --profile tools run --build --rm integration
```

CI проверяет форматирование, Go vet/race, frontend, PostgreSQL и полный HTTP-сценарий
с fake-провайдерами. `/healthz` — процесс, `/readyz` — БД, `/metrics` — HTTP-метрики.

## Ограничения

Один пользователь, локальный запуск, без авторизации. Файл до 10 MiB;
PDF только с текстовым слоем, без OCR. Вопрос — до 4000 байт и 20 документов.
Источники проверяются сервером; выводы модели нужно сверять с текстом.
Compose `down` сохраняет данные, `down -v` удаляет. Перед обновлением
старого индекса остановите API/worker и сохраните резервную копию БД.
Тесты с подменой HTTP не подтверждают работу живого Yandex API.

[API / OpenAPI](api/openapi.yaml)
