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

**Бэкапы** — архив наборов правил (вместе с ручными правками) и всех файлов хранилища в папке `backup` рядом с `docker-compose.yml`. По кнопке или по расписанию: раз в 12 часов, день, 3 дня или неделю. Старые автоматические удаляются сами, свежие можно отправлять в Telegram. Восстановление — выбрать архив в настройках, в том числе на новом сервере с другим доменом.

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

Понадобится отдельный поддомен, например `rules.example.com`. Дальше всё зависит от того, как запущен nginx панели. Посмотрите на её upstream в `nginx.conf`:

- `server remnawave:3000;` — nginx работает в сети Docker `remnawave-network` и видит контейнеры по именам. Вам подходит вариант «nginx в сети remnawave-network».
- `server 127.0.0.1:3000;` — nginx запущен с `network_mode: host`, имён контейнеров он не знает. Вам подходит вариант «nginx с network_mode: host». Если в этом случае указать `mrsmith:8080`, `nginx -t` ответит `host not found in upstream "mrsmith:8080"`.

#### nginx в сети remnawave-network

Compose панели Remnawave создаёт свою сеть `remnawave-network`. MRSmith подключается к ней вместо того, чтобы открывать порт, и nginx видит его по имени `mrsmith:8080`.

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

#### nginx с network_mode: host

Возьмите обычный compose-файл из шага 2, без `networks`: он публикует панель на `127.0.0.1:8090`, и nginx в сети хоста дойдёт до неё так же, как до самой панели по `127.0.0.1:3000`.

```bash
curl -o docker-compose.yml https://raw.githubusercontent.com/Rxxich/MRSmith/refs/heads/main/docker-compose.yml && curl -o .env https://raw.githubusercontent.com/Rxxich/MRSmith/refs/heads/main/.env.sample
```

В `nginx.conf` панели отличается только upstream:

```nginx
upstream mrsmith {
    server 127.0.0.1:8090;
}
```

Блок `server` для поддомена — тот же, что выше. Порт `8090` открыт только на `127.0.0.1`, снаружи он не виден. Если `8090` уже занят, поменяйте левую часть в `ports` compose-файла (например `127.0.0.1:8091:8080`) и upstream вместе с ней.

Частая ошибка при добавлении блока — лишняя закрывающая скобка `}` после `server`. `nginx -t` укажет на неё строкой `unexpected "}"`.

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

Панель записывает запросы к публичным ссылкам `/r/` и `/f/` и чем каждый закончился:
- скачал — панель отдала файл целиком;
- не изменился (проверка обновления) — клиент спросил, не обновился ли файл, и его копия оказалась актуальной, поэтому файл заново не передавался. Так mihomo сверяет свою копию по `interval` из `rule-providers`;
- не прошёл — загрузка оборвалась, в ссылке на приватный набор нет токена или он неверный, такого файла нет (набор не собран или формат не публикуется) или бакет не публичный. Клиент в этих случаях получает 404, а в журнале видна настоящая причина.

Счётчики скачиваний и проверок видны на странице набора, в списке наборов и в таблице бакета. В «Настройки» → «Кто скачивал» два вида: клиенты (IP и программа по User-Agent, какие файлы, сколько скачал, проверил и сколько запросов не прошло) и все запросы по одному. Журнал листается страницами, ищется по IP, программе или имени файла и фильтруется по результату. Клик по IP показывает все запросы с этого адреса. Хранятся последние 10 000 запросов. Запросы из самой панели и запросы `HEAD` не записываются. IP берётся из `X-Forwarded-For` только от доверенного reverse proxy (`MRSMITH_TRUSTED_PROXIES`). Если перед панелью стоит nginx, обрыв загрузки у клиента панель может не увидеть: nginx забирает файл целиком сам.

## Бэкапы

Раздел «Настройки» → «Бэкапы». В архив попадают наборы правил со всеми настройками и ручными правками, все бакеты и файлы хранилища (вместе со скачанными копиями источников). Логин, пароль, сессии и журнал «Кто скачивал» в архив не входят. Готовые файлы наборов тоже: после восстановления панель собирает их заново из копий источников, ничего не скачивая.

Архивы лежат в папке `backup` рядом с `docker-compose.yml`, например `/opt/mrsmith/backup/mrsmith-2026-11-23-0300-auto.zip`. Это обычный zip: внутри `manifest.json`, `rulesets.json`, `storage.json` и файлы.

