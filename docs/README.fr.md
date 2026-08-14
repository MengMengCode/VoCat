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

[English](../README.md) | [العربية](README.ar.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | **Français** | [Русский](README.ru.md) | [Español](README.es.md) | [日本語](README.ja.md)

Vocat est un panneau de contrôle web open-source et une boîte à outils d'ingénierie pour les modems cellulaires Quectel de classe EC20/EC25. Il réunit, dans un service autonome unique, la découverte de modems, l'état radio en direct, les terminaux AT et USSD, les SMS, la WiFi Calling, la gestion eSIM, la sélection de réseau, le routage par proxy, les notifications, les journaux d'audit et l'automatisation des versions.

Le backend est écrit en Go, l'interface est construite avec React et TypeScript, et le frontend de production est intégré dans le binaire Go. Un seul exécutable contient l'application web et utilise SQLite pour l'état persistant.

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## Fonctionnalités

| Domaine | Ce que Vocat fournit |
| --- | --- |
| Gestion des appareils | Découverte série/USB automatique, prise en charge de plusieurs modems, noms d'appareils conviviaux, mises à jour en direct de la vue d'ensemble, redémarrage du module, mode avion et contrôles du mode réseau USB. |
| Radio et réseau | État d'enregistrement, opérateur, métriques de signal, RSRP/RSRQ/SINR, mode réseau, bande, canal, recherche d'opérateurs et sélection de réseau automatique ou manuelle. |
| AT et USSD | Terminal AT interactif, historique des commandes, réponses brutes du modem, flux de démarrage/poursuite/annulation USSD et rapport d'erreurs modem clair. |
| SMS | Envoi direct de SMS cellulaires et IMS, synchronisation entrante, gestion des messages multiparties, rapports de livraison, historique des conversations, état non lu, horodatages et statut de livraison par message. |
| WiFi Calling | Établissement de tunnel IKEv2/ePDG, authentification EAP-AKA, enregistrement IMS, SMS IMS, contrôles de reconnexion, diagnostics d'état et routage par appareil. |
| eSIM et eUICC | Découverte eUICC, EID et informations de production, métadonnées de certificat, inventaire multi-eUICC, liste des profils installés, opérations d'activation/désactivation/commutation, ainsi que téléchargement, renommage et suppression lorsque la carte le permet. |
| Politique de carte | Comportement WiFi Calling et mode avion basé sur l'ICCID avec application immédiate de la politique. |
| Routage par proxy | Routage SOCKS amont, liaisons d'appareils, règles par pays, vérifications d'accessibilité TCP et vérifications UDP Associate pour les chemins de données WiFi Calling. |
| Notifications | Transfert des nouveaux SMS entrants via Telegram, Bark, e-mail, Pushplus et webhooks signés. Chaque SMS est livré comme une notification individuelle. |
| Bot Telegram | État de l'appareil, liste et commutation des profils installés, contrôles WiFi Calling et envoi de SMS. Les actions sensibles nécessitent une confirmation de l'administrateur. |
| Exploitation | Authentification, protection CSRF, politiques d'accès, événements d'audit, journaux en direct, rétention des journaux, vérifications de santé, mise en page réactive, mode sombre et interface utilisateur en anglais/chinois. |
| Distribution | Binaires Linux statiques, script d'installation systemd, auto-mise à jour avec vérification SHA-256, image Docker, publication GHCR et builds de version GitHub Actions. |

## Matériel pris en charge

Vocat cible les modules Quectel à base Qualcomm qui exposent des interfaces AT, QMI, série et réseau USB compatibles, notamment :

- Quectel EC20
- Quectel EC25
- Famille Quectel EG25
- Modules EG600 compatibles et apparentés

Les fonctionnalités disponibles dépendent du firmware du module, de la composition USB, des capacités SIM/eSIM, des pilotes hôtes, du réseau radio et de la configuration de l'opérateur.

## Installation

### Installation Linux en un clic

En tant que root (y compris OpenWrt/Kwrt, où `sudo` est normalement absent) :

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash
```

Depuis un utilisateur normal sur une distribution disposant de sudo :

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

Vérifier les prérequis VoWiFi/XFRM de l'hôte sans installer VoCat :

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash -s -- --check-env
```

Installer une version spécifique :

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

VoWiFi IMS nécessite Linux XFRM/IPsec. Sur OpenWrt/Kwrt, le programme d'installation tente
d'installer les paquets correspondants `ip-full`, `kmod-ipsec`, `kmod-ipsec4/6`,
`kmod-crypto-authenc`, AES-CBC et SHA1 depuis le dépôt du firmware lui-même.
Si des modules noyau correspondants ne sont pas disponibles, utilisez un firmware qui les inclut ;
ne forcez jamais l'installation de kmods compilés pour un noyau différent.

Le programme d'installation :

- détecte `amd64`, `386`, `arm64`, `aarch64` ou `armv7` ;
- télécharge le binaire GitHub Release correspondant ;
- le vérifie par rapport à `SHA256SUMS` ;
- installe Vocat sous `/opt/vocat` ;
- crée un service systemd renforcé disposant des accès matériel et réseau requis par Vocat ;
- stocke la configuration d'exécution dans `/etc/vocat/env` ;
- génère un mot de passe administrateur initial aléatoire lors de la première installation.

Après l'installation, ouvrez :

```text
http://<adresse-du-serveur>:7575
```

### Installation manuelle du binaire

Téléchargez le binaire correspondant et `SHA256SUMS` depuis GitHub Releases :

| Plateforme | Fichier de version |
| --- | --- |
| Linux x86-64 | `vocat-linux-amd64` |
| Linux x86 32 bits | `vocat-linux-386` |
| Linux ARM64 | `vocat-linux-arm64` |
| Linux AArch64 | `vocat-linux-aarch64` |
| Linux ARMv7 | `vocat-linux-armv7` |

Vérifiez-le et installez-le :

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

Cette commande manuelle exécute Vocat au premier plan. Utilisez `vocat serve` pour que le
processus démarre directement le serveur ; exécuter `vocat` sans argument en tant que root
sur un TTY ouvre plutôt le menu de gestion interactif. Utilisez le programme d'installation
en un clic lorsqu'un service systemd géré et un redémarrage automatique sont requis.

### Docker

Pour un hôte Linux qui doit découvrir chaque modem Quectel pris en charge connecté et
continuer à voir les événements de branchement à chaud USB, exécutez Vocat en mode d'accès matériel :

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

Ouvrez `http://<adresse-du-serveur>:7575` après le démarrage du conteneur. Le réseau de l'hôte
est requis pour que les interfaces réseau QMI restent visibles par Vocat, tandis que l'accès
privilégié aux périphériques est requis pour les ports série, les nœuds de contrôle QMI, les
interfaces TUN, la configuration réseau et les périphériques ajoutés après le démarrage du
conteneur. Le montage bind de `/dev` rend les nouveaux nœuds `ttyUSB*`, `ttyACM*` et `cdc-wdm*`
visibles sans recréer le conteneur.

Ce mode donne intentionnellement à Vocat un large accès aux périphériques et à la pile réseau
de l'hôte. Ne l'utilisez que sur un hôte Linux de confiance. La découverte automatique
identifie actuellement les modems USB Quectel pris en charge (ID fabricant USB `2c7c`), et non
des marques de modems arbitraires. Le mappage de nœuds individuels uniquement avec `--device`,
comme `/dev/ttyUSB2` et `/dev/cdc-wdm0`, limite le conteneur à ces nœuds fixes et ne fournit
pas une découverte multi-périphériques ou à chaud complète.

L'image GHCR est publiée pour `linux/amd64` et `linux/arm64`.

## Configuration

Vocat lit un fichier de configuration JSON optionnel depuis `VOCAT_CONFIG`, puis applique les variables d'environnement `VOCAT_*`. Les variables d'environnement ont la priorité.

| Variable d'environnement | Par défaut | Description |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | Adresse d'écoute HTTP. |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | Chemin de la base de données SQLite. |
| `VOCAT_SESSION_TTL` | `24h` | Durée de vie de la session d'authentification. |
| `VOCAT_SECURE_COOKIES` | `false` | Marque les cookies de session comme sécurisés lorsque HTTPS est utilisé. |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | Délai d'arrêt gracieux. |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | Taille maximale du corps de requête API. |
| `VOCAT_REPO` | `MengMengCode/VoCat` | Dépôt GitHub de confiance utilisé par l'auto-updater, au format `owner/name`. |
| `GITHUB_TOKEN` | vide | Jeton GitHub optionnel pour les dépôts privés ou des limites d'API plus élevées. |

Ne stockez pas de jetons Telegram, mots de passe SMTP, secrets de webhook, identifiants SIM ou autres données privées dans le dépôt. Configurez-les via les paramètres de l'application ou des fichiers d'environnement protégés.

## Bot Telegram

Lorsque les notifications Telegram sont activées et que le Chat ID et l'Admin ID sont configurés, le bot prend en charge :

```text
/status [appareil]
/esim <appareil>
/switch <appareil> <iccid>
/wfc <appareil> <status|on|off|reconnect>
/sms <appareil> <numéro> <message>
```

La commutation de profil et l'envoi de SMS utilisent des boutons de confirmation à usage unique. Le bot n'expose pas les commandes de téléchargement, de suppression ou de renommage eSIM.

## Mise à jour

Vérifier l'existence d'une GitHub Release plus récente :

```bash
vocat update --check --repo MengMengCode/VoCat
```

Installer la dernière version :

```bash
sudo vocat update --repo MengMengCode/VoCat
```

L'updater télécharge le binaire correspondant à l'architecture Linux actuelle, le vérifie avec le `SHA256SUMS` publié, remplace l'exécutable de manière atomique et redémarre le service systemd `vocat` lorsqu'il est disponible.

Pour les installations Docker :

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

Recréez le conteneur après avoir tiré la nouvelle image.

## Développement

Prérequis :

- Go 1.25 ou plus récent
- Node.js 20 ou plus récent
- npm

Lancer le serveur de développement frontend :

```bash
cd web
npm install
npm run dev
```

Construire le frontend intégré et démarrer le backend :

```bash
cd web
npm run build
cd ..
go run ./cmd/vocat
```

Exécuter tous les tests :

```bash
go test ./...
```

Construire un binaire de production :

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## Automatisation des versions

Pousser un tag de version déclenche deux workflows GitHub Actions :

- `release-binaries` construit et publie les binaires `amd64`, `386`, `arm64`, `aarch64` et `armv7` ainsi que `SHA256SUMS`.
- `docker` construit et publie une image multi-architecture vers GitHub Container Registry.

```bash
git tag v0.2.0
git push origin v0.2.0
```

## Structure du projet

```text
cmd/vocat/                  Point d'entrée de l'application et CLI
internal/device/            Découverte de modems et contrôle des appareils
internal/modem/             Session AT et gestion des réponses
internal/server/            API HTTP, notifications et serveur web intégré
internal/store/             Persistance SQLite
internal/update/            Auto-updater GitHub Release
internal/vowifi/            Runtime IKE, EAP-AKA, IMS et WiFi Calling
scripts/install.sh          Installeur et updater Linux
web/src/                    Frontend React et TypeScript
.github/workflows/          Automatisation des versions binaires et Docker
```

## Utilisation responsable

Les opérations sur les modems cellulaires et les eSIM peuvent affecter le service de l'abonné, les profils stockés, l'enregistrement réseau et l'état du matériel. Effectuez des sauvegardes, examinez attentivement les actions destructrices et n'utilisez le logiciel que dans des environnements légaux où vous êtes autorisé à exploiter le matériel et les ressources réseau connectés.

Vocat ne contourne ni l'authentification de l'opérateur, ni la politique réseau, ni la sécurité matérielle, ni les exigences de confiance eSIM. La prise en charge d'une opération signifie que Vocat peut la demander au modem ou à l'eUICC ; l'appareil, le profil, le réseau ou l'opérateur peut toujours la refuser.

## Contribution

Les issues et pull requests sont les bienvenues. Gardez des changements ciblés, incluez des tests lorsque c'est possible, évitez de committer des identifiants ou des données d'abonnés, et documentez clairement les comportements spécifiques au matériel.

Avant de soumettre un changement :

```bash
go test ./...
cd web && npm run build
```

## Remerciements
- [Nodeseek.com](https://www.nodeseek.com) — Une communauté dédiée aux serveurs
- [Linux.do](https://linux.do) — Une communauté technologique inspirante
- [iniwex5](https://github.com/iniwex5) — Directives de style et de fonctionnalité

## Offrez-moi un café

| Réseau | Adresse |
| ------- | ------- |
| USDT-TRON (TRC20) | `TQQAbboBoU8h5xX4YCA1rqWJU2WjK3seSg` |
| USDT-BSC (BEP20) | `0xdbfcd4a462550d6ff06d09cbd89026c6b145d9c4` |
| USDT-Polygon | `0xdbfcd4a462550d6ff06d09cbd89026c6b145d9c4` |

## Licence

Voir [LICENSE](../LICENSE).

[![MengMengCode/VoCat Star History](https://mengmeng.meteor-history.com/api/embed/MengMengCode/VoCat.svg?sig=sdeXRVxAoY3yLWgXL7JViY2USYIN3t9neJ6ScPvgUAo&theme=light&style=xkcd&color=dd4528&background=ffffff&textColor=000000&width=900&height=600&lineWidth=3&showTitle=true&showLegend=true&showDots=false&v=0.0.14)](https://meteor-history.com)
