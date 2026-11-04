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

Vocat est un panneau de contrôle web open-source et une boîte à outils d'ingénierie pour les modems cellulaires Quectel de classe EC20/EC25 et China Mobile de série ML307. Il réunit, dans un service autonome unique, la découverte de modems, l'état radio en direct, les terminaux AT et USSD, les SMS, les appels WiFi (WiFi Calling), la gestion eSIM, la sélection de réseau, le routage par proxy, les notifications, les journaux d'audit et l'automatisation des versions.

Le backend est écrit en Go, l'interface est construite avec React et TypeScript, et le frontend de production est intégré dans le binaire Go. Un seul exécutable contient l'application web et utilise SQLite pour l'état persistant.

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## Fonctionnalités

| Domaine | Ce que Vocat fournit |
| --- | --- |
| Gestion des appareils | Découverte série/USB automatique, prise en charge de plusieurs modems, noms d'appareils conviviaux, mises à jour en direct de la vue d'ensemble, redémarrage du module, mode avion et contrôles du mode réseau USB. |
| Radio et réseau | État d'enregistrement, opérateur, métriques de signal, RSRP/RSRQ/SINR, mode réseau, bande, canal, recherche d'opérateurs, sélection de réseau automatique ou manuelle, et interrogation des cellules LTE avec verrouillage/déverrouillage d'une cellule unique (EARFCN/PCI). |
| AT et USSD | Terminal AT interactif, historique des commandes, réponses brutes du modem, flux de démarrage/poursuite/annulation USSD et rapport d'erreurs modem clair. |
| SMS | Envoi direct de SMS cellulaires et IMS, synchronisation entrante, gestion des messages multiparties, rapports de livraison, historique des conversations, état non lu, horodatages et statut de livraison par message. |
| WiFi Calling | Établissement de tunnel IKEv2/ePDG, authentification EAP-AKA, enregistrement IMS, SMS IMS, contrôles de reconnexion, diagnostics d'état et routage par appareil. |
| eSIM et eUICC | Découverte eUICC, EID et informations de production, métadonnées de certificat, inventaire multi-eUICC, liste des profils installés, opérations d'activation/désactivation/commutation, ainsi que téléchargement, renommage et suppression lorsque la carte le permet. |
| Politique de carte | Comportement WiFi Calling et mode avion basé sur l'ICCID avec application immédiate de la politique. |
| Routage par proxy | Routage SOCKS amont, liaisons d'appareils, règles par pays, vérifications d'accessibilité TCP et vérifications UDP Associate pour les chemins de données WiFi Calling. |
| Notifications | Transfert des nouveaux SMS entrants via Telegram, Bark, e-mail, Pushplus et webhooks signés. Chaque SMS est livré comme une notification individuelle. |
| Bot Telegram | État de l'appareil, liste et commutation des profils installés, contrôles WiFi Calling et envoi de SMS. Les actions sensibles nécessitent une confirmation de l'administrateur. |
| Exploitation | Authentification par mot de passe et SSO (Pocket ID, GitHub, Generic OAuth2), protection CSRF, politiques d'accès, événements d'audit, journaux en direct, rétention des journaux, vérifications de santé, mise en page réactive, mode sombre et interface utilisateur en anglais/chinois. |
| Distribution | Binaires Linux statiques, script d'installation systemd, auto-mise à jour avec vérification SHA-256, image Docker, publication GHCR et builds de version GitHub Actions. |

## Matériel pris en charge

Vocat cible les modules Quectel à base Qualcomm et les modules China Mobile de série ML307 qui exposent des interfaces AT, QMI, série et réseau USB compatibles, notamment :

- Quectel EC20
- Quectel EC25
- Famille Quectel EG25
- Modules EG600 compatibles et apparentés
- Série China Mobile ML307 (par exemple ML307A-E5, divers OEM)

Les fonctionnalités disponibles dépendent du firmware du module, de la composition USB, des capacités SIM/eSIM, des pilotes hôtes, du réseau radio et de la configuration de l'opérateur.

## Installation

### Installation Linux en un clic

