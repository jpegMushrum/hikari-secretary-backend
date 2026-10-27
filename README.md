# Hikari Secretary Backend

![Версия](https://img.shields.io/badge/version-v0.1.0--dev-6A5ACD)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-4169E1?logo=postgresql&logoColor=white)

Go-бэкенд платформы Hikari Secretary. Это модульный монолит: единый владелец
бизнес-данных, авторизации Mini App, API и фоновых задач для бота.

## Текущий этап

В `v0.1.0-dev` реализован фундамент, но ещё нет предметных API:

- конфигурация окружения и безопасное завершение HTTP-сервера;
- PostgreSQL в Docker Compose;
- версионируемые SQL-миграции и отдельный контейнер-мигратор;
- `GET /healthz` и `GET /readyz`;
- начальный OpenAPI-контракт.

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
конфигурацию и HTTP health endpoints.

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
- Проверка Telegram `initData`, модель ролей и доменные эндпоинты добавляются
  до подключения пользовательских интерфейсов.
- API работает за reverse proxy; TLS не завершается самим Go-сервисом.
