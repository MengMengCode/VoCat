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

Vocat es un panel de control web de código abierto y un conjunto de herramientas de ingeniería para módems celulares Quectel de clase EC20/EC25 y China Mobile de serie ML307. Combina, en un único servicio autocontenido, el descubrimiento de módems, el estado de radio en vivo, terminales AT y USSD, SMS, WiFi Calling, gestión de eSIM, selección de red, enrutamiento por proxy, notificaciones, registros de auditoría y automatización de versiones.

El backend está escrito en Go, la interfaz está construida con React y TypeScript, y el frontend de producción está incrustado en el binario de Go. Un único ejecutable contiene la aplicación web y utiliza SQLite para el estado persistente.

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## Funcionalidades

| Área | Lo que proporciona Vocat |
| --- | --- |
| Gestión de dispositivos | Descubrimiento serie/USB automático, soporte para múltiples módems, nombres de dispositivo amigables, actualizaciones en vivo de la vista general, reinicio del módulo, modo avión y controles del modo de red USB. |
| Radio y red | Estado de registro, operador, métricas de señal, RSRP/RSRQ/SINR, modo de red, banda, canal, búsqueda de operadores, selección de red automática o manual, y consulta de celdas LTE con bloqueo/desbloqueo de celda única (EARFCN/PCI). |
| AT y USSD | Terminal AT interactivo, historial de comandos, respuestas sin procesar del módem, flujos de inicio/continuación/cancelación de USSD y reporte claro de errores del módem. |
| SMS | Envío directo de SMS celulares e IMS, sincronización entrante, manejo multiparte, informes de entrega, historial de conversaciones, estado de no leído, marcas de tiempo y estado de entrega por mensaje. |
| WiFi Calling | Establecimiento de túnel IKEv2/ePDG, autenticación EAP-AKA, registro IMS, SMS IMS, controles de reconexión, diagnósticos de estado y enrutamiento por dispositivo. |
| eSIM y eUICC | Descubrimiento de eUICC, EID e información de producción, metadatos de certificados, inventario multi-eUICC, lista de perfiles instalados, operaciones de habilitar/deshabilitar/cambiar, y operaciones de descarga, renombrado y eliminación cuando la tarjeta lo admite. |
| Política de tarjeta | Comportamiento de WiFi Calling y modo avión basado en ICCID con aplicación inmediata de la política. |
| Enrutamiento por proxy | Enrutamiento SOCKS ascendente, vinculaciones de dispositivos, reglas por país, comprobaciones de accesibilidad TCP y comprobaciones UDP Associate para las rutas de datos de WiFi Calling. |
| Notificaciones | Reenvío de nuevos SMS entrantes a través de Telegram, Bark, correo electrónico, Pushplus y webhooks firmados. Cada SMS se entrega como una notificación individual. |
| Bot de Telegram | Estado del dispositivo, lista y cambio de perfiles instalados, controles de WiFi Calling y envío de SMS. Las acciones sensibles requieren confirmación del administrador. |
| Operaciones | Autenticación por contraseña y SSO (Pocket ID, GitHub, Generic OAuth2), protección CSRF, políticas de acceso, eventos de auditoría, registros en vivo, retención de registros, comprobaciones de salud, diseño adaptable, modo oscuro e interfaz de usuario en inglés/chino. |
| Distribución | Binarios estáticos de Linux, script de instalación systemd, autoactualización con verificación SHA-256, imagen Docker, publicación en GHCR y compilaciones de versión de GitHub Actions. |

## Hardware compatible

Vocat está dirigido a módulos Quectel basados en Qualcomm y módulos China Mobile de serie ML307 que exponen interfaces AT, QMI, serie y de red USB compatibles, incluyendo:

- Quectel EC20
- Quectel EC25
- Familia Quectel EG25
- Módulos EG600 compatibles y relacionados
- Serie China Mobile ML307 (por ejemplo, ML307A-E5, diversos OEM)

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

Este modo otorga intencionadamente a Vocat un amplio acceso a los dispositivos y a la pila de red del host. Úselo solo en un host Linux de confianza. El descubrimiento automático identifica los módems USB Quectel compatibles (ID de fabricante USB `2c7c`), módulos de la serie China Mobile ML307 por cadena de producto y los módems PCIe/MHI expuestos a través del subsistema Linux WWAN; no identifica disposiciones arbitrarias de módems. Mapear solo nodos individuales con `--device`, como `/dev/ttyUSB2`, `/dev/cdc-wdm0` o `/dev/wwan0qmi0`, limita el contenedor a esos nodos fijos y no permite un descubrimiento completo de múltiples dispositivos o de conexiones en caliente.