En tant que root (y compris OpenWrt/Kwrt, où `sudo` est généralement absent) :

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash
```

Depuis un utilisateur ordinaire sur une distribution disposant de sudo :

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

VoWiFi IMS nécessite Linux XFRM/IPsec. Sur OpenWrt/Kwrt, le script d'installation tente d'installer depuis les dépôts du firmware les paquets correspondants : `ip-full`, `kmod-ipsec`, `kmod-ipsec4/6`, `kmod-crypto-authenc`, AES-CBC et SHA1. Si les modules de noyau correspondants ne sont pas disponibles, utilisez un firmware qui les intègre ; ne forcez jamais l'installation de modules kmod compilés pour un autre noyau.

Si votre noyau ne peut pas fournir XFRM/IPsec et que vous n'avez besoin que de fonctionnalités hors VoWiFi comme les SMS ou données cellulaires, installez avec `--skip-vowifi-check` :

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh --skip-vowifi-check
```

L'installateur :

- détecte l'architecture `amd64`, `386`, `arm64`, `aarch64` ou `armv7` ;
- télécharge le binaire GitHub Release correspondant ;
- le vérifie par rapport à `SHA256SUMS` ;
- installe Vocat sous `/opt/vocat` ;
- crée un service systemd sécurisé avec les accès matériel et réseau requis par Vocat ;
- enregistre la configuration d'exécution dans `/etc/vocat/env` ;
- génère un mot de passe administrateur initial aléatoire lors de la première installation.

Après l'installation, ouvrez :

```text
http://<server-address>:7575
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

Vérifiez et installez :

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

Cette commande manuelle exécute Vocat au premier plan. Utilisez `vocat serve` pour que le processus démarre directement le serveur ; exécuter `vocat` sans argument en tant que root dans un TTY ouvre plutôt le menu de gestion interactif. Utilisez l'installateur en un clic lorsqu'un service systemd géré et un redémarrage automatique sont requis.

### Docker

Pour un hôte Linux qui doit découvrir chaque modem Quectel pris en charge connecté et continuer à voir les événements de branchement à chaud USB, exécutez Vocat en mode accès matériel :

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

Ouvrez `http://<server-address>:7575` après le démarrage du conteneur. Le réseau hôte est requis pour que les interfaces réseau QMI restent visibles par Vocat, tandis que l'accès privilégié aux périphériques est nécessaire pour les ports série, les nœuds de contrôle QMI, les interfaces TUN, la configuration réseau et les périphériques ajoutés après le démarrage du conteneur. Le montage de liaison `/dev` rend les nouveaux nœuds `ttyUSB*`, `ttyACM*`, `cdc-wdm*` et `wwan*` MHI visibles sans recréer le conteneur.

Ce mode donne délibérément à Vocat un accès étendu aux périphériques et à la pile réseau de l'hôte. Ne l'utilisez que sur un hôte Linux de confiance. La découverte automatique identifie les modems USB Quectel pris en charge (identifiant de vendeur USB `2c7c`), les modules China Mobile de série ML307 par chaîne de produit, et les modems PCIe/MHI exposés via le sous-système WWAN de Linux ; elle n'identifie pas les configurations arbitraires de modems. Mapper uniquement des nœuds individuels avec `--device`, tels que `/dev/ttyUSB2`, `/dev/cdc-wdm0` ou `/dev/wwan0qmi0`, limite le conteneur à ces nœuds fixes et ne fournit pas de découverte complète multi-appareils ou de branchement à chaud.

Sur les hôtes OpenWrt, la chaîne input de fw4 rejette les nouvelles connexions provenant des interfaces qu'elle ne gère pas, y compris l'interface TUN VoWiFi créée par Vocat pour chaque session. Vocat insère donc des règles d'autorisation `vocat-ims-*` ciblées (du P-CSCF vers l'adresse interne négociée) à l'aide du binaire `nft` inclus dans l'image. Sans ces règles, l'enregistrement IMS et les SMS sortants fonctionnent, mais les SMS et les appels entrants sont rejetés. Si le binaire `nft` inclus ne peut pas lire le jeu de règles de l'hôte, Vocat consigne `nft could not inspect the OpenWrt fw4 input chain` ; pointez alors `VOCAT_NFT` vers le nft de l'hôte :

