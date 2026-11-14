# Демонстрационный стенд semenovm.ru

Адрес: https://semenovm.ru/avito_buddy/. Проект на сервере: `/opt/avito-buddy`; Compose project: `avito-buddy`. Используется fake LLM и только синтетические данные. Панель ведущего публично не опубликована.

## Сборка

На машине с Go и Node версий из go.mod/package.json создать каталог `runtime/` в корне копии релиза, не в git (`mkdir -p runtime`). Из backend и mocks соответственно выполнить:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o ../runtime/api ./cmd/server
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o ../runtime/mocks ./cmd/server
```

Во frontend выполнить `VITE_BASE_PATH=/avito_buddy/ npm run build`, скопировать dist в runtime/dist. Передать compose.yaml, .dockerignore, infra/, frontend/nginx.conf и runtime/ на сервер. Файлы .env не передавать из рабочей копии: отдельные случайные секреты и пароли создаются на сервере, права .env — 600.

## Управление на сервере

```sh
cd /opt/avito-buddy
docker compose -p avito-buddy -f compose.yaml -f infra/deploy.compose.yaml config --quiet
docker compose -p avito-buddy -f compose.yaml -f infra/deploy.compose.yaml up -d --build --wait
docker compose -p avito-buddy -f compose.yaml -f infra/deploy.compose.yaml ps
docker compose -p avito-buddy -f compose.yaml -f infra/deploy.compose.yaml logs --tail=100 api mocks
```

В HTTPS server nginx подключён `/etc/nginx/snippets/avito-buddy.conf` из infra/avito-buddy.nginx.conf. Перед изменениями сохранять копию текущего vhost, выполнять `nginx -t` перед reload. Публичен только HTTPS через nginx; UI слушает 127.0.0.1:25173. Томам БД/Redis задан отдельный namespace Compose. Обычный restart сохраняет данные. `down --volumes` удаляет демо-данные и для обновления не нужен.

## Проверка и откат

Проверить перенаправление `/avito_buddy`, загрузку статики, 401 на API без сессии, вход демо-профиля, чат и план, сохранение истории после restart. Проверить прежние `/` и `/alpha26/`. Для отката публикации восстановить резервную копию vhost, выполнить `nginx -t` и reload, остановить только проект avito-buddy без удаления томов.

Панель ведущего при необходимости доступна оператору через SSH и временный localhost-прокси к контейнеру mocks; DEMO_ADMIN_SECRET хранится только в серверном .env.

Проверка опубликованного стенда из корня репозитория (создаёт демо-сессию и вопрос о правилах отгула):

```sh
node frontend/test/e2e/deployment-smoke.mjs
```

Скрипт проверяет TLS, серверный отказ без сессии, cookie, вход, план, чат, ссылки документов и перезагрузку; снимок сохраняется в output/deployment/smoke.png.