En hosts OpenWrt, la cadena input de fw4 rechaza nuevas conexiones de interfaces que no gestiona, incluida la interfaz TUN VoWiFi por sesión creada por Vocat. Por lo tanto, Vocat inserta reglas de aceptación dirigidas `vocat-ims-*` (desde el P-CSCF hacia la dirección interna negociada) utilizando el binario `nft` incluido en la imagen. Sin ellas, el registro IMS y los SMS salientes funcionan, pero los SMS y llamadas entrantes se descartan. Si el binario `nft` incluido no puede inspeccionar el estado de las reglas del host, Vocat registra `nft could not inspect the OpenWrt fw4 input chain`; apunte `VOCAT_NFT` al nft del host:

```bash
cat > /opt/vocat-host-nft <<'EOF'
#!/bin/sh
exec /host/lib/ld-musl-$(uname -m).so.1 --library-path /host/usr/lib:/host/lib /host/usr/sbin/nft "$@"
EOF
chmod 755 /opt/vocat-host-nft
# Añada al comando docker run anterior:
#   -v /usr:/host/usr:ro -v /lib:/host/lib:ro \
#   -v /opt/vocat-host-nft:/usr/local/bin/host-nft:ro \
#   -e VOCAT_NFT=/usr/local/bin/host-nft
```

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

### Datos de host celulares vía AT

Los datos ECM/RNDIS gestionados mediante AT requieren Linux, iproute2 y BusyBox con `udhcpc`.
VoCat debe gestionar de forma exclusiva la interfaz de red del módem; deshabilite clientes DHCP o perfiles de NetworkManager en conflicto para esa interfaz. VoCat mantiene una concesión IPv4 y rutas de directiva marcadas sin modificar el enrutamiento predeterminado ni la configuración de resolución DNS del sistema. Esta ruta requiere actualmente un servicio celular compatible con IPv4.

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
| `VOCAT_MCP_ENABLED` | `true` | Expone el endpoint MCP en `/mcp` para agentes de IA. Establezca `false` para deshabilitarlo. |
| `VOCAT_PROBE_UNKNOWN_USB` | `false` | Habilita el sondeo heurístico con comandos AT en dispositivos serie USB no reconocidos. El sondeo cambia la configuración serie y borra la entrada; habilítelo solo cuando esos puertos estén dedicados a módems. Las identidades de módems verificados se descubren automáticamente. Clave JSON: `probe_unknown_usb`. |
| `VOCAT_REPO` | `MengMengCode/VoCat` | Repositorio de GitHub de confianza usado por el autoactualizador, en formato `owner/name`. |
| `GITHUB_TOKEN` | vacío | Token de GitHub opcional para repositorios privados o límites de API más altos. |

Los paquetes de operador de Apple proporcionados por el usuario pueden convertirse con `vocat carrier import-ipcc` en perfiles de operador revisables y limitados a una lista de elementos permitidos; consulte [docs/CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md).

Las credenciales del administrador se almacenan únicamente en SQLite. Inicialice una base de datos vacía una sola vez con `vocat bootstrap-admin`; las variables de entorno y la configuración JSON no pueden establecer ni sobrescribir el nombre de usuario o la contraseña del administrador.

No almacene tokens de Telegram, contraseñas SMTP, secretos de webhook, credenciales SIM u otros datos privados en el repositorio. Configúrelos a través de los ajustes de la aplicación o archivos de entorno protegidos.

## Inicio de sesión único (SSO)

VoCat admite Pocket ID mediante OpenID Connect, aplicaciones OAuth de GitHub.com y proveedores Generic OAuth2. Seleccione un proveedor en **Ajustes → Inicio de sesión único (SSO)**. El SSO comienza deshabilitado y el inicio de sesión con contraseña permanece disponible. Inicie sesión primero con el administrador local, guarde un proveedor y vincule explícitamente una cuenta externa antes de habilitar el SSO. Otras cuentas externas no pueden registrarse ni convertirse en administradores, incluidas las cuentas que compartan la dirección de correo electrónico de la cuenta vinculada.

### Pocket ID