```bash
cat > /opt/vocat-host-nft <<'EOF'
#!/bin/sh
exec /host/lib/ld-musl-$(uname -m).so.1 --library-path /host/usr/lib:/host/lib /host/usr/sbin/nft "$@"
EOF
chmod 755 /opt/vocat-host-nft
# Ajoutez à la commande docker run ci-dessus :
#   -v /usr:/host/usr:ro -v /lib:/host/lib:ro \
#   -v /opt/vocat-host-nft:/usr/local/bin/host-nft:ro \
#   -e VOCAT_NFT=/usr/local/bin/host-nft
```

L'image GHCR est publiée pour `linux/amd64` et `linux/arm64`.

> [!TIP]
> **Remarque sur le déploiement NAS / QNAP Container Station** :
> Sur les systèmes d'exploitation NAS comme QNAP QTS / QuTS hero (Container Station), les comptes administrateurs personnalisés non-root et les mécanismes d'isolation de volume peuvent amener les volumes nommés Docker (par exemple `-v vocat-data:/opt/vocat/data`) à pointer vers des chemins isolés différents entre l'initialisation ponctuelle `bootstrap-admin` et le conteneur de service démon, provoquant des erreurs « Mot de passe incorrect » lors de la connexion Web.
> Pour les environnements NAS, il est fortement recommandé de remplacer les volumes nommés par un montage de liaison avec chemin absolu sur l'hôte (par exemple `-v /share/Container/vocat/data:/opt/vocat/data` sur QNAP) pour l'initialisation et l'exécution, afin de garantir la persistance cohérente de la base de données SQLite.

### Lecteurs de cartes SIM USB

Les lecteurs de cartes SIM USB utilisent le service Linux PC/SC. L'installateur en un clic installe et démarre automatiquement `pcscd` ainsi que le pilote CCID sur les gestionnaires de paquets pris en charge. Sur Debian/Ubuntu, la commande manuelle équivalente est `apt install pcscd libccid`. Si l'USB détecte un lecteur CCID mais que PC/SC est indisponible, VoCat laisse le lecteur visible dans la boîte de dialogue d'ajout d'appareil et signale le service ou le pilote manquant au lieu de le masquer silencieusement.

### Utilitaires QMI en ligne de commande

VoCat utilise `qmicli` pour vérifier qu'un canal de contrôle QMI est prêt et `qmi-proxy` pour multiplexer son accès. Les sessions de données par paquets sont gérées par le client QMI WDS intégré plutôt que par les fichiers d'état CID/PDH de `qmi-network`. L'installateur en un clic installe et vérifie les utilitaires correspondants. Pour un déploiement manuel, Debian/Ubuntu utilise `apt install libqmi-utils` ; Arch Linux utilise `pacman -S libqmi`, Alpine utilise `apk add qmi-utils`, et OpenWrt utilise `opkg install qmi-utils`.

`vocat doctor --repair-dji-qmi` vérifie la présence de `qmicli` avant de modifier toute liaison de pilote USB ou d'activer le signal DTR. Si l'utilitaire est indisponible, la commande s'arrête avec une indication d'installation et conserve l'état actuel de l'appareil sans modification.

### Données d'hôte cellulaires via AT

Les données ECM/RNDIS gérées via AT nécessitent Linux, iproute2 et BusyBox avec `udhcpc`.
VoCat doit gérer de manière exclusive l'interface réseau du modem ; désactivez les clients DHCP ou profils NetworkManager concurrents pour cette interface. VoCat gère un bail IPv4 et des routes de politique marquées sans modifier la route par défaut ni la configuration de résolution DNS du système. Ce chemin nécessite actuellement un service cellulaire compatible IPv4.

## Configuration

Vocat lit un fichier de configuration JSON optionnel depuis `VOCAT_CONFIG`, puis applique les variables d'environnement `VOCAT_*`. Les variables d'environnement ont la priorité.

