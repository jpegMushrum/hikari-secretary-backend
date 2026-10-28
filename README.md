# Hikari Secretary Backend

![Версия](https://img.shields.io/badge/version-v0.2.0--dev-6A5ACD)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-4169E1?logo=postgresql&logoColor=white)

Go-бэкенд платформы Hikari Secretary. Это модульный монолит: единый владелец
бизнес-данных, авторизации Mini App, API и фоновых задач для бота.

## Текущий этап

В `v0.2.0-dev` реализованы фундамент и контур доступа, но ещё нет предметных
API:

- конфигурация окружения и безопасное завершение HTTP-сервера;
- PostgreSQL в Docker Compose;
- версионируемые SQL-миграции и отдельный контейнер-мигратор;
- `GET /healthz` и `GET /readyz`;
- проверка подписи и срока действия Telegram Mini App `initData`;
- допуск в административный API только Telegram ID из конфигурации;
- начальный OpenAPI-контракт и закрытый `GET /api/v1/admin/me`.

Мероприятия, регистрации, отзывы и публикации будут добавляться следующими
вертикальными срезами. Текущий Python-бот `v0.9.0` пока продолжает работать со
своей SQLite-базой и не зависит от этого сервиса.

## Быстрый старт для разработки

```bash
cp .env.example .env
docker compose up --build
```

После запуска:

```bash
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

Порты по умолчанию привязаны только к `127.0.0.1`, поэтому база и API не
становятся общедоступными. В интеграционном production Compose доступ к Mini
App будет организован через отдельный HTTPS reverse proxy на `8443`.

## Доступ Mini App

Backend не доверяет данным, которые браузер Mini App может сформировать сам.
Клиент передаёт **исходную** строку `Telegram.WebApp.initData` в заголовке:

```http
Authorization: tma <Telegram.WebApp.initData>
```

Backend проверяет HMAC-подпись Telegram и время `auth_date`; по умолчанию
строка действительна один час. Затем он сверяет `user.id` с
`ADMIN_TELEGRAM_IDS`. Поэтому изменённые и устаревшие данные получают `401`, а
пользователь Telegram без роли администратора — `403`.

Это правило обязательно для каждого будущего endpoint с данными. Единственные
незащищённые пути — `/healthz` и `/readyz`; они не содержат пользовательских
или предметных данных.

Для запуска нужны дополнительные переменные окружения:

| Переменная | Назначение |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | Реальный токен того же бота, который открывает Mini App. Нужен только серверу для проверки подписи. |
| `ADMIN_TELEGRAM_IDS` | Список числовых Telegram ID администраторов через запятую. Пустой список не допускается. |
| `TELEGRAM_INIT_DATA_MAX_AGE` | Допустимый возраст подписи, по умолчанию `1h`. |

Токен и рабочий `.env` не коммитятся. На этом этапе список ролей намеренно
хранится в конфигурации: таблицы пользователей и постоянная модель ролей будут
добавлены вместе с первым предметным API. До этого конфигурация остаётся
единственным источником прав доступа.

Для проверки конфигурации без рабочего `.env`:

```bash
BACKEND_ENV_FILE=.env.example docker compose config --quiet
```

## Команды

```bash
docker compose up --build                 # база, мигратор и API
docker compose logs -f api                # логи API
docker compose down                       # остановка контейнеров
docker compose down -v                    # удалить только локальный том PostgreSQL
docker run --rm --mount type=bind,src="$(pwd)",dst=/src,readonly \
  -w /src golang:1.27.3-alpine go test ./... # тесты в контейнере
```

Последняя команда не использует работающую базу: тесты текущего этапа покрывают
конфигурацию, проверку Telegram-подписи, роли и HTTP endpoints.

## Структура

```text
cmd/api/                 # HTTP API
cmd/migrate/             # применение SQL-миграций
internal/platform/       # конфигурация, PostgreSQL, мигратор и HTTP-инфраструктура
migrations/              # упорядоченные SQL-миграции
openapi/                 # версионируемый контракт API
```

## Границы

- PostgreSQL станет единственным хранилищем после переноса данных с SQLite.
- Mini App и Python-бот не будут подключаться к PostgreSQL напрямую.
- Модель постоянных ролей и доменные endpoints будут добавлены до подключения
  пользовательских интерфейсов; временный allowlist администраторов уже
  закрывает текущий API.
- API работает за reverse proxy; TLS не завершается самим Go-сервисом.