1. Utilice un origen HTTPS para VoCat, como `https://vocat.example.com`, y configure `VOCAT_SECURE_COOKIES=true`. En la configuración de SSO, seleccione **Pocket ID**. El origen público utiliza inicialmente la dirección actual del navegador; edite el campo visible **Origen público de VoCat** si es necesario. La URL de retorno compacta y el botón Copiar aparecen de inmediato, por lo que la URL se puede copiar antes de crear el cliente OIDC o guardar credenciales: `https://vocat.example.com/api/auth/sso/callback`.
2. En Pocket ID, cree un cliente OIDC **Confidencial** independiente para VoCat. Registre la URL de retorno exacta y verifique que el usuario o grupo previsto tenga permiso para utilizar este cliente. VoCat y Komari necesitan clientes independientes y sesiones locales separadas.
3. En VoCat, introduzca la `APP_URL` pública exacta de Pocket ID como Issuer, e introduzca el Client ID y el Client Secret emitidos para el cliente creado en el paso 2. El campo Client ID incluye un recordatorio de dónde encontrar este valor. Los scopes y PKCE S256 son gestionados por VoCat. La autenticación de token se selecciona a partir de los metadatos de Discovery validados, prefiriendo `client_secret_basic` sobre `client_secret_post`. Guarde mientras el SSO permanezca deshabilitado. Las ediciones del origen actualizan la URL de retorno mostrada de inmediato; surten efecto para la autorización tras guardar.
4. Compruebe la configuración guardada; si tiene éxito, se mostrará **Comprobación de configuración superada**. A continuación, vincule la cuenta. Complete el inicio de sesión con Passkey y el consentimiento de Pocket ID, y confirme la vinculación devuelta en VoCat. La comprobación de configuración valida los metadatos y la conectividad; no puede verificar un secreto de cliente ni la autorización del usuario sin este inicio de sesión.
5. Habilite el SSO y guarde. Utilice una ventana privada del navegador para verificar el inicio de sesión por SSO, el retorno a la página solicitada y la primera solicitud que modifique el estado.

La página de ajustes muestra la cuenta vinculada y una acción de desvinculación. Para cambiar la cuenta externa, desvincúlela primero, luego vincule la cuenta deseada y habilite de nuevo el SSO.

El Issuer se compara de forma estricta, incluyendo su ruta y la barra diagonal final. El inicio de sesión de Pocket ID utiliza el `issuer + sub` del ID Token validado; no se requiere un endpoint UserInfo ni la notificación de correo electrónico. Discovery y sus endpoints de autorización, Token y JWKS deben utilizar el origen del Issuer configurado. Las implementaciones que utilicen `INTERNAL_APP_URL` con un origen de backend diferente no son compatibles en esta versión. Mantenga la `APP_URL` pública como Issuer. Los métodos de autenticación y scopes explícitamente configurados se conservan al guardar.

### GitHub y Generic OAuth2

Para GitHub, cree una aplicación OAuth en GitHub.com con exactamente la misma URL de retorno, seleccione **GitHub**, introduzca su Client ID y Secret, y siga la secuencia de guardar, comprobar, vincular y habilitar descrita anteriormente. Los endpoints son fijos en GitHub.com; el scope predeterminado es `read:user` y se requiere PKCE S256. La vinculación sigue el identificador de cuenta numérico estable de GitHub, por lo que cambiar el nombre de usuario no cambia la cuenta. GitHub Enterprise queda fuera del alcance de esta versión.

Para Generic OAuth2, proporcione las URL de autorización, Token y UserInfo además de las credenciales del cliente. Los scopes, el campo de ID de usuario de nivel superior (por defecto `id`), la autenticación de token y las opciones de compatibilidad con PKCE se muestran cuando se selecciona Generic OAuth2. UserInfo se solicita con un token de acceso Bearer. El ID debe ser una cadena no vacía o un número entero; los identificadores enteros grandes conservan su valor exacto. La autenticación de token predeterminada es `client_secret_post` y PKCE es S256. Deshabilite PKCE únicamente para un proveedor que no lo admita. Generic OAuth2 no realiza la verificación de ID Token OIDC.

### Red, credenciales y sesiones

La directiva de red de origen existente de VoCat también se aplica a las rutas de SSO. Configurar SSO no hace que una instalación sea accesible públicamente. El navegador debe poder acceder a VoCat y al endpoint de autorización; el proceso de VoCat debe poder comunicarse con Discovery, los endpoints de Token y JWKS/UserInfo con certificados TLS de confianza. Guardar el Issuer HTTPS exacto de Pocket ID o las URL de endpoints de Generic aprueba esas direcciones para conexiones HTTPS, incluidas las direcciones privadas RFC1918/IPv6 ULA. Los endpoints fijos de GitHub permanecen restringidos a direcciones públicas.