| Variable d'environnement | Valeur par défaut | Description |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | Adresse d'écoute HTTP. |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | Chemin de la base de données SQLite. |
| `VOCAT_SESSION_TTL` | `24h` | Durée de validité de la session d'authentification. |
| `VOCAT_SECURE_COOKIES` | `false` | Marque les cookies de session comme sécurisés en cas d'utilisation de HTTPS. |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | Délai d'arrêt gracieux. |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | Taille maximale du corps de requête API. |
| `VOCAT_MCP_ENABLED` | `true` | Exposer le point de terminaison MCP sur `/mcp` pour les agents IA. Définissez `false` pour le désactiver. |
| `VOCAT_PROBE_UNKNOWN_USB` | `false` | Autoriser le sondage par commandes AT des périphériques série USB non reconnus. Le sondage modifie les paramètres série et vide le tampon d'entrée ; activez uniquement si ces ports sont réservés aux modems. Les identités des modems vérifiés sont découvertes automatiquement. Clé JSON : `probe_unknown_usb`. |
| `VOCAT_REPO` | `MengMengCode/VoCat` | Dépôt GitHub de confiance utilisé par l'auto-mise à jour, sous la forme `propriétaire/nom`. |
| `GITHUB_TOKEN` | vide | Jeton GitHub optionnel pour les dépôts privés ou des limites d'API plus élevées. |

Les paquets de configuration opérateur Apple fournis par l'utilisateur peuvent être convertis en profils opérateur examinables et conformes à une liste d'autorisation avec `vocat carrier import-ipcc` ; voir [docs/CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md).

Les identifiants administrateur sont enregistrés uniquement dans SQLite. Initialisez une base de données vide une seule fois avec `vocat bootstrap-admin` ; les variables d'environnement et la configuration JSON ne peuvent pas définir ou écraser le nom d'utilisateur ou le mot de passe administrateur.

Ne stockez pas de jetons Telegram, de mots de passe SMTP, de secrets de webhook, d'identifiants SIM ou d'autres données privées dans le dépôt. Configurez-les via les paramètres de l'application ou des fichiers d'environnement protégés.

## Authentification unique (SSO)

VoCat prend en charge Pocket ID via OpenID Connect, les applications OAuth GitHub.com et les fournisseurs Generic OAuth2. Sélectionnez un fournisseur dans **Paramètres → Authentification unique (SSO)**. Le SSO est désactivé par défaut et la connexion par mot de passe reste disponible. Connectez-vous d'abord avec le compte administrateur local, enregistrez un fournisseur et associez explicitement un compte externe avant d'activer le SSO. Les autres comptes externes ne peuvent pas s'inscrire ni devenir administrateurs, même s'ils partagent la même adresse e-mail que le compte associé.

### Pocket ID

1. Utilisez une origine HTTPS pour VoCat, telle que `https://vocat.example.com`, et définissez `VOCAT_SECURE_COOKIES=true`. Dans les paramètres SSO, choisissez **Pocket ID**. L'origine publique utilise initialement l'adresse actuelle du navigateur ; modifiez le champ visible **Origine publique de VoCat** si nécessaire. L'URL de rappel compacte et le bouton de copie apparaissent immédiatement, ce qui permet de copier l'URL avant de créer le client OIDC ou d'enregistrer des identifiants : `https://vocat.example.com/api/auth/sso/callback`.
2. Dans Pocket ID, créez un client OIDC distinct de type **Confidential** pour VoCat. Enregistrez l'URL de rappel exacte et vérifiez que l'utilisateur ou le groupe ciblé est autorisé à utiliser ce client. VoCat et Komari nécessitent des clients distincts et des sessions locales séparées.
3. Dans VoCat, saisissez l'URL publique exacte (`APP_URL`) de Pocket ID comme Issuer, puis saisissez le Client ID et le Client Secret émis pour le client créé à l'étape 2. Le champ Client ID inclut un rappel de l'emplacement de cette valeur. Les portées (scopes) et PKCE S256 sont gérés par VoCat. L'authentification du client pour le jeton est sélectionnée à partir des métadonnées Discovery validées, en préférant `client_secret_basic` à `client_secret_post`. Enregistrez tout en laissant le SSO désactivé. La modification de l'origine met immédiatement à jour l'URL de rappel affichée ; elle prend effet pour l'autorisation après l'enregistrement.
4. Vérifiez la configuration enregistrée ; en cas de succès, le message **Vérification de la configuration réussie** s'affiche. Associez ensuite le compte. Effectuez la connexion par Passkey et le consentement dans Pocket ID, puis confirmez l'association retournée dans VoCat. La vérification de configuration valide uniquement les métadonnées et la connectivité réseau ; elle ne peut pas vérifier le secret client ni l'autorisation de l'utilisateur sans cette connexion.
5. Activez le SSO et enregistrez. Utilisez une fenêtre de navigation privée pour vérifier la connexion SSO, le retour à la page demandée et la première requête modifiant l'état.

