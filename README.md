# Hikari Secretary Backend

![Версия](https://img.shields.io/badge/version-v0.3.3-6A5ACD)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-4169E1?logo=postgresql&logoColor=white)

Go-бэкенд платформы Hikari Secretary. Это модульный монолит: единый владелец
бизнес-данных, авторизации Mini App, API и фоновых задач для бота.

## Возможности

Отмена мероприятия из Bot API и Admin API использует общий Events service:
изменение статуса, уведомления в outbox и аудит записываются атомарно.
Bot API сохраняет ответ-массив, но возвращает пустой список: уведомления
доставляет scheduler, а не обработчик callback. В аудите вызов от бота
отмечается как `bot_service`, вызов Mini App — как `telegram_admin`.

Admin API обслуживает мероприятия и регистрации, участников, публикации и
медиа, отзывы, настройки и историю ИИ. Основные списки поддерживают серверную
пагинацию, фильтрацию и сортировку. История ИИ показывает текущий статус
связанной публикации, поддерживает сортировку по этому статусу и позволяет
получить отдельное задание по ID. См. [OpenAPI](openapi/openapi.yaml) и
[руководство по Admin API](docs/admin-api.md).

Python-бот направляет в этот сервис профили, мероприятия, регистрации,
посещаемость, отзывы, напоминания, публикации и временные диалоги. Генерация,
проверка, история и планирование AI-материалов полностью принадлежат backend;
бот остаётся Telegram UI и delivery-адаптером. SQLite служит только входом
одноразового импортёра.

## Быстрый старт для разработки

В корне интеграционного репозитория создайте `targets.json` по
`targets.example.json` и `.env` по `.env.example`. При первом запуске
на пустом томе задайте `LOCAL_DATABASE_NAME=hikari_dev`. Затем запустите:

```sh
docker compose -f docker-compose.bot-local.yml up -d --build backend
```

Запускаются PostgreSQL, миграции и backend. `LOCAL_DATABASE_NAME` в корневом
`.env` выбирает базу локального стенда. Для работы с копией импортированных данных сначала
восстановите её и укажите имя этой базы.
API остаётся во внутренней сети Docker. Для работы через браузер запустите
`docker compose -f docker-compose.bot-local.yml up -d --build miniapp` из
корня. Этот запуск включает локальный gateway для браузерной авторизации.

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

Проверка применяется ко всем маршрутам `/api/v1/admin/`. Для `/internal/bot/v1/`
используется отдельный сервисный токен. Маршруты `/healthz` и `/readyz`
доступны без авторизации и возвращают состояние сервиса и PostgreSQL.

Для запуска нужны дополнительные переменные окружения:

| Переменная | Назначение |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | Реальный токен того же бота, который открывает Mini App. Нужен только серверу для проверки подписи. |
| `ADMIN_TELEGRAM_IDS` | Список числовых Telegram ID администраторов через запятую. Пустой список не допускается. |
| `TELEGRAM_INIT_DATA_MAX_AGE` | Допустимый возраст подписи, по умолчанию `1h`. |
| `BOT_SERVICE_TOKEN` | Отдельный длинный случайный секрет для запросов Python-бота к внутренним endpoints backend. Не равен токену Telegram-бота. |

Список `ADMIN_TELEGRAM_IDS` определяет права Admin API независимо от записей
об участниках в PostgreSQL. Настройки окружения собраны в [.env.example](.env.example).
Для production Compose параметры DeepSeek, цель, время автопубликации и
количество элементов материала задаются в корневом `.env`.
`AI_TOPIC_MAX_ATTEMPTS` читается из `backend/.env`. При пустом ключе
настройки и история доступны, а генератор отключён.

## Проверки и импорт

Из `backend/`:

```sh
go test ./...
TEST_DATABASE_URL="postgres://..." go test -count=1 -tags=integration ./...
```

Интеграционные тесты требуют отдельную тестовую PostgreSQL с правом
`CREATE SCHEMA` и создают временные схемы.

Для проверки локальной копии SQLite из `backend/` выполните:

```sh
go run ./cmd/import-legacy --sqlite ../data/bot.sqlite3 --json
```

В режиме dry-run соединение с PostgreSQL не требуется. При `--apply`
укажите `DATABASE_URL` или передайте `--database-url`.

Снимок и перенос серверного SQLite выполняются вручную в production-профиле
`legacy-import`. Образ импортёра `hikari-secretary-import-legacy:local`
собирается из `Dockerfile.import-legacy`. По умолчанию импортёр формирует
отчёт dry-run; для записи используйте явный флаг `--apply` после проверки
отчёта. Сначала остановите старого бота и сохраните резервную копию.
После перехода новые записи хранятся только в PostgreSQL.
Порядок команд описан в [инструкции production](https://github.com/jpegMushrum/hikari-secretary/blob/main/docs/production.md).

## Структура

```text
cmd/api/                 # HTTP API
cmd/migrate/             # применение SQL-миграций
cmd/import-legacy/       # перенос SQLite в PostgreSQL
internal/platform/       # конфигурация, PostgreSQL, мигратор и HTTP-инфраструктура
migrations/              # упорядоченные SQL-миграции
openapi/                 # версионируемый контракт API
```

## Границы

- Backend хранит рабочие данные в PostgreSQL. Mini App и Python-бот
  обращаются к данным через API.
- API работает за reverse proxy; TLS не завершается самим Go-сервисом.
