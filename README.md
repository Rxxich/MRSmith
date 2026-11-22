# MRSmith

Самохостящаяся панель для rule-providers [mihomo](https://github.com/MetaCubeX/mihomo): собирает списки доменов и IP из ссылок и файлов, убирает дубли, конвертирует между MRS, YAML, TXT и LIST и раздаёт результат по ссылкам вашего домена. Внутри — S3-подобное хранилище для любых файлов с предпросмотром картинок.

Один бинарник на Go со встроенным интерфейсом, образ на distroless без shell, работает от непривилегированного пользователя.

![Обзор панели MRSmith](docs/screenshot.png)

## Что умеет

**Наборы правил**
- Источники — ссылки (`http/https`) и файлы: с компьютера перетаскиванием или из хранилища.
- Читает MRS, YAML (`payload:`), текст mihomo, классический LIST (`DOMAIN-SUFFIX,…`), hosts, AdBlock (`||example.com^`), dnsmasq (`server=/…/`), v2fly (`domain:`, `full:`).
- Склеивает несколько источников в один файл без дублей. Убирает перекрытые домены: `mail.google.com` не нужен при `+.google.com`. Склеивает соседние IP-сети в минимальный список.
- Ручные правки переживают обновления. Можно добавить домен или IP либо исключить его. IP-исключение вырезается из более широкой сети: `10.0.0.0/8` минус `10.1.0.0/16`.
- Обновление по расписанию: от 5 минут до недели. Запросы условные (`ETag` / `If-Modified-Since`), так что неизменённые источники не скачиваются заново. В истории видно, что добавилось и пропало.
- Каждый набор публикуется в выбранных форматах: `https://ваш-домен/r/<имя>.mrs|yaml|txt|list`. Есть приватный режим со ссылкой по токену.
- MRS-файл после сборки декодируется обратно и сверяется с набором. Испорченный файл не уйдёт клиентам. Формат проверен на совместимость с исходниками mihomo v1.19: файл совпадает с тем, что пишет сам mihomo, байт в байт.
- Панель показывает готовый блок `rule-providers:` для конфига.
- Загрузку с GitHub можно пустить через SOCKS5-прокси (`socks5://` или `socks5h://`), остальные ссылки скачиваются напрямую.
- Уведомления в Telegram: когда обновление из источников принесло новые правила или убрало старые, бот пришлёт, что именно изменилось и в каком файле.
- У каждой ссылки есть счётчик скачиваний, а в настройках видно, кто скачивал: IP, программа (mihomo, Clash Verge, браузер…), какие файлы и когда.

**Аудит** — проверяет все наборы и файлы правил разом и показывает пересечения: `admin.twitch.tv` есть в файле A, а в наборе B уже стоит `+.twitch.tv`; одна и та же сеть в трёх списках; лишние строки внутри одного файла. Предлагает, откуда убрать, и исправляет по одной находке или пачкой («убрать из A всё, что уже есть в B»).

**Конвертер** — разовая конвертация файла, ссылки или вставленного текста в любой формат: скачать, сохранить в хранилище или превратить в набор. Смешанный список (`DOMAIN-SUFFIX`, `DOMAIN-KEYWORD`, `IP-CIDR`…) тоже можно перевести в MRS: в файл попадут домены или IP-сети, а панель покажет, что не вошло.

**Хранилище** — бакеты (публичные и приватные), папки, перетаскивание файлов и целых папок в окно, предпросмотр PNG/JPEG/GIF/WebP/SVG и текстовых файлов, прямые ссылки `https://ваш-домен/f/<бакет>/<путь>` со счётчиком скачиваний.

Два служебных бакета есть всегда:
- `downloads` — последние скачанные копии ссылок из наборов, по папке на набор. Панель обновляет копию при каждой проверке, удаляет её, когда ссылку меняют или убирают, и удаляет папку вместе с набором. Смотреть и скачивать можно, менять нельзя.
- `rule-sources` — файлы, загруженные с компьютера как источники. Файл удаляется, когда его больше не использует ни один набор.

## Установка

Нужен сервер с Docker и плагином compose. Docker сам скачает готовый образ. Панель слушает только `127.0.0.1:8090`, а HTTPS даёт reverse proxy, который вы выберете сами: nginx, Caddy или прокси уже установленной панели Remnawave. Примеры конфигов лежат в [`examples/`](examples/).

**Шаг 1.** Создайте папку:

```bash
mkdir /opt/mrsmith && cd /opt/mrsmith
```

**Шаг 2.** Скачайте compose-файл и шаблон настроек:

```bash
curl -o docker-compose.yml https://raw.githubusercontent.com/Rxxich/MRSmith/refs/heads/main/docker-compose.yml && curl -o .env https://raw.githubusercontent.com/Rxxich/MRSmith/refs/heads/main/.env.sample
```

**Шаг 3.** Заполните `.env`:

```bash
nano .env
```

Обязательно только `MRSMITH_PUBLIC_URL` — адрес, по которому будет открываться панель, например `https://rules.example.com`. Там же можно спрятать панель под секретный путь (`MRSMITH_PANEL_PATH`), включить Telegram и прокси для GitHub. Все переменные описаны в разделе «Настройки».

**Шаг 4.** Запустите:

```bash
docker compose up -d
```

**Шаг 5.** Настройте reverse proxy на `127.0.0.1:8090` — примеры ниже.

### Первый вход

Логин и пароль администратора не хранятся в конфигах: при первом запуске панель сама создаёт пользователя со случайным логином (вида `smith-k7x2qm`) и паролем и один раз печатает их в лог:

```bash
docker compose logs mrsmith | grep -A4 "создан администратор"
```

После входа панель попросит сменить пароль. Логин тоже можно заменить на свой в настройках.

## Reverse proxy

### nginx

Полный пример — [`examples/nginx.conf`](examples/nginx.conf). Главное в нём:

```nginx
upstream mrsmith {
    server 127.0.0.1:8090;
}

server {
    server_name rules.example.com;
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;

    client_max_body_size 110m;

    location / {
        proxy_pass http://mrsmith;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_request_buffering off;
        proxy_read_timeout 600s;
    }

    ssl_certificate     /etc/letsencrypt/live/rules.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/rules.example.com/privkey.pem;
}
```

Сертификат можно выпустить через `certbot certonly --nginx -d rules.example.com`.

### Caddy

Caddy выпускает сертификат сам ([`examples/Caddyfile`](examples/Caddyfile)):

```caddy
rules.example.com {
	reverse_proxy 127.0.0.1:8090
}
```

Какой бы прокси ни стоял, он должен передавать `Host` и `X-Forwarded-Proto: https`. Без этого вход не сработает, и панель прямо об этом скажет.

### Рядом с панелью Remnawave

Compose панели Remnawave создаёт свою сеть `remnawave-network`. MRSmith подключается к ней вместо того, чтобы открывать порт. Тогда nginx панели видит его по имени `mrsmith:8080`. Понадобится отдельный поддомен, например `rules.example.com`.

На шаге 2 возьмите compose-файл для этого случая:

```bash
curl -o docker-compose.yml https://raw.githubusercontent.com/Rxxich/MRSmith/refs/heads/main/examples/remnawave/docker-compose.yml && curl -o .env https://raw.githubusercontent.com/Rxxich/MRSmith/refs/heads/main/.env.sample
```

Он отличается от обычного двумя местами: в нём нет `ports`, и сервис подключён к сети панели. Если правите свой compose, допишите:

```yaml
services:
  mrsmith:
    # ports: ... — уберите
    networks:
      - remnawave-network

networks:
  remnawave-network:
    name: remnawave-network
    external: true
```

Сначала должна быть запущена Remnawave, иначе сети ещё нет. Затем добавьте в `nginx.conf` панели upstream и server для поддомена — полный блок в [`examples/remnawave/nginx.conf`](examples/remnawave/nginx.conf):

```nginx
upstream mrsmith {
    server mrsmith:8080;
}

server {
    server_name rules.example.com;

    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;

    client_max_body_size 110m;

    location / {
        proxy_http_version 1.1;
        proxy_pass http://mrsmith;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_request_buffering off;
        proxy_read_timeout 600s;
    }

    ssl_certificate "/etc/nginx/ssl/rules_fullchain.pem";
    ssl_certificate_key "/etc/nginx/ssl/rules_privkey.key";
    # остальные ssl_* и gzip — как в server-блоке панели
}
```

Сертификат для поддомена выпустите так же, как для панели, и смонтируйте в контейнер nginx рядом с её сертификатом. Блоки `map $http_upgrade` и `default_server` с `ssl_reject_handshake` в конфиге Remnawave уже есть — повторять их не нужно. После правки:

```bash
docker exec remnawave-nginx nginx -t && docker exec remnawave-nginx nginx -s reload
```

Если у Remnawave вместо nginx стоит Caddy, хватит блока `rules.example.com { reverse_proxy mrsmith:8080 }`.

## Подключение в mihomo

На странице набора нажмите значок `</>` у нужного формата — панель покажет готовый блок. Пример:

```yaml
rule-providers:
  telegram-ip:
    type: http
    behavior: ipcidr
    format: mrs
    url: https://rules.example.com/r/telegram-ip.mrs
    interval: 21600

rules:
  - RULE-SET,telegram-ip,PROXY
```

| Файл | behavior | format | Что внутри |
|------|----------|--------|------------|
| `.mrs` | `domain` или `ipcidr` | `mrs` | бинарный формат mihomo, самый компактный |
| `.yaml` | как у набора | `yaml` | `payload:` со списком правил |
| `.txt` | как у набора | `text` | по правилу в строке: `+.example.com`, `1.2.3.0/24` |
| `.list` | `classical` | `text` | `DOMAIN-SUFFIX,example.com`, `IP-CIDR,1.2.3.0/24` |

MRS хранит либо только домены, либо только IP. Если список смешанный, MRS всё равно соберётся:
- `DOMAIN` и `DOMAIN-SUFFIX` становятся `example.com` и `+.example.com`, `DOMAIN-WILDCARD,*.x.com` — `*.x.com`;
- простые `DOMAIN-REGEX` переводятся в домены: `^(.*\.)?example\.com$` → `+.example.com`, `^example\.com$` → `example.com`, `^.+\.example\.com$` → `.example.com`, `^[^.]+\.example\.com$` → `*.example.com`;
- в файл идут домены или IP-сети — чего больше. Вторую часть можно получить отдельным файлом: в конвертере кнопкой «Сделать MRS из IP-сетей», в наборах — вторым набором с behavior `ipcidr`;
- `DOMAIN-KEYWORD`, сложные `DOMAIN-REGEX`, `GEOIP`, `PROCESS-NAME` и прочие правила MRS хранить не умеет — панель покажет, сколько их и каких.

Остальные форматы набора (YAML, TXT, LIST) при этом сохраняют весь список, а в блоке для mihomo у MRS будет свой behavior.

Синтаксис доменов: `example.com` — только сам домен, `+.example.com` — домен со всеми поддоменами, `.example.com` — только поддомены, `*.example.com` — ровно один уровень поддомена.

## Прокси для GitHub

Если GitHub с сервера открывается медленно или не открывается, укажите SOCKS5-прокси в `.env`:

```env
MRSMITH_GITHUB_PROXY=socks5h://user:password@proxy.example.com:1080
```

Через прокси идут только адреса GitHub: `github.com`, `raw.githubusercontent.com`, `objects.githubusercontent.com`, `*.github.io` и другие поддомены `github.com` и `githubusercontent.com`. Остальные источники скачиваются напрямую. Логин и пароль необязательны.

- `socks5h://` — домен отправляется прокси, и он сам узнаёт IP. Подходит, когда DNS на сервере отвечает неправильно.
- `socks5://` — IP узнаёт сам сервер, прокси получает готовый адрес.

Если прокси работает на том же сервере, контейнер видит его по адресу `host.docker.internal` (это имя прописано в compose-файле), например `socks5h://host.docker.internal:1080`. Прокси должен слушать не только `127.0.0.1`, а и адрес сети Docker (`172.17.0.1` или `0.0.0.0` за файрволом).

Проверить настройку можно в разделе «Настройки» → «GitHub и Telegram» и кнопкой «Обновить» у набора с источником на GitHub: если прокси недоступен, у источника появится ошибка с его адресом.

## Уведомления в Telegram

1. Создайте бота у [@BotFather](https://t.me/BotFather) и скопируйте токен.
2. Узнайте свой числовой ID, например у [@userinfobot](https://t.me/userinfobot).
3. Откройте своего бота и нажмите «Запустить» — без этого Telegram не даст боту написать первым.
4. Добавьте в `.env` и перезапустите контейнер:

```env
TELEGRAM_TOKEN=123456789:AA...
ADMIN_ID=123456789
# если api.telegram.org с сервера недоступен:
# TELEGRAM_PROXY=socks5h://user:password@proxy.example.com:1080
```

Получателей может быть несколько, через запятую. Для группы укажите её ID (он начинается с `-100`) и добавьте бота в группу.

Бот пишет, когда обновление по расписанию или по кнопке «Обновить» изменило содержимое набора: сколько правил добавилось и пропало, примеры строк и имя файла. Сборка после ручной правки уведомление не присылает. Уведомления включаются для каждого набора отдельно: в его настройках, переключатель «Сообщать в Telegram» (по умолчанию выключен). Кнопка «Отправить проверочное сообщение» в настройках панели проверяет токен, ID и прокси.

## Кто скачивал

Панель считает запросы к публичным ссылкам `/r/` и `/f/`:
- скачивание — клиент получил файл целиком;
- проверка — клиент спросил, нет ли новой версии, и получил ответ «не изменилось» (mihomo делает так, если у него уже есть свежая копия).

Счётчики видны на странице набора, в списке наборов и в таблице бакета. В «Настройки» → «Кто скачивал» собраны клиенты: IP, программа по User-Agent, какие файлы запрашивал, сколько раз и когда последний раз. Журнал хранит последние 10 000 запросов. Скачивания из самой панели не считаются. IP берётся из `X-Forwarded-For` только от доверенного reverse proxy (`MRSMITH_TRUSTED_PROXIES`).

## Настройки

Всё задаётся переменными окружения в `.env`.

| Переменная | По умолчанию | Назначение |
|------------|--------------|------------|
| `MRSMITH_PUBLIC_URL` | адрес из браузера | база для ссылок, например `https://rules.example.com` |
| `MRSMITH_PANEL_PATH` | `/` | секретный путь к панели, например `/k7x2-panel/`. Ссылки `/r/` и `/f/` остаются на месте |
| `MRSMITH_MAX_UPLOAD_MB` | `100` | максимальный размер загружаемого файла |
| `MRSMITH_MAX_FETCH_MB` | `64` | максимальный размер источника по ссылке |
| `MRSMITH_FETCH_TIMEOUT` | `60s` | таймаут скачивания источника |
| `MRSMITH_FETCH_ALLOW_CIDRS` | — | диапазоны, которые можно скачивать несмотря на защиту от SSRF |
| `MRSMITH_ALLOW_PRIVATE_FETCH` | `false` | разрешить скачивать с адресов внутренней сети |
| `MRSMITH_GITHUB_PROXY` | — | SOCKS5-прокси для GitHub: `socks5://` или `socks5h://[логин:пароль@]хост:порт` |
| `TELEGRAM_TOKEN` | — | токен бота от @BotFather, включает уведомления |
| `ADMIN_ID` | — | числовой Telegram ID получателя, несколько — через запятую |
| `TELEGRAM_PROXY` | — | SOCKS5-прокси для связи с Telegram, формат как у `MRSMITH_GITHUB_PROXY` |
| `MRSMITH_SESSION_TTL` | `168h` | максимальная длительность сессии |
| `MRSMITH_SESSION_IDLE` | `24h` | сессия завершается после такого простоя |
| `MRSMITH_TRUSTED_PROXIES` | частные сети | откуда принимать `X-Forwarded-For` и `X-Forwarded-Proto` |
| `MRSMITH_COOKIE_SECURE` | `true` | `false` — только для локальной разработки по HTTP |
| `MRSMITH_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Безопасность

- Секретов в конфигах нет: логин и пароль администратора генерируются при первом запуске, панель требует сменить пароль.
- Пароли хранятся как argon2id. Сессия — случайный 256-битный токен в cookie `__Host-` с `HttpOnly`, `Secure` и `SameSite=Strict`; в базе лежит только его SHA-256. Сессии можно посмотреть и завершить в настройках, смена пароля завершает остальные.
- Перебор паролей ограничен: после 5 ошибок с одного IP (IPv6 — по /64) вход блокируется на 5 минут, и каждый следующий блок вдвое длиннее. Время ответа для несуществующего логина такое же, как для существующего.
- От CSRF защищают токен в заголовке, проверка Fetch Metadata и `SameSite=Strict`.
- Строгий CSP без inline-скриптов и сторонних доменов: шрифты и код встроены в бинарник. Плюс `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy: no-referrer`, HSTS за HTTPS-прокси.
- Защита от SSRF: адрес проверяется в момент соединения, после DNS, поэтому DNS-rebinding не проходит. Внутренние сети и cloud-metadata закрыты. Размер и время скачивания ограничены, редиректы проверяются. Через `socks5://` действуют те же проверки; через `socks5h://` прокси получает только домены GitHub.
- Токен Telegram не попадает в логи и ответы API: из ошибок соединения адрес запроса вырезается. Логин и пароль прокси панель тоже не показывает.
- Файлы лежат на диске под случайными ID, пользовательские имена в файловую систему не попадают — path traversal невозможен. Пользовательский контент отдаётся в CSP-песочнице, так что загруженный HTML или SVG не может обратиться к панели.
- Разбор MRS проверяет все длины до выделения памяти и ограничивает распакованный размер (защита от zip-бомб).
- Контейнер: distroless `nonroot`, `read_only`, `cap_drop: ALL`, `no-new-privileges`. Наружу смотрит только reverse proxy.
- CI запускает тесты, `govulncheck` и `npm audit`, а раз в неделю перепроверяет зависимости на свежие CVE.

## Обслуживание

Обновление:

```bash
cd /opt/mrsmith && docker compose pull && docker compose up -d
```

Резервная копия — содержимое тома `mrsmith-data` (база `mrsmith.db`, файлы, кэш источников):

```bash
docker run --rm -v mrsmith-data:/data -v "$PWD":/backup alpine tar czf /backup/mrsmith-backup.tgz -C /data .
```

Сброс пароля, если вход потерян (новый логин и пароль появятся в выводе, все сессии завершатся):

```bash
docker compose stop mrsmith && docker compose run --rm mrsmith reset-password && docker compose start mrsmith
```

## Разработка

Нужны Go 1.26.11+ и Node 24.

```bash
cd frontend && npm ci && npm run build
```

```bash
cd backend && go test ./...
```

Локальный запуск: создайте `.env.local` в корне с `MRSMITH_LISTEN=127.0.0.1:8080` и `MRSMITH_COOKIE_SECURE=false`, затем `scripts/dev.ps1` (Windows) или `scripts/dev.sh`. Панель откроется на `http://127.0.0.1:8080`, данные будут лежать в `.localdata/`. Для правок интерфейса удобен `npm run dev` во `frontend/`: Vite проксирует API на запущенный бэкенд.

Структура:

```
backend/
  cmd/mrsmith/         точка входа: serve, healthcheck, reset-password
  internal/rules/      разбор форматов, дедупликация, MRS-кодек
  internal/rulesets/   наборы: источники, сборка, расписание, история
  internal/storage/    S3-подобное хранилище
  internal/auth/       пароли, сессии, ограничение попыток входа
  internal/fetch/      загрузчик с защитой от SSRF
  internal/server/     HTTP API, публичные ссылки, раздача интерфейса
frontend/              React + Vite, собирается внутрь backend/internal/webui/dist
docker-compose.yml     установка из готового образа, .env.sample — шаблон настроек
examples/              nginx, Caddy и установка рядом с Remnawave
```

Образ `ghcr.io/rxxich/mrsmith` собирает workflow [`.github/workflows/docker.yml`](.github/workflows/docker.yml) для amd64 и arm64: при пуше тега `v*` (теги `1.0.13`, `1.0`, `latest`) или вручную из вкладки Actions. Собрать образ локально: `docker build -t mrsmith:local .`