La page des paramètres affiche le compte associé et une action de dissociation. Pour modifier le compte externe, dissociez-le d'abord, puis associez le nouveau compte souhaité et réactivez le SSO.

L'Issuer est comparé de manière stricte, y compris son chemin et la barre oblique finale. La connexion Pocket ID utilise `issuer + sub` du jeton ID validé ; aucun point de terminaison UserInfo ni revendication d'e-mail n'est requis. Discovery et ses points de terminaison d'autorisation, de jeton (Token) et JWKS doivent utiliser l'origine de l'Issuer configuré. Les déploiements utilisant `INTERNAL_APP_URL` avec une origine backend différente ne sont pas pris en charge dans cette version. Conservez l'`APP_URL` publique comme Issuer. Les méthodes d'authentification et les scopes explicitement configurés sont conservés lors de l'enregistrement.

### GitHub et Generic OAuth2

Pour GitHub, créez une application OAuth sur GitHub.com avec exactement la même URL de rappel, sélectionnez **GitHub**, saisissez son Client ID et son Secret, puis suivez la séquence enregistrement, vérification, association et activation ci-dessus. Les points de terminaison sont fixes sur GitHub.com ; la portée par défaut est `read:user` et PKCE S256 est requis. L'association repose sur l'identifiant numérique stable du compte GitHub, ainsi changer de nom d'utilisateur ne modifie pas l'association. GitHub Enterprise ne fait pas partie du périmètre de cette version.

Pour Generic OAuth2, fournissez les URL d'autorisation, de jeton (Token) et UserInfo en plus des identifiants client. Les scopes, le champ d'identifiant utilisateur de premier niveau (par défaut `id`), l'authentification par jeton et les options de compatibilité PKCE sont affichés lorsque Generic OAuth2 est sélectionné. UserInfo est interrogé avec un jeton d'accès Bearer. L'ID doit être une chaîne non vide ou un entier ; les grands identifiants entiers conservent leur valeur exacte. L'authentification par jeton par défaut est `client_secret_post` et PKCE est S256. Désactivez PKCE uniquement pour un fournisseur qui ne le prend pas en charge. Generic OAuth2 n'effectue pas de vérification de jeton ID OIDC.

### Réseau, identifiants et sessions

La politique de réseau source existante de VoCat s'applique également aux routes SSO. La configuration du SSO ne rend pas une installation accessible publiquement. Le navigateur doit pouvoir joindre VoCat et le point de terminaison d'autorisation ; le processus VoCat doit pouvoir joindre Discovery, les points de terminaison Token et JWKS/UserInfo avec des certificats TLS de confiance. L'enregistrement de l'Issuer HTTPS exact de Pocket ID ou des URL de Generic approuve ces adresses pour les connexions HTTPS, y compris les adresses privées RFC1918/IPv6 ULA. Les points de terminaison fixes de GitHub restent restreints aux adresses publiques.

