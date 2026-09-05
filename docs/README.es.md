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

[English](../README.md) | [العربية](README.ar.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [Français](README.fr.md) | [Русский](README.ru.md) | **Español** | [日本語](README.ja.md)

Vocat es un panel de control web de código abierto y un conjunto de herramientas de ingeniería para módems celulares Quectel de clase EC20/EC25. Combina, en un único servicio autocontenido, el descubrimiento de módems, el estado de radio en vivo, terminales AT y USSD, SMS, WiFi Calling, gestión de eSIM, selección de red, enrutamiento por proxy, notificaciones, registros de auditoría y automatización de versiones.

El backend está escrito en Go, la interfaz está construida con React y TypeScript, y el frontend de producción está incrustado en el binario de Go. Un único ejecutable contiene la aplicación web y utiliza SQLite para el estado persistente.

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## Funcionalidades

| Área | Lo que proporciona Vocat |
| --- | --- |
| Gestión de dispositivos | Descubrimiento serie/USB automático, soporte para múltiples módems, nombres de dispositivo amigables, actualizaciones en vivo de la vista general, reinicio del módulo, modo avión y controles del modo de red USB. |
| Radio y red | Estado de registro, operador, métricas de señal, RSRP/RSRQ/SINR, modo de red, banda, canal, búsqueda de operadores y selección de red automática o manual. |
| AT y USSD | Terminal AT interactivo, historial de comandos, respuestas sin procesar del módem, flujos de inicio/continuación/cancelación de USSD y reporte claro de errores del módem. |
| SMS | Envío directo de SMS celulares e IMS, sincronización entrante, manejo multiparte, informes de entrega, historial de conversaciones, estado de no leído, marcas de tiempo y estado de entrega por mensaje. |
| WiFi Calling | Establecimiento de túnel IKEv2/ePDG, autenticación EAP-AKA, registro IMS, SMS IMS, controles de reconexión, diagnósticos de estado y enrutamiento por dispositivo. |
| eSIM y eUICC | Descubrimiento de eUICC, EID e información de producción, metadatos de certificados, inventario multi-eUICC, lista de perfiles instalados, operaciones de habilitar/deshabilitar/cambiar, y operaciones de descarga, renombrado y eliminación cuando la tarjeta lo admite. |
| Política de tarjeta | Comportamiento de WiFi Calling y modo avión basado en ICCID con aplicación inmediata de la política. |
| Enrutamiento por proxy | Enrutamiento SOCKS ascendente, vinculaciones de dispositivos, reglas por país, comprobaciones de accesibilidad TCP y comprobaciones UDP Associate para las rutas de datos de WiFi Calling. |
| Notificaciones | Reenvío de nuevos SMS entrantes a través de Telegram, Bark, correo electrónico, Pushplus y webhooks firmados. Cada SMS se entrega como una notificación individual. |
| Bot de Telegram | Estado del dispositivo, lista y cambio de perfiles instalados, controles de WiFi Calling y envío de SMS. Las acciones sensibles requieren confirmación del administrador. |
| Operaciones | Autenticación, protección CSRF, políticas de acceso, eventos de auditoría, registros en vivo, retención de registros, comprobaciones de salud, diseño adaptable, modo oscuro e interfaz de usuario en inglés/chino. |
| Distribución | Binarios estáticos de Linux, script de instalación systemd, autoactualización con verificación SHA-256, imagen Docker, publicación en GHCR y compilaciones de versión de GitHub Actions. |

## Hardware compatible

Vocat está dirigido a módulos Quectel basados en Qualcomm que exponen interfaces AT, QMI, serie y de red USB compatibles, incluyendo:

- Quectel EC20
- Quectel EC25
- Familia Quectel EG25
- Módulos EG600 compatibles y relacionados

Las funciones disponibles dependen del firmware del módulo, la composición USB, las capacidades SIM/eSIM, los controladores del host, la red de radio y la configuración del operador.

## Instalación

### Instalación en Linux con un clic

Como root (incluyendo OpenWrt/Kwrt, donde `sudo` normalmente está ausente):

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash
```

Desde un usuario normal en una distribución con sudo:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

Comprobar los prerrequisitos de VoWiFi/XFRM del host sin instalar VoCat:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash -s -- --check-env
```

Instalar una versión específica:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

VoWiFi IMS requiere Linux XFRM/IPsec. En OpenWrt/Kwrt el instalador intenta
instalar los paquetes coincidentes `ip-full`, `kmod-ipsec`, `kmod-ipsec4/6`,
`kmod-crypto-authenc`, AES-CBC y SHA1 desde el propio feed del firmware.
Si no hay módulos de kernel coincidentes disponibles, use un firmware que los incluya;
nunca fuerce la instalación de kmods compilados para un kernel diferente.

Si su kernel no puede proporcionar XFRM/IPsec y solo necesita funciones que no utilicen VoWiFi, como SMS celulares o datos, instale con `--skip-vowifi-check`:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh --skip-vowifi-check
```

El instalador:

- detecta `amd64`, `386`, `arm64`, `aarch64` o `armv7`;
- descarga el binario de GitHub Release correspondiente;
- lo verifica contra `SHA256SUMS`;
- instala Vocat en `/opt/vocat`;
- crea un servicio systemd reforzado con el acceso a hardware y red requerido por Vocat;
- almacena la configuración en tiempo de ejecución en `/etc/vocat/env`;
- genera una contraseña de administrador inicial aleatoria en la primera instalación.

Después de la instalación, abra:

```text
http://<server-address>:7575
```

### Instalación manual del binario

Descargue el binario correspondiente y `SHA256SUMS` desde GitHub Releases:

| Plataforma | Archivo de versión |
| --- | --- |
| Linux x86-64 | `vocat-linux-amd64` |
| Linux x86 32 bits | `vocat-linux-386` |
| Linux ARM64 | `vocat-linux-arm64` |
| Linux AArch64 | `vocat-linux-aarch64` |
| Linux ARMv7 | `vocat-linux-armv7` |

Verifíquelo e instálelo:

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

Este comando manual ejecuta Vocat en primer plano. Use `vocat serve` para que el
proceso inicie el servidor directamente; ejecutar `vocat` sin argumentos como root
en un TTY abre en su lugar el menú de gestión interactivo. Use el instalador de
un clic cuando se requiera un servicio systemd gestionado y reinicio automático.

### Docker

Para un host Linux que debe descubrir cada módem Quectel compatible conectado y
seguir viendo los eventos de conexión en caliente USB, ejecute Vocat en modo de acceso a hardware:

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

Abra `http://<server-address>:7575` después de que se inicie el contenedor. La red del host es necesaria para que las interfaces de red QMI permanezcan visibles para Vocat, mientras que el acceso privilegiado a dispositivos es necesario para los puertos serie, los nodos de control QMI, las interfaces TUN, la configuración de red y los dispositivos añadidos después del inicio del contenedor. El montaje bind de `/dev` hace visibles los nuevos nodos `ttyUSB*`, `ttyACM*`, `cdc-wdm*` y MHI `wwan*` sin recrear el contenedor.

Este modo otorga intencionadamente a Vocat un amplio acceso a los dispositivos y a la pila de red del host. Úselo solo en un host Linux de confianza. El descubrimiento automático identifica los módems USB Quectel compatibles (ID de fabricante USB `2c7c`) y los módems PCIe/MHI expuestos a través del subsistema Linux WWAN; no identifica disposiciones arbitrarias de módems. Mapear solo nodos individuales con `--device`, como `/dev/ttyUSB2`, `/dev/cdc-wdm0` o `/dev/wwan0qmi0`, limita el contenedor a esos nodos fijos y no permite un descubrimiento completo de múltiples dispositivos o de conexiones en caliente.

La imagen GHCR se publica para `linux/amd64` y `linux/arm64`.

> [!TIP]
> **Nota sobre el despliegue en NAS / QNAP Container Station**:
> En sistemas operativos NAS como QNAP QTS / QuTS hero (Container Station), las cuentas de administrador personalizadas sin privilegios root y los mecanismos de aislamiento de volúmenes pueden hacer que los volúmenes con nombre de Docker (por ejemplo, `-v vocat-data:/opt/vocat/data`) se resuelvan en rutas aisladas distintas entre la inicialización única `bootstrap-admin` y el contenedor del servicio en segundo plano, provocando errores de «Contraseña incorrecta» al iniciar sesión en la web.
> En entornos NAS, se recomienda encarecidamente sustituir los volúmenes con nombre por un montaje bind con una ruta absoluta del host (por ejemplo, `-v /share/Container/vocat/data:/opt/vocat/data` en QNAP), tanto para la inicialización como para la ejecución, para garantizar una persistencia coherente de la base de datos SQLite.

### Lectores USB de tarjetas SIM

Los lectores USB de tarjetas SIM utilizan el servicio PC/SC de Linux. Con los gestores de paquetes compatibles, el instalador de un clic instala e inicia automáticamente `pcscd` e instala el controlador CCID. En Debian/Ubuntu, la configuración manual equivalente es `apt install pcscd libccid`. Si USB detecta un lector CCID pero PC/SC no está disponible, VoCat mantiene el lector visible en el diálogo para añadir dispositivos e informa del servicio o controlador que falta en lugar de ocultarlo silenciosamente.

### Utilidades QMI de línea de comandos

VoCat utiliza `qmicli` para verificar que un canal de control QMI esté listo y `qmi-proxy` para multiplexar el acceso a él. Las sesiones de datos por paquetes se gestionan mediante el cliente QMI WDS integrado, en lugar de los archivos de estado CID/PDH de `qmi-network`. El instalador de un clic instala y verifica las utilidades correspondientes. Para el despliegue manual, Debian/Ubuntu utiliza `apt install libqmi-utils`; Arch Linux utiliza `pacman -S libqmi`, Alpine utiliza `apk add qmi-utils` y OpenWrt utiliza `opkg install qmi-utils`.

`vocat doctor --repair-dji-qmi` comprueba la presencia de `qmicli` antes de cambiar cualquier asociación de controladores USB o activar la señal DTR. Si la utilidad no está disponible, el comando se detiene con una indicación para instalarla y deja intacto el estado actual del dispositivo.

## Configuración

Vocat lee un archivo de configuración JSON opcional desde `VOCAT_CONFIG` y luego aplica las variables de entorno `VOCAT_*`. Las variables de entorno tienen prioridad.

| Variable de entorno | Predeterminado | Descripción |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | Dirección de escucha HTTP. |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | Ruta de la base de datos SQLite. |
| `VOCAT_SESSION_TTL` | `24h` | Duración de la sesión de autenticación. |
| `VOCAT_SECURE_COOKIES` | `false` | Marca las cookies de sesión como seguras cuando se usa HTTPS. |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | Tiempo de espera de apagado ordenado. |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | Tamaño máximo del cuerpo de solicitud de la API. |
| `VOCAT_REPO` | `MengMengCode/VoCat` | Repositorio de GitHub de confianza usado por el autoactualizador, en formato `owner/name`. |
| `GITHUB_TOKEN` | vacío | Token de GitHub opcional para repositorios privados o límites de API más altos. |

Los paquetes de operador de Apple proporcionados por el usuario pueden convertirse con `vocat carrier import-ipcc` en perfiles de operador revisables y limitados a una lista de elementos permitidos; consulte [docs/CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md).

Las credenciales del administrador se almacenan únicamente en SQLite. Inicialice una base de datos vacía una sola vez con `vocat bootstrap-admin`; las variables de entorno y la configuración JSON no pueden establecer ni sobrescribir el nombre de usuario o la contraseña del administrador.

No almacene tokens de Telegram, contraseñas SMTP, secretos de webhook, credenciales SIM u otros datos privados en el repositorio. Configúrelos a través de los ajustes de la aplicación o archivos de entorno protegidos.

## Bot de Telegram

Cuando las notificaciones de Telegram están habilitadas y tanto el Chat ID como el Admin ID están configurados, el bot admite:

```text
/status [device]
/esim <device>
/switch <device> <iccid>
/wfc <device> <status|on|off|reconnect>
/sms <device> <number> <message>
```

El cambio de perfil y el envío de SMS usan botones de confirmación de un solo uso. El bot no expone comandos de descarga, eliminación o renombrado de eSIM.

## Actualización

Comprobar si hay una GitHub Release más reciente:

```bash
vocat update --check --repo MengMengCode/VoCat
```

Instalar la última versión:

```bash
sudo vocat update --repo MengMengCode/VoCat
```

El actualizador descarga el binario que coincide con la arquitectura Linux actual, lo verifica con el `SHA256SUMS` publicado, reemplaza el ejecutable de forma atómica y reinicia el servicio systemd `vocat` cuando está disponible.

Para instalaciones Docker:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

Recrear el contenedor después de descargar la nueva imagen.

## Desarrollo

Requisitos:

- Go 1.25 o más reciente
- Node.js 20 o más reciente
- npm

Ejecutar el servidor de desarrollo del frontend:

```bash
cd web
npm install
npm run dev
```

Compilar el frontend incrustado e iniciar el backend:

```bash
cd web
npm run build
cd ..
go run ./cmd/vocat
```

Ejecutar todas las pruebas:

```bash
go test ./...
```

Compilar un binario de producción:

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## Automatización de versiones

Hacer push de una etiqueta de versión inicia dos flujos de trabajo de GitHub Actions:

- `release-binaries` compila y publica los binarios `amd64`, `386`, `arm64`, `aarch64` y `armv7` más `SHA256SUMS`.
- `docker` compila y publica una imagen multiarquitectura en GitHub Container Registry.

```bash
git tag v0.2.0
git push origin v0.2.0
```

## Estructura del proyecto

```text
cmd/vocat/                  Punto de entrada de la aplicación y CLI
internal/device/            Descubrimiento de módems y control de dispositivos
internal/modem/             Sesión AT y manejo de respuestas
internal/server/            API HTTP, notificaciones y servidor web incrustado
internal/store/             Persistencia SQLite
internal/update/            Autoactualizador de GitHub Release
internal/vowifi/            Runtime de IKE, EAP-AKA, IMS y WiFi Calling
scripts/install.sh          Instalador y actualizador de Linux
web/src/                    Frontend en React y TypeScript
.github/workflows/          Automatización de versiones de binarios y Docker
```

## Uso responsable

Las operaciones con módems celulares y eSIM pueden afectar el servicio del abonado, los perfiles almacenados, el registro de red y el estado del hardware. Mantenga copias de seguridad, revise con cuidado las acciones destructivas y use el software solo en entornos legales donde tenga permiso para operar el hardware y los recursos de red conectados.

Vocat no elude la autenticación del operador, la política de red, la seguridad del hardware ni los requisitos de confianza de eSIM. El soporte de una operación significa que Vocat puede solicitarla al módem o al eUICC; el dispositivo, el perfil, la red o el operador aún pueden rechazarla.

## Contribuir

Las incidencias y pull requests son bienvenidas. Mantenga los cambios enfocados, incluya pruebas cuando sea práctico, evite confirmar credenciales o datos de abonados, y documente claramente el comportamiento específico del hardware.

Antes de enviar un cambio:

```bash
go test ./...
cd web && npm run build
```

## Agradecimientos
- [Nodeseek.com](https://www.nodeseek.com) — Una comunidad dedicada a los servidores
- [Linux.do](https://linux.do) — Una comunidad tecnológica inspiradora
- [iniwex5](https://github.com/iniwex5) — Guías de estilo y funcionalidad

## Invítame a un café

| Red | Dirección |
| ------- | ------- |
| USDT-TRON (TRC20) | `TWSAkvzVsFc7KqncDLmUfRxpPQbpV5CgTB` |
| USDT-BSC (BEP20) | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |
| USDT-Polygon | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |

## Licencia

Consulte [LICENSE](../LICENSE).

<a href="https://star-history.dera.page/#MengMengCode/VoCat">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://star-history.dera.page/svg?repos=MengMengCode/VoCat&theme=dark" />
   <source media="(prefers-color-scheme: light)" srcset="https://star-history.dera.page/svg?repos=MengMengCode/VoCat" />
   <img alt="Star History Chart" src="https://star-history.dera.page/svg?repos=MengMengCode/VoCat" />
 </picture>
</a>