- **Создать бэкап** — сразу, по кнопке.
- **Создавать автоматически** — раз в 12 часов, раз в день, раз в 3 дня или раз в неделю. Первый автоматический появляется в течение минуты после включения.
- **Хранить автоматических** — сколько последних автоматических архивов держать (по умолчанию 5). Более старые удаляются сами. Архивы, созданные кнопкой или положенные в папку вручную, не удаляются.
- **Отправлять в Telegram** — каждый новый архив приходит боту из `TELEGRAM_TOKEN` всем `ADMIN_ID`. Telegram принимает от ботов файлы до 50 МБ: если архив больше, придёт только сообщение. В архиве есть токены приватных наборов и ссылки источников, так что отправляйте его только в свой чат.

### Восстановление

Выберите архив в списке и нажмите «Восстановить». Наборы правил и хранилище заменяются содержимым архива, а прежние данные панель сначала сама сохраняет в архив `…-before-restore.zip` — к ним можно вернуться тем же способом.

На новом сервере:

1. Установите панель по шагам выше.
2. Положите архив в папку `backup` рядом с `docker-compose.yml` (например, `scp mrsmith-….zip root@новый-сервер:/opt/mrsmith/backup/`) — или загрузите его в панель кнопкой «Загрузить архив».
3. В «Настройки» → «Бэкапы» нажмите «Проверить папку», затем «Восстановить» у нужного архива.

Если у нового сервера другой домен, имена файлов в ссылках (`/r/<имя>.mrs`) и токены приватных наборов остаются прежними, меняется только адрес: `https://старый-домен/r/twitch.mrs` становится `https://новый-домен/r/twitch.mrs`. Панель покажет оба адреса, и в конфигах mihomo нужно заменить только домен. Источники наборов, которые ссылались на файлы самой панели по старому адресу, панель перепишет на новый сама. Новый адрес берётся из `MRSMITH_PUBLIC_URL`, а если он не задан — из адреса, по которому открыта панель.

### Если вы ставили панель до версии 1.0.19

Папку `backup` добавляет новый `docker-compose.yml`. Скачайте его заново (шаг 2 установки, ваш `.env` не трогайте) и выполните:

```bash
cd /opt/mrsmith && docker compose pull && docker compose up -d
```

Если правили compose-файл под себя, добавьте в сервис `mrsmith` строки ниже, а сервис `backup-folder` скопируйте из [`docker-compose.yml`](docker-compose.yml) целиком. Он запускается один раз перед панелью и отдаёт ей права на папку: сама панель работает от непривилегированного пользователя `65532` и создать в ней файлы иначе не сможет.

```yaml
    volumes:
      - mrsmith-data:/data
      - ./backup:/backup
    depends_on:
      backup-folder:
        condition: service_completed_successfully
```

Если в настройках написано, что панели не хватает прав, выполните в папке с `docker-compose.yml`: `chown -R 65532:65532 backup`.

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

Бэкапы наборов и хранилища делаются в самой панели — раздел «Бэкапы» выше. Полная копия тома `mrsmith-data` вместе с логином, сессиями и журналом скачиваний:

```bash
docker run --rm -v mrsmith-data:/data -v "$PWD":/out alpine tar czf /out/mrsmith-data.tgz -C /data .
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
  cmd/mrsmith/         точка входа: serve, healthcheck, reset-password, prepare-backup-dir
  internal/rules/      разбор форматов, дедупликация, MRS-кодек
  internal/rulesets/   наборы: источники, сборка, расписание, история
  internal/storage/    S3-подобное хранилище
  internal/backup/     архивы наборов и хранилища, расписание, восстановление
  internal/auth/       пароли, сессии, ограничение попыток входа
  internal/fetch/      загрузчик с защитой от SSRF
  internal/server/     HTTP API, публичные ссылки, раздача интерфейса
frontend/              React + Vite, собирается внутрь backend/internal/webui/dist
docker-compose.yml     установка из готового образа, .env.sample — шаблон настроек
examples/              nginx, Caddy и установка рядом с Remnawave
```

Образ `ghcr.io/rxxich/mrsmith` собирает workflow [`.github/workflows/docker.yml`](.github/workflows/docker.yml) для amd64 и arm64: при пуше тега `v*` (теги `1.0.13`, `1.0`, `latest`) или вручную из вкладки Actions. Собрать образ локально: `docker build -t mrsmith:local .`