Pour le service HTTP interne d'un fournisseur Generic, saisissez explicitement son URL HTTP complète dans le champ Token/UserInfo. Chaque candidat DNS et connexion réelle vers ces destinations HTTP doit être une adresse privée autorisée. Discovery de Pocket ID ne peut pas approuver une autre origine ; ses points de terminaison doivent utiliser l'origine de l'Issuer configuré. L'Issuer, l'URL de récupération Discovery et le point de terminaison d'autorisation du navigateur exigent toujours HTTPS. Les adresses de bouclage (loopback), lien local (link-local), métadonnées cloud et autres adresses spéciales restent bloquées pour les connexions IdP. Installez les certificats CA privés dans le magasin de confiance de l'hôte/du conteneur ; il n'y a pas d'option pour contourner la vérification TLS. L'accès du navigateur à VoCat via localhost ou HTTPS est indépendant de ces autorisations de connexion IdP.

Le déploiement Compose fourni utilise le réseau hôte. Vérifiez le DNS, le routage et la confiance CA depuis le conteneur lui-même ; ne supposez pas que les noms de services en réseau ponté (bridge) sont disponibles. Les proxys inverses doivent conserver l'origine publique configurée. VoCat ne déduit pas les URL de rappel à partir des en-têtes host transférés. Un déploiement à la racine est requis. Pour le développement local Vite, utilisez une origine de bouclage cohérente, telle que `http://127.0.0.1:5173`, pour l'origine publique et l'enregistrement de rappel ; les exceptions de transport IdP ne doivent être utilisées que dans les fixtures de test.

Les cookies de session et CSRF conservent `SameSite=Strict` ; les cookies d'autorisation éphémères utilisent `SameSite=Lax` pour les rappels GET de premier niveau. L'enregistrement de toute configuration SSO, y compris sa désactivation ou la rotation du secret, révoque toutes les sessions SSO. La dissociation d'un compte révoque les sessions SSO associées à cette liaison. Les sessions par mot de passe et les jetons d'API subsistent après ces actions. La modification du mot de passe de l'administrateur local révoque toutes les sessions et tous les jetons d'API, annule les autorisations en cours et conserve l'association externe. L'interface demande une nouvelle connexion lorsqu'une opération réussie révoque la session SSO actuelle.

Laissez le champ du secret vide pour conserver un secret existant, ou entrez un nouveau secret pour le remplacer. La page des paramètres ne propose pas de commande pour effacer un secret enregistré. La modification du fournisseur, du Client ID, de l'Issuer ou des points de terminaison/champ d'ID de Generic bascule l'espace de noms d'identité, exige de nouveaux identifiants et une nouvelle association ; les associations existantes nécessitent une confirmation explicite de réinitialisation. Pour modifier le compte associé, dissociez-le d'abord puis créez une nouvelle association. Les secrets clients sont conservés uniquement sur le serveur dans la base de données SQLite protégée ; l'indicateur de paramètre sensible masque les réponses et ne chiffre pas la base de données. Les jetons tiers ne sont pas persistés ni renvoyés au navigateur.

La déconnexion standard met fin uniquement à la session VoCat en cours. La désactivation d'un compte IdP ou la déconnexion de Pocket ID ne révoque pas immédiatement une session VoCat existante ; elle expire selon `VOCAT_SESSION_TTL` ou peut être révoquée en désactivant le SSO ou en dissociant le compte dans VoCat. Le point de terminaison public des fonctionnalités est `GET /api/auth/providers`. Les API de configuration et de liaison SSO nécessitent une session de navigateur et CSRF pour les écritures ; les jetons Bearer ne peuvent pas les gérer. Les conflits de révision de configuration renvoient `409 sso_config_changed` ; les changements de source d'identité renvoient `409 sso_rebind_required`.

### Mise à niveau et restauration

Le SSO introduit la version 29 du schéma SQLite. Les mises à niveau conservent l'administrateur existant, les sessions, les jetons d'API et les données d'appareils ; le SSO reste désactivé tant qu'il n'est pas configuré. Avant la mise à niveau, notez l'ancienne version binaire ou le digest de l'image, et sauvegardez la configuration, les certificats CA et le répertoire de données persistant complet pendant que VoCat est arrêté, ou utilisez l'API de sauvegarde SQLite / la commande `.backup` pour obtenir un instantané cohérent à chaud. La simple copie d'un fichier de base de données en mode WAL en cours d'exécution n'est pas une sauvegarde fiable.

