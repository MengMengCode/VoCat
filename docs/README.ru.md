<p align="center">
  <img src="../web/public/favicon.svg" width="96" alt="Vocat">
</p>

<h1 align="center">VoCat</h1>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go&logoColor=white">
  <img alt="React" src="https://img.shields.io/badge/React-19-61DAFB?style=flat-square&logo=react&logoColor=111111">
  <img alt="TypeScript" src="https://img.shields.io/badge/TypeScript-5.8-3178C6?style=flat-square&logo=typescript&logoColor=white">
  <img alt="Vite" src="https://img.shields.io/badge/Vite-7-646CFF?style=flat-square&logo=vite&logoColor=white">
  <img alt="Tailwind CSS" src="https://img.shields.io/badge/Tailwind_CSS-3-06B6D4?style=flat-square&logo=tailwindcss&logoColor=white">
  <img alt="SQLite" src="https://img.shields.io/badge/SQLite-Embedded-003B57?style=flat-square&logo=sqlite&logoColor=white">
</p>

<p align="center">
  <img alt="Linux" src="https://img.shields.io/badge/Linux-amd64_%7C_386_%7C_arm64_%7C_aarch64_%7C_armv7-FCC624?style=flat-square&logo=linux&logoColor=111111">
  <img alt="Docker" src="https://img.shields.io/badge/Docker-Multi--Arch-2496ED?style=flat-square&logo=docker&logoColor=white">
  <img alt="WiFi Calling" src="https://img.shields.io/badge/WiFi_Calling-IMS_SMS-7B1FA2?style=flat-square">
  <img alt="eSIM" src="https://img.shields.io/badge/eSIM-LPA_%2F_eUICC-009688?style=flat-square">
  <img alt="Telegram" src="https://img.shields.io/badge/Telegram-Bot-26A5E4?style=flat-square&logo=telegram&logoColor=white">
  <img alt="GitHub Actions" src="https://img.shields.io/badge/GitHub_Actions-Release-2088FF?style=flat-square&logo=githubactions&logoColor=white">
</p>