Para el servicio HTTP interno de un proveedor Generic, introduzca explícitamente su URL HTTP completa en el campo Token/UserInfo. Cada candidato DNS y conexión real para estos destinos HTTP debe ser una dirección privada permitida. Discovery de Pocket ID no puede aprobar otro origen; sus endpoints deben utilizar el origen del Issuer configurado. El Issuer, la URL de obtención de Discovery y el endpoint de autorización del navegador siempre requieren HTTPS. Las direcciones de bucle local (loopback), enlace local (link-local), metadatos en la nube y otros destinos de uso especial permanecen bloqueados para las conexiones con el IdP. Instale certificados de CA privados en el almacén de confianza del host/contenedor; no existe opción para eludir la verificación TLS. El acceso del navegador a VoCat mediante localhost o HTTPS es independiente de estos permisos de conexión con el IdP.

El despliegue de Compose proporcionado utiliza la red del host. Verifique el DNS, el enrutamiento y la confianza de CA desde el propio contenedor; no asuma que los nombres de servicio en red tipo bridge estén disponibles. Los servidores proxy inversos deben conservar el origen público configurado. VoCat no deriva las URL de retorno a partir de cabeceras de host reenviadas. Se requiere un despliegue en la ruta raíz. Para el desarrollo local con Vite, utilice un único origen de bucle local coherente, como `http://127.0.0.1:5173`, para el origen público y el registro de la URL de retorno; las excepciones de transporte de IdP solo deben usarse en entornos de prueba.

Las cookies de sesión y CSRF conservan `SameSite=Strict`; las cookies de autorización de corta duración utilizan `SameSite=Lax` para retornos GET de nivel superior. Guardar cualquier configuración de SSO, incluida su desactivación o la rotación del secreto, revoca todas las sesiones de SSO. Desvincular una cuenta revoca las sesiones de SSO asociadas a esa vinculación. Las sesiones por contraseña y los tokens de API sobreviven a estas acciones. Cambiar la contraseña del administrador local revoca todas las sesiones y tokens de API, cancela las autorizaciones en curso y conserva la vinculación externa. La interfaz solicita iniciar sesión nuevamente cuando una operación exitosa revoca la sesión de SSO actual.

Deje el campo de secreto vacío para conservar un secreto existente, o introduzca un nuevo secreto para reemplazarlo. La página de ajustes no ofrece un control para borrar un secreto. Cambiar de proveedor, Client ID, Issuer o los endpoints/campo de ID de Generic cambia el espacio de nombres de identidad y requiere nuevas credenciales y una nueva vinculación; las vinculaciones existentes requieren confirmación explícita de restablecimiento. Para cambiar la cuenta vinculada, desvincúlela y luego cree una nueva vinculación. Los secretos de cliente se conservan únicamente en el servidor dentro de la base de datos SQLite protegida; la etiqueta de ajuste sensible ofusca las respuestas y no cifra la base de datos. Los tokens de terceros no se almacenan ni se devuelven al navegador.

Cerrar sesión finaliza únicamente la sesión actual de VoCat. Deshabilitar una cuenta de IdP o cerrar sesión en Pocket ID no revoca de inmediato una sesión existente de VoCat; esta expira según `VOCAT_SESSION_TTL`, o puede revocarse deshabilitando el SSO o desvinculando la cuenta en VoCat. El endpoint público de capacidades es `GET /api/auth/providers`. Las API de ajustes y vinculación de SSO requieren una sesión de navegador y CSRF para operaciones de escritura; los tokens Bearer no pueden gestionarlas. Los conflictos de revisión de configuración devuelven `409 sso_config_changed`; los cambios de origen de identidad devuelven `409 sso_rebind_required`.

### Actualización y reversión

SSO añade la versión de esquema SQLite 29. Las actualizaciones conservan el administrador existente, las sesiones, los tokens de API y los datos de los dispositivos; el SSO permanece deshabilitado hasta que se configure. Antes de actualizar, anote el binario anterior o el digest de la imagen y haga una copia de seguridad de la configuración, los certificados CA y el directorio de datos persistente completo mientras VoCat está detenido, o use la API de copia de seguridad de SQLite / `.backup` para obtener una instantánea coherente en caliente. Copiar únicamente un archivo de base de datos en ejecución en modo WAL no es una copia de seguridad confiable.

Deshabilite el SSO en la nueva versión para solucionar problemas con el proveedor mientras mantiene el inicio de sesión con contraseña y los datos actuales. Volver a un binario con esquema 28 requiere detener VoCat y restaurar la instantánea de datos y configuración previa a la actualización correspondiente. La simple reversión de la imagen del contenedor o la reducción de `PRAGMA user_version` no pueden degradar la base de datos. Restaure en un directorio/volumen separado siempre que sea posible y conserve la base de datos más reciente para su investigación. Restaurar una instantánea provoca la pérdida de mensajes y cambios en tareas registrados después de esa copia de seguridad. Para Docker, actualice descargando la nueva imagen y recreando el contenedor.