Désactivez le SSO dans la nouvelle version pour résoudre d'éventuels problèmes de fournisseur tout en conservant la connexion par mot de passe et les données métier actuelles. Le retour à un binaire avec le schéma 28 nécessite d'arrêter VoCat et de restaurer l'instantané de données et de configuration antérieur correspondant. La simple restauration de l'image du conteneur ou la diminution de `PRAGMA user_version` ne peut pas rétrograder la base de données. Restaurez si possible dans un répertoire/volume distinct et conservez la base de données la plus récente à des fins d'analyse. La restauration d'un instantané entraîne la perte des SMS et des modifications de tâches enregistrés après cette sauvegarde. Pour Docker, mettez à jour en tirant la nouvelle image et en recréant le conteneur.

Avant un déploiement en production, effectuez le test réel Passkey/politique d'accès Pocket ID et une répétition de la mise à niveau et de la restauration.

## Jetons d'API et MCP

Les clients autres que les navigateurs s'authentifient auprès des mêmes points de terminaison `/api/...` à l'aide d'un jeton Bearer au lieu du cookie de session et du flux CSRF du navigateur :

```bash
vocat token create --read-only --ttl 720h ci-runner   # affiche le jeton une seule fois
curl -H "Authorization: Bearer vcat_..." http://127.0.0.1:7575/api/devices
```

`vocat token list` et `vocat token revoke <name>` gèrent l'inventaire. Les jetons disposent des privilèges administrateur ; un jeton `--read-only` ne peut exécuter que des requêtes `GET` (une exception documentée : la lecture d'un fil de SMS le marque localement comme lu — la règle étant « aucune modification matérielle ou réseau »). La rotation du mot de passe administrateur révoque tous les jetons. Créez des jetons après le premier `bootstrap-admin`. Les clients distants utilisant Bearer ont également besoin d'une politique d'accès réseau autorisant leur adresse source ; le mode par défaut `internal` ne dessert que les réseaux privés.

