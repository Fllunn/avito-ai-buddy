# Проверка серверного стенда

Дата: 14.11.2026. Адрес: https://semenovm.ru/avito_buddy/. Развёртывание разрешено пользователем в чате.

- Спецификация deploy-semenovm-subpath: openspec validate --strict проходит.
- Frontend: suite coverage проходит, отчёт — 95,97% строк; отдельные 3 теста URL проходят. TypeScript и сборка с VITE_BASE_PATH=/avito_buddy/ проходят.
- Compose config и nginx -t проходят. Пять сервисов healthy; опубликован только loopback-порт UI. У всех сервисов restart unless-stopped.
- HTTPS: /avito_buddy → 301, /avito_buddy/ → 200, API /me без сессии → 401.
- Браузерный smoke: вход Алины, план онбординга, вопрос о правилах отгула, ответ, ссылка на документ и перезагрузка проходят. Cookie HttpOnly/Secure с Path=/avito_buddy/. Ошибок JavaScript и неудачных запросов ресурсов не обнаружено.
- Перезапуск всех пяти сервисов: контрольная сумма истории и число сессий совпали до и после; после запуска все сервисы healthy и HTTPS отвечает.
- Скриншот: output/deployment/smoke.png. Повторная проверка: node frontend/test/e2e/deployment-smoke.mjs.
- Остальные адреса: / — 200 до и после, /alpha26/ — 404 до и после (исходное состояние).
- Резервная копия nginx: /etc/nginx/sites-available/cv.before-avito-buddy-20261114T123234Z.

Используется fake LLM. Реальная LLM и корпоративные интеграции этой проверкой не подтверждаются.