Antes de un despliegue en producción, complete la prueba real de Passkey/política de acceso de Pocket ID y un ensayo de actualización y reversión para ese entorno.

## Tokens de API y MCP

Los clientes que no son navegadores se autentican contra los mismos endpoints `/api/...` con un token Bearer en lugar de la cookie de sesión y el flujo CSRF del navegador:

```bash
vocat token create --read-only --ttl 720h ci-runner   # muestra el token una sola vez
curl -H "Authorization: Bearer vcat_..." http://127.0.0.1:7575/api/devices
```

`vocat token list` y `vocat token revoke <name>` gestionan el inventario. Los tokens actúan con privilegios de administrador; un token `--read-only` solo puede emitir solicitudes `GET` (una excepción documentada: leer un hilo de SMS lo marca como leído localmente — la regla es «sin modificaciones de hardware o red»). Cambiar la contraseña del administrador revoca todos los tokens. Cree tokens después del primer `bootstrap-admin`. Los clientes remotos con Bearer también necesitan una política de acceso de red que admita su dirección de origen; el modo predeterminado `internal` solo atiende redes privadas.

Por defecto (`VOCAT_MCP_ENABLED=false` para deshabilitarlo), el mismo servicio expone un endpoint MCP (Model Context Protocol) para agentes de IA en `http://<host>:7575/mcp` utilizando el transporte Streamable HTTP y autenticación por token Bearer. Las herramientas cubren el estado de los dispositivos, hilos de SMS y envío protegido, listado y cambio de perfiles eSIM, y estado/reconexión de WiFi Calling; las operaciones sensibles (comandos AT directos, descarga/eliminación de eSIM, PIN de SIM, selección de red, actualización del sistema) no están expuestas, y la salida de las herramientas nunca incluye IMSI, PIN de SIM ni códigos de activación. Un token de solo lectura recibe una lista de herramientas sin las herramientas de escritura. El envío de SMS sigue la misma política de destinos bloqueados y el mismo límite horario global que la interfaz web y se envía a través de la ruta celular AT (el enrutamiento preferente por IMS de la interfaz web cuando WiFi Calling está habilitado permanece en el lado web, y los mensajes enviados a través de MCP aún no se registran en el historial de conversaciones web).

Ejemplo de configuración de cliente (con Claude Code):

```json
{
  "mcpServers": {
    "vocat": {
      "type": "http",
      "url": "http://192.168.1.10:7575/mcp",
      "headers": { "Authorization": "Bearer vcat_..." }
    }
  }
}
```

Consulte [docs/API_MCP_DEVELOPMENT_PLAN.md](API_MCP_DEVELOPMENT_PLAN.md) para el documento de diseño.

## Bot de Telegram

Cuando las notificaciones de Telegram están habilitadas y tanto el Chat ID como el Admin ID están configurados, el bot admite:

```text
/status [device]
/esim <device>
/switch <device> <iccid>
/wfc <device> <status|on|off|reconnect>
/sms <device> <number> <message>
/ussd <device> <code>
/ussd_reply <session> <input>
/ussd_cancel <session>
```

El cambio de perfil y el envío de SMS usan botones de confirmación de un solo uso. El bot no expone comandos de descarga, eliminación o renombrado de eSIM.

El terminal web y Telegram comparten el enrutamiento USSD. Con WiFi Calling habilitado, las solicitudes utilizan IMS/USSI y devuelven un error de espera hasta que IMS se registre, sin recurrir a AT+CUSD celular. Con WiFi Calling deshabilitado, las solicitudes utilizan USSD celular. Las respuestas a los menús y las cancelaciones permanecen vinculadas al dispositivo y diálogo originales. Los errores de cancelación se reportan y conservan la sesión para reintentar; un envío incierto nunca se reenvía automáticamente por otra vía.

El manejo de diálogos USSI sigue la norma [3GPP TS 24.390](https://www.etsi.org/deliver/etsi_ts/124300_124399/124390/18.00.00_60/ts_124390v180000p.pdf). Si falla un BYE, se conserva un registro de cancelación, pero cualquier entrada adicional en el menú se bloquea una vez iniciada la cancelación ([RFC 3261, sección 15.1.1](https://www.rfc-editor.org/rfc/rfc3261.html#section-15.1.1)).

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

## Star History

<a href="https://www.star-history.com/?repos=mengmengcode%2Fvocat&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
 </picture>
</a>