Par défaut (définir `VOCAT_MCP_ENABLED=false` pour le désactiver), le même service expose un point de terminaison MCP (Model Context Protocol) pour les agents IA sur `http://<host>:7575/mcp` en utilisant le transport Streamable HTTP et l'authentification par jeton Bearer. Les outils couvrent l'état des appareils, les fils de SMS et l'envoi sécurisé, la liste et la commutation des profils eSIM, ainsi que l'état et la reconnexion WiFi Calling ; les opérations sensibles (commandes AT directes, téléchargement/suppression d'eSIM, code PIN de la SIM, sélection de réseau, mise à jour du système) ne sont pas exposées, et la sortie des outils n'inclut jamais l'IMSI, le code PIN de la SIM ou les codes d'activation. Un jeton en lecture seule reçoit une liste d'outils dépourvue des outils d'écriture. L'envoi de SMS suit la même politique de numéros bloqués et la même limite horaire globale que l'interface Web, et s'effectue via le chemin AT cellulaire (le routage préférentiel IMS de l'interface Web lorsque WiFi Calling est activé reste côté Web, et les messages envoyés via MCP ne sont pas encore consignés dans l'historique des conversations Web).

Exemple de configuration client (avec Claude Code) :

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

Consultez [docs/API_MCP_DEVELOPMENT_PLAN.md](API_MCP_DEVELOPMENT_PLAN.md) pour le document de conception.

## Bot Telegram

Lorsque les notifications Telegram sont activées et que le Chat ID et l'Admin ID sont configurés, le bot prend en charge :

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

La commutation de profil et l'envoi de SMS utilisent des boutons de confirmation à usage unique. Le bot n'expose pas de commandes de téléchargement, suppression ou renommage d'eSIM.

Le terminal web et Telegram partagent le routage USSD. Avec WiFi Calling activé, les requêtes utilisent IMS/USSI et renvoient une erreur d'attente jusqu'à l'enregistrement IMS, sans basculer sur AT+CUSD cellulaire. Avec WiFi Calling désactivé, les requêtes utilisent l'USSD cellulaire. Les réponses aux menus et les annulations restent liées à l'appareil et au dialogue d'origine. Les erreurs d'annulation sont signalées et conservent la session pour une nouvelle tentative ; un envoi incertain n'est jamais réémis automatiquement sur un autre canal.

La gestion des dialogues USSI est conforme à la norme [3GPP TS 24.390](https://www.etsi.org/deliver/etsi_ts/124300_124399/124390/18.00.00_60/ts_124390v180000p.pdf). Un échec de BYE conserve un enregistrement d'annulation, mais toute saisie supplémentaire dans le menu est bloquée dès que l'annulation est initiée ([RFC 3261, section 15.1.1](https://www.rfc-editor.org/rfc/rfc3261.html#section-15.1.1)).

## Mise à jour

Vérifier la présence d'une version GitHub Release plus récente :

```bash
vocat update --check --repo MengMengCode/VoCat
```

Installer la dernière version :

```bash
sudo vocat update --repo MengMengCode/VoCat
```

Le programme de mise à jour télécharge le binaire correspondant à l'architecture Linux actuelle, le vérifie avec le fichier `SHA256SUMS` publié, remplace l'exécutable de manière atomique et redémarre le service systemd `vocat` lorsqu'il est disponible.

Pour les installations Docker :

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

Recréez le conteneur après avoir téléchargé la nouvelle image.

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

Compiler le frontend intégré et démarrer le backend :

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

Compiler un binaire de production :

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## Automatisation des versions

Pousser un tag de version déclenche deux flux de travail GitHub Actions :

- `release-binaries` compile et publie les binaires `amd64`, `386`, `arm64`, `aarch64` et `armv7` ainsi que `SHA256SUMS`.
- `docker` compile et publie une image multi-architecture sur GitHub Container Registry.

```bash
git tag v0.2.0
git push origin v0.2.0
```

## Structure du projet

```text
cmd/vocat/                  Point d'entrée de l'application et CLI
internal/device/            Découverte de modems et contrôle des appareils
internal/modem/             Session AT et traitement des réponses
internal/server/            API HTTP, notifications et serveur web intégré
internal/store/             Persistance SQLite
internal/update/            Auto-mise à jour via GitHub Release
internal/vowifi/            Environnement d'exécution IKE, EAP-AKA, IMS et WiFi Calling
scripts/install.sh          Script d'installation et de mise à jour Linux
web/src/                    Frontend React et TypeScript
.github/workflows/          Automatisation des versions binaires et Docker
```

## Utilisation responsable

Les opérations sur les modems cellulaires et les eSIM peuvent affecter le service abonné, les profils enregistrés, l'enregistrement réseau et l'état du matériel. Conservez des sauvegardes, examinez attentivement les actions destructrices et n'utilisez le logiciel que dans des environnements légitimes où vous êtes autorisé à exploiter le matériel et les ressources réseau connectés.

Vocat ne contourne pas l'authentification de l'opérateur, les politiques réseau, la sécurité matérielle ou les exigences de confiance eSIM. La prise en charge d'une opération signifie que Vocat peut la demander au modem ou à l'eUICC ; l'appareil, le profil, le réseau ou l'opérateur peuvent toujours la refuser.

## Contribution

Les issues et pull requests sont les bienvenues. Gardez les modifications ciblées, incluez des tests lorsque c'est faisable, évitez d'enregistrer des identifiants ou des données d'abonnés et documentez clairement les comportements spécifiques au matériel.

Avant de soumettre une modification :

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
| USDT-TRON (TRC20) | `TWSAkvzVsFc7KqncDLmUfRxpPQbpV5CgTB` |
| USDT-BSC (BEP20) | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |
| USDT-Polygon | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |

## Licence

Voir [LICENSE](../LICENSE).

## Star History

<a href="https://www.star-history.com/?repos=mengmengcode%2Fvocat&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
 </picture>
</a>\n