[English](../README.md) | [العربية](README.ar.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [Français](README.fr.md) | **Русский** | [Español](README.es.md) | [日本語](README.ja.md)

Vocat — это веб-панель управления с открытым исходным кодом и набор инженерных инструментов для сотовых модемов Quectel класса EC20/EC25. Она объединяет в одном автономном сервисе обнаружение модемов, состояние радиосвязи в реальном времени, терминалы AT и USSD, SMS, WiFi Calling, управление eSIM, выбор сети, маршрутизацию через прокси, уведомления, журналы аудита и автоматизацию релизов.

Бэкенд написан на Go, интерфейс построен на React и TypeScript, а производственный фронтенд встроен в бинарный файл Go. Один исполняемый файл содержит веб-приложение и использует SQLite для постоянного хранения состояния.

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## Возможности

| Область | Что предоставляет Vocat |
| --- | --- |
| Управление устройствами | Автоматическое обнаружение по последовательному порту/USB, поддержка нескольких модемов, понятные имена устройств, обновление обзора в реальном времени, перезапуск модуля, авиарежим и управление режимом USB-сети. |
| Радио и сеть | Статус регистрации, оператор, метрики сигнала, RSRP/RSRQ/SINR, режим сети, диапазон, канал, сканирование операторов и автоматический или ручной выбор сети. |
| AT и USSD | Интерактивный AT-терминал, история команд, необработанные ответы модема, потоки запуска/продолжения/отмены USSD и понятные сообщения об ошибках модема. |
| SMS | Прямая отправка сотовых и IMS SMS, входящая синхронизация, обработка составных сообщений, отчёты о доставке, история диалогов, статус непрочитанных, метки времени и статус доставки каждого сообщения. |
| WiFi Calling | Установка туннеля IKEv2/ePDG, аутентификация EAP-AKA, регистрация IMS, IMS SMS, управление переподключением, диагностика состояния и маршрутизация по устройствам. |
| eSIM и eUICC | Обнаружение eUICC, EID и производственная информация, метаданные сертификатов, инвентарь нескольких eUICC, список установленных профилей, операции включения/отключения/переключения, а также загрузка, переименование и удаление при поддержке картой. |
| Политика карты | Поведение WiFi Calling и авиарежима на основе ICCID с немедленным применением политики. |
| Маршрутизация через прокси | Восходящая маршрутизация SOCKS, привязки устройств, правила по странам, проверки доступности TCP и проверки UDP Associate для путей передачи данных WiFi Calling. |
| Уведомления | Пересылка новых входящих SMS через Telegram, Bark, электронную почту, Pushplus и подписанные вебхуки. Каждое SMS доставляется как отдельное уведомление. |
| Telegram-бот | Статус устройства, список и переключение установленных профилей, управление WiFi Calling и отправка SMS. Чувствительные действия требуют подтверждения администратора. |
| Эксплуатация | Аутентификация, защита CSRF, политики доступа, события аудита, журналы в реальном времени, хранение журналов, проверки работоспособности, адаптивная вёрстка, тёмный режим и интерфейс на английском/китайском. |
| Дистрибуция | Статические бинарные файлы Linux, скрипт установки systemd, самообновление с проверкой SHA-256, образ Docker, публикация в GHCR и сборки релизов GitHub Actions. |

## Поддерживаемое оборудование

Vocat ориентирован на модули Quectel на базе Qualcomm, которые предоставляют совместимые интерфейсы AT, QMI, последовательный порт и USB-сеть, включая:

- Quectel EC20
- Quectel EC25
- Семейство Quectel EG25
- Совместимые модули EG600 и родственные

Доступные функции зависят от прошивки модуля, конфигурации USB, возможностей SIM/eSIM, драйверов хоста, радиосети и настроек оператора.

## Установка

### Установка в Linux одной командой

От имени root (включая OpenWrt/Kwrt, где `sudo` обычно отсутствует):

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash
```

От обычного пользователя в дистрибутиве с sudo:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

Проверить предварительные требования VoWiFi/XFRM на хосте без установки VoCat:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash -s -- --check-env
```

Установить конкретную версию:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

VoWiFi IMS требует Linux XFRM/IPsec. В OpenWrt/Kwrt установщик пытается
установить соответствующие пакеты `ip-full`, `kmod-ipsec`, `kmod-ipsec4/6`,
`kmod-crypto-authenc`, AES-CBC и SHA1 из собственного репозитория прошивки.
Если соответствующие модули ядра недоступны, используйте прошивку, которая их включает;
никогда не устанавливайте принудительно kmod, собранные для другого ядра.

Если ваше ядро не поддерживает XFRM/IPsec и вам нужны только функции без VoWiFi, например сотовые SMS или передача данных, выполните установку с `--skip-vowifi-check`:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh --skip-vowifi-check
```

Установщик:

- определяет `amd64`, `386`, `arm64`, `aarch64` или `armv7`;
- загружает соответствующий бинарный файл GitHub Release;
- проверяет его по `SHA256SUMS`;
- устанавливает Vocat в `/opt/vocat`;
- создаёт усиленный сервис systemd с доступом к оборудованию и сети, необходимым Vocat;
- хранит конфигурацию времени выполнения в `/etc/vocat/env`;
- генерирует случайный начальный пароль администратора при первой установке.

После установки откройте:

```text
http://<server-address>:7575
```

### Ручная установка бинарного файла

Загрузите соответствующий бинарный файл и `SHA256SUMS` из GitHub Releases:

| Платформа | Файл релиза |
| --- | --- |
| Linux x86-64 | `vocat-linux-amd64` |
| Linux x86 32-бит | `vocat-linux-386` |
| Linux ARM64 | `vocat-linux-arm64` |
| Linux AArch64 | `vocat-linux-aarch64` |
| Linux ARMv7 | `vocat-linux-armv7` |

Проверьте и установите его:

```bash
sha256sum -c SHA256SUMS --ignore-missing
sudo install -d -m 0755 /opt/vocat/bin /opt/vocat/data
sudo install -m 0755 vocat-linux-amd64 /opt/vocat/bin/vocat
read -rsp "Admin password: " VOCAT_BOOTSTRAP_PASSWORD; echo
printf '%s\n' "$VOCAT_BOOTSTRAP_PASSWORD" | sudo /opt/vocat/bin/vocat bootstrap-admin
unset VOCAT_BOOTSTRAP_PASSWORD
sudo env \
  VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db \
  /opt/vocat/bin/vocat serve
```

Эта ручная команда запускает Vocat в переднем плане. Используйте `vocat serve`, чтобы
процесс сразу запустил сервер; запуск `vocat` без аргументов от имени root
в TTY вместо этого открывает интерактивное меню управления. Используйте установку
одной командой, когда требуется управляемый сервис systemd и автоматический перезапуск.

### Docker

Для хоста Linux, который должен обнаруживать каждый подключённый поддерживаемый модем Quectel и
продолжать видеть события горячего подключения USB, запустите Vocat в режиме доступа к оборудованию:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest

read -rsp "Admin password: " VOCAT_BOOTSTRAP_PASSWORD; echo
printf '%s\n' "$VOCAT_BOOTSTRAP_PASSWORD" | docker run --rm -i \
  --user 0:0 \
  -v vocat-data:/opt/vocat/data \
  --entrypoint /opt/vocat/bin/vocat \
  ghcr.io/mengmengcode/vocat:latest bootstrap-admin
unset VOCAT_BOOTSTRAP_PASSWORD

docker run -d \
  --name vocat \
  --restart unless-stopped \
  --network host \
  --privileged \
  --user 0:0 \
  -v vocat-data:/opt/vocat/data \
  -v /dev:/dev \
  -v /sys:/sys:ro \
  ghcr.io/mengmengcode/vocat:latest
```

После запуска контейнера откройте `http://<server-address>:7575`. Сеть хоста необходима, чтобы сетевые интерфейсы QMI оставались видимыми для Vocat, а привилегированный доступ к устройствам — для последовательных портов, узлов управления QMI, интерфейсов TUN, настройки сети и устройств, добавленных после запуска контейнера. Привязка `/dev` делает новые узлы `ttyUSB*`, `ttyACM*`, `cdc-wdm*` и MHI `wwan*` видимыми без пересоздания контейнера.

Этот режим намеренно предоставляет Vocat широкий доступ к устройствам и сетевому стеку хоста. Используйте его только на доверенном хосте Linux. Автоматическое обнаружение распознаёт поддерживаемые USB-модемы Quectel (идентификатор производителя USB `2c7c`) и модемы PCIe/MHI, представленные через подсистему Linux WWAN; произвольные конфигурации модемов не распознаются. Проброс только отдельных узлов с помощью `--device`, например `/dev/ttyUSB2`, `/dev/cdc-wdm0` или `/dev/wwan0qmi0`, ограничивает контейнер этими фиксированными узлами и не обеспечивает полноценного обнаружения нескольких устройств или горячего подключения.

Образ GHCR публикуется для `linux/amd64` и `linux/arm64`.

> [!TIP]
> **Примечание по развёртыванию на NAS / QNAP Container Station**:
> В операционных системах NAS, таких как QNAP QTS / QuTS hero (Container Station), пользовательские учётные записи администратора без прав root и механизмы изоляции томов могут приводить к тому, что именованные тома Docker (например, `-v vocat-data:/opt/vocat/data`) указывают на разные изолированные пути при однократной инициализации `bootstrap-admin` и в контейнере фоновой службы, вызывая ошибки «Неверный пароль» при входе через веб-интерфейс.
> В средах NAS настоятельно рекомендуется заменить именованные тома привязкой абсолютного пути хоста (например, `-v /share/Container/vocat/data:/opt/vocat/data` на QNAP) как при инициализации, так и при работе службы, чтобы обеспечить согласованное постоянное хранение базы данных SQLite.

### USB-считыватели SIM-карт

USB-считыватели SIM-карт используют службу Linux PC/SC. При наличии поддерживаемого менеджера пакетов установщик одной командой автоматически устанавливает и запускает `pcscd`, а также устанавливает драйвер CCID. В Debian/Ubuntu эквивалентная команда ручной установки — `apt install pcscd libccid`. Если USB обнаруживает считыватель CCID, но PC/SC недоступна, VoCat оставляет считыватель видимым в диалоге добавления устройства и сообщает об отсутствующей службе или драйвере, а не скрывает его без объяснения.

### Утилиты командной строки QMI

VoCat использует `qmicli` для проверки готовности канала управления QMI и `qmi-proxy` для мультиплексирования доступа к нему. Сеансами пакетной передачи данных управляет встроенный клиент QMI WDS, а не файлы состояния CID/PDH утилиты `qmi-network`. Установщик одной командой устанавливает и проверяет соответствующие утилиты. При ручном развёртывании в Debian/Ubuntu используется `apt install libqmi-utils`, в Arch Linux — `pacman -S libqmi`, в Alpine — `apk add qmi-utils`, а в OpenWrt — `opkg install qmi-utils`.

`vocat doctor --repair-dji-qmi` проверяет наличие `qmicli` перед изменением любой привязки USB-драйвера или активацией сигнала DTR. Если утилита недоступна, команда останавливается с подсказкой по установке и оставляет текущее состояние устройства без изменений.

## Конфигурация

Vocat читает необязательный JSON-файл конфигурации из `VOCAT_CONFIG`, затем применяет переменные окружения `VOCAT_*`. Переменные окружения имеют приоритет.

| Переменная окружения | По умолчанию | Описание |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | Адрес прослушивания HTTP. |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | Путь к базе данных SQLite. |
| `VOCAT_SESSION_TTL` | `24h` | Время жизни сессии аутентификации. |
| `VOCAT_SECURE_COOKIES` | `false` | Помечает cookie сессии как безопасные при использовании HTTPS. |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | Тайм-аут корректного завершения работы. |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | Максимальный размер тела запроса API. |
| `VOCAT_REPO` | `MengMengCode/VoCat` | Доверенный репозиторий GitHub, используемый самообновлятором, в формате `owner/name`. |
| `GITHUB_TOKEN` | пусто | Необязательный токен GitHub для приватных репозиториев или более высоких лимитов API. |

Предоставленные пользователем пакеты настроек операторов Apple можно преобразовать командой `vocat carrier import-ipcc` в профили операторов, доступные для проверки и ограниченные списком разрешённых элементов; см. [docs/CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md).

Учётные данные администратора хранятся только в SQLite. Один раз инициализируйте пустую базу данных командой `vocat bootstrap-admin`; переменные окружения и конфигурация JSON не могут задать или перезаписать имя пользователя либо пароль администратора.

Не храните токены Telegram, пароли SMTP, секреты вебхуков, учётные данные SIM или другие приватные данные в репозитории. Настраивайте их через параметры приложения или защищённые файлы окружения.

## Telegram-бот

Когда уведомления Telegram включены и настроены Chat ID и Admin ID, бот поддерживает:

```text
/status [device]
/esim <device>
/switch <device> <iccid>
/wfc <device> <status|on|off|reconnect>
/sms <device> <number> <message>
```

Переключение профилей и отправка SMS используют одноразовые кнопки подтверждения. Бот не предоставляет команды загрузки, удаления или переименования eSIM.

## Обновление

Проверить наличие более нового GitHub Release:

```bash
vocat update --check --repo MengMengCode/VoCat
```

Установить последний релиз:

```bash
sudo vocat update --repo MengMengCode/VoCat
```

Обновлятор загружает бинарный файл, соответствующий текущей архитектуре Linux, проверяет его по опубликованному `SHA256SUMS`, атомарно заменяет исполняемый файл и перезапускает сервис systemd `vocat`, когда он доступен.

Для установок Docker:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

Пересоздайте контейнер после загрузки нового образа.

## Разработка

Требования:

- Go 1.25 или новее
- Node.js 20 или новее
- npm

Запустить сервер разработки фронтенда:

```bash
cd web
npm install
npm run dev
```

Собрать встроенный фронтенд и запустить бэкенд:

```bash
cd web
npm run build
cd ..
go run ./cmd/vocat
```

Запустить все тесты:

```bash
go test ./...
```

Собрать производственный бинарный файл:

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## Автоматизация релизов

Отправка тега версии запускает два рабочих процесса GitHub Actions:

- `release-binaries` собирает и публикует бинарные файлы `amd64`, `386`, `arm64`, `aarch64` и `armv7` вместе с `SHA256SUMS`.
- `docker` собирает и публикует мультиархитектурный образ в GitHub Container Registry.

```bash
git tag v0.2.0
git push origin v0.2.0
```

## Структура проекта

```text
cmd/vocat/                  Точка входа приложения и CLI
internal/device/            Обнаружение модемов и управление устройствами
internal/modem/             Сессия AT и обработка ответов
internal/server/            HTTP API, уведомления и встроенный веб-сервер
internal/store/             Постоянное хранение SQLite
internal/update/            Самообновлятор GitHub Release
internal/vowifi/            Среда выполнения IKE, EAP-AKA, IMS и WiFi Calling
scripts/install.sh          Установщик и обновлятор Linux
web/src/                    Фронтенд на React и TypeScript
.github/workflows/          Автоматизация релизов бинарных файлов и Docker
```

## Ответственное использование

Операции с сотовыми модемами и eSIM могут влиять на обслуживание абонента, сохранённые профили, регистрацию в сети и состояние оборудования. Делайте резервные копии, внимательно проверяйте деструктивные действия и используйте программное обеспечение только в законных средах, где вам разрешено работать с подключённым оборудованием и сетевыми ресурсами.

Vocat не обходит аутентификацию оператора, сетевую политику, аппаратную безопасность или требования доверия eSIM. Поддержка операции означает, что Vocat может запросить её у модема или eUICC; устройство, профиль, сеть или оператор всё равно могут её отклонить.

## Участие в разработке

Мы приветствуем issues и pull request'ы. Делайте изменения сфокусированными, по возможности добавляйте тесты, избегайте коммита учётных данных или данных абонентов и чётко документируйте поведение, специфичное для оборудования.

Перед отправкой изменения:

```bash
go test ./...
cd web && npm run build
```

## Благодарности
- [Nodeseek.com](https://www.nodeseek.com) — Сообщество, посвящённое серверам
- [Linux.do](https://linux.do) — Вдохновляющее технологическое сообщество
- [iniwex5](https://github.com/iniwex5) — Руководства по стилю и функциональности

## Угостите меня кофе

| Сеть | Адрес |
| ------- | ------- |
| USDT-TRON (TRC20) | `TWSAkvzVsFc7KqncDLmUfRxpPQbpV5CgTB` |
| USDT-BSC (BEP20) | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |
| USDT-Polygon | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |

## Лицензия

См. [LICENSE](../LICENSE).

<a href="https://star-history.dera.page/#MengMengCode/VoCat">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://star-history.dera.page/svg?repos=MengMengCode/VoCat&theme=dark" />
   <source media="(prefers-color-scheme: light)" srcset="https://star-history.dera.page/svg?repos=MengMengCode/VoCat" />
   <img alt="Star History Chart" src="https://star-history.dera.page/svg?repos=MengMengCode/VoCat" />
 </picture>
</a>
