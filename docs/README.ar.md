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

[English](../README.md) | **العربية** | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [Français](README.fr.md) | [Русский](README.ru.md) | [Español](README.es.md) | [日本語](README.ja.md)

Vocat هي لوحة تحكم ويب مفتوحة المصدر ومجموعة أدوات هندسية لمودمات Quectel الخلوية من فئة EC20/EC25 وسلسلة China Mobile ML307. تجمع في خدمة واحدة مكتفية ذاتيًا بين اكتشاف المودم، وحالة الراديو المباشرة، وطرفيات AT وUSSD، والرسائل القصيرة SMS، وWiFi Calling، وإدارة eSIM، واختيار الشبكة، والتوجيه عبر البروكسي، والإشعارات، وسجلات التدقيق، وأتمتة الإصدارات.

الواجهة الخلفية مكتوبة بلغة Go، والواجهة مبنية باستخدام React وTypeScript، وتُضمَّن واجهة الإنتاج الأمامية داخل الملف الثنائي لـ Go. يحتوي ملف تنفيذي واحد على تطبيق الويب ويستخدم SQLite للحالة الدائمة.

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## الميزات

| المجال | ما يوفره Vocat |
| --- | --- |
| إدارة الأجهزة | اكتشاف تلقائي عبر المنفذ التسلسلي/USB، دعم عدة مودمات، أسماء أجهزة مألوفة، تحديثات مباشرة للنظرة العامة، إعادة تشغيل الوحدة، وضع الطيران، وضوابط وضع شبكة USB. |
| الراديو والشبكة | حالة التسجيل، المشغّل، مقاييس الإشارة، RSRP/RSRQ/SINR، وضع الشبكة، النطاق، القناة، فحص المشغّلين، الاختيار التلقائي أو اليدوي للشبكة، واستعلام خلايا LTE مع قفل/إلغاء قفل خلية واحدة (EARFCN/PCI). |
| AT وUSSD | طرفية AT تفاعلية، سجل الأوامر، استجابات المودم الخام، تدفقات بدء/متابعة/إلغاء USSD، والإبلاغ الواضح عن أخطاء المودم. |
| الرسائل القصيرة | إرسال مباشر للرسائل الخلوية ورسائل IMS، المزامنة الواردة، التعامل مع الرسائل متعددة الأجزاء، تقارير التسليم، سجل المحادثات، حالة عدم القراءة، الطوابع الزمنية، وحالة التسليم لكل رسالة. |
| WiFi Calling | إنشاء نفق IKEv2/ePDG، مصادقة EAP-AKA، تسجيل IMS، رسائل IMS القصيرة، ضوابط إعادة الاتصال، تشخيصات الحالة، والتوجيه لكل جهاز. |
| eSIM وeUICC | اكتشاف eUICC، ومعلومات EID والإنتاج، والبيانات الوصفية للشهادات، وجرد متعدد لـ eUICC، وقائمة الملفات الشخصية المثبتة، وعمليات التمكين/التعطيل/التبديل، وعمليات التنزيل وإعادة التسمية والحذف عندما تدعمها البطاقة. |
| سياسة البطاقة | سلوك WiFi Calling ووضع الطيران بناءً على ICCID مع تطبيق فوري للسياسة. |
| التوجيه عبر البروكسي | توجيه SOCKS صاعد، ربط الأجهزة، قواعد الدول، فحوصات الوصول عبر TCP، وفحوصات UDP Associate لمسارات بيانات WiFi Calling. |
| الإشعارات | إعادة توجيه الرسائل القصيرة الواردة الجديدة عبر Telegram وBark والبريد الإلكتروني وPushplus وwebhooks الموقّعة. يتم تسليم كل رسالة كإشعار منفصل. |
| بوت Telegram | حالة الجهاز، قائمة الملفات الشخصية المثبتة وتبديلها، ضوابط WiFi Calling، وإرسال الرسائل القصيرة. تتطلب الإجراءات الحساسة تأكيد المسؤول. |
| العمليات | المصادقة بكلمة المرور وبـ SSO (مثل Pocket ID وGitHub وGeneric OAuth2)، الحماية من CSRF، سياسات الوصول، أحداث التدقيق، السجلات المباشرة، الاحتفاظ بالسجلات، فحوصات الصحة، تخطيط متجاوب، الوضع الداكن، وواجهة مستخدم بالإنجليزية/الصينية. |
| التوزيع | ملفات Linux الثنائية الثابتة، سكربت تثبيت systemd، تحديث ذاتي مع التحقق من SHA-256، صورة Docker، النشر إلى GHCR، وبنى إصدارات GitHub Actions. |

## الأجهزة المدعومة

يستهدف Vocat وحدات Quectel المبنية على Qualcomm والتي توفر واجهات AT وQMI والمنفذ التسلسلي وشبكة USB المتوافقة، بما في ذلك:

- Quectel EC20
- Quectel EC25
- عائلة Quectel EG25
- وحدات EG600 المتوافقة وذات الصلة
- سلسلة China Mobile ML307 (مثل ML307A-E5 ومختلف المصنّعين)

تعتمد الميزات المتاحة على برنامج الوحدة الثابت (firmware)، وتكوين USB، وقدرات SIM/eSIM، وتعريفات المضيف، والشبكة اللاسلكية، وإعدادات المشغّل.

## التثبيت

### تثبيت Linux بنقرة واحدة

بصفتك root (بما في ذلك OpenWrt/Kwrt، حيث يكون `sudo` غير موجود عادةً):

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash
```

من مستخدم عادي على توزيعة تحتوي على sudo:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

تحقق من متطلبات VoWiFi/XFRM على المضيف دون تثبيت VoCat:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash -s -- --check-env
```

تثبيت إصدار محدد:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

يتطلب VoWiFi IMS وجود Linux XFRM/IPsec. على OpenWrt/Kwrt يحاول المثبّت
تثبيت الحزم المطابقة `ip-full` و`kmod-ipsec` و`kmod-ipsec4/6`
و`kmod-crypto-authenc` وAES-CBC وSHA1 من مستودع البرنامج الثابت نفسه.
إذا لم تكن وحدات النواة المطابقة متاحة، فاستخدم برنامجًا ثابتًا يتضمنها؛
ولا تفرض أبدًا تثبيت kmods مبنية لنواة مختلفة.

إذا كانت النواة لا تستطيع توفير XFRM/IPsec وكنت تحتاج فقط إلى ميزات لا تعتمد على VoWiFi، مثل الرسائل القصيرة الخلوية أو البيانات، فثبّت باستخدام `--skip-vowifi-check`:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh --skip-vowifi-check
```

المثبّت:

- يكتشف `amd64` أو `386` أو `arm64` أو `aarch64` أو `armv7`؛
- ينزّل الملف الثنائي المطابق من GitHub Release؛
- يتحقق منه مقابل `SHA256SUMS`؛
- يثبّت Vocat في `/opt/vocat`؛
- ينشئ خدمة systemd محصّنة بوصول الأجهزة والشبكة الذي يتطلبه Vocat؛
- يخزّن إعدادات وقت التشغيل في `/etc/vocat/env`؛
- يولّد كلمة مرور مسؤول أولية عشوائية عند التثبيت الأول.

بعد التثبيت، افتح:

```text
http://<server-address>:7575
```

### التثبيت اليدوي للملف الثنائي

نزّل الملف الثنائي المطابق و`SHA256SUMS` من GitHub Releases:

| المنصة | ملف الإصدار |
| --- | --- |
| Linux x86-64 | `vocat-linux-amd64` |
| Linux x86 32-بت | `vocat-linux-386` |
| Linux ARM64 | `vocat-linux-arm64` |
| Linux AArch64 | `vocat-linux-aarch64` |
| Linux ARMv7 | `vocat-linux-armv7` |

تحقق منه وثبّته:

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

يشغّل هذا الأمر اليدوي Vocat في المقدمة. استخدم `vocat serve` حتى
تبدأ العملية تشغيل الخادم مباشرةً؛ إن تشغيل `vocat` دون وسائط بصفتك root
على TTY يفتح بدلاً من ذلك قائمة الإدارة التفاعلية. استخدم المثبّت بنقرة
واحدة عند الحاجة إلى خدمة systemd مُدارة وإعادة تشغيل تلقائية.

### Docker

لمضيف Linux الذي يجب أن يكتشف كل مودم Quectel مدعوم متصل ويواصل
رؤية أحداث التوصيل الساخن لـ USB، شغّل Vocat في وضع الوصول إلى الأجهزة:

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

افتح `http://<server-address>:7575` بعد بدء الحاوية. يلزم استخدام شبكة المضيف حتى تبقى واجهات شبكة QMI مرئية لـ Vocat، كما يلزم الوصول بصلاحيات مرتفعة إلى الأجهزة لاستخدام المنافذ التسلسلية وعقد تحكم QMI وواجهات TUN وإعدادات الشبكة والأجهزة المضافة بعد بدء الحاوية. يتيح الربط المباشر لـ `/dev` ظهور عقد `ttyUSB*` و`ttyACM*` و`cdc-wdm*` الجديدة وعقد MHI من نمط `wwan*` دون إعادة إنشاء الحاوية.

يمنح هذا الوضع Vocat عمدًا وصولًا واسعًا إلى أجهزة المضيف ومكدس الشبكة. استخدمه فقط على مضيف Linux موثوق. يتعرّف الاكتشاف التلقائي على مودمات Quectel USB المدعومة (معرّف مورّد USB هو `2c7c`)، ووحدات سلسلة China Mobile ML307 عبر سلسلة تعريف المنتج، ومودمات PCIe/MHI التي يتيحها نظام Linux WWAN الفرعي؛ ولا يتعرّف على جميع ترتيبات المودمات الممكنة. إن تعيين عقد فردية فقط باستخدام `--device`، مثل `/dev/ttyUSB2` أو `/dev/cdc-wdm0` أو `/dev/wwan0qmi0`, يحصر الحاوية في تلك العقد الثابتة ولا يوفّر اكتشافًا كاملًا لعدة أجهزة أو للأجهزة الموصولة أثناء التشغيل.

على مضيفي OpenWrt، ترفض سلسلة input في fw4 الاتصالات الجديدة من الواجهات التي لا تديرها، بما في ذلك واجهة TUN الخاصة بـ VoWiFi لكل جلسة والتي ينشئها Vocat. لذلك، يُدرج Vocat قواعد قبول مستهدفة باسم `vocat-ims-*` (من P-CSCF إلى العنوان الداخلي المتفاوض عليه) باستخدام ثنائي `nft` المضمّن في الصورة. بدون هذه القواعد، يعمل تسجيل IMS والرسائل القصيرة الصادرة، ولكن تُسقط الرسائل والمكالمات الواردة. إذا لم يتمكن ثنائي `nft` المضمّن من فحص حالة قواعد المضيف، يسجّل Vocat الملاحظة `nft could not inspect the OpenWrt fw4 input chain`؛ وجّه `VOCAT_NFT` حينئذٍ إلى nft الخاص بالمضيف:

```bash
cat > /opt/vocat-host-nft <<'EOF'
#!/bin/sh
exec /host/lib/ld-musl-$(uname -m).so.1 --library-path /host/usr/lib:/host/lib /host/usr/sbin/nft "$@"
EOF
chmod 755 /opt/vocat-host-nft
# أضف إلى أمر docker run أعلاه:
#   -v /usr:/host/usr:ro -v /lib:/host/lib:ro \
#   -v /opt/vocat-host-nft:/usr/local/bin/host-nft:ro \
#   -e VOCAT_NFT=/usr/local/bin/host-nft
```

تُنشر صورة GHCR لـ `linux/amd64` و`linux/arm64`.

> [!TIP]
> **ملاحظة النشر على NAS / QNAP Container Station**:
> في أنظمة تشغيل NAS مثل QNAP QTS / QuTS hero (Container Station)، قد تؤدي حسابات المسؤولين المخصصة التي لا تعمل بصلاحيات root وآليات عزل وحدات التخزين إلى إسناد وحدات تخزين Docker المسماة (مثل `-v vocat-data:/opt/vocat/data`) إلى مسارات معزولة مختلفة بين التهيئة لمرة واحدة باستخدام `bootstrap-admin` وحاوية الخدمة العاملة في الخلفية، مما يسبب أخطاء «كلمة المرور غير صحيحة» عند تسجيل الدخول عبر الويب.
> في بيئات NAS، يوصى بشدة باستبدال وحدات التخزين المسماة بربط مباشر لمسار مطلق على المضيف (مثل `-v /share/Container/vocat/data:/opt/vocat/data` على QNAP) لكل من التهيئة والتشغيل، لضمان اتساق التخزين الدائم لقاعدة بيانات SQLite.

### قارئات SIM عبر USB

تستخدم قارئات SIM عبر USB خدمة Linux PC/SC. عند استخدام مدير حزم مدعوم، يثبّت برنامج التثبيت بنقرة واحدة `pcscd` ويشغّله تلقائيًا، كما يثبّت برنامج تشغيل CCID. على Debian/Ubuntu، يكون الإعداد اليدوي المكافئ هو `apt install pcscd libccid`. إذا اكتشف USB قارئ CCID ولكن PC/SC غير متاح، يُبقي VoCat القارئ ظاهرًا في مربع حوار إضافة جهاز ويبلّغ عن الخدمة أو برنامج التشغيل المفقود بدلًا من إخفائه بصمت.

### أدوات سطر أوامر QMI

يستخدم VoCat الأداة `qmicli` للتحقق من جاهزية قناة تحكم QMI، و`qmi-proxy` لتعدد الإرسال في الوصول إليها. تُدار جلسات بيانات الحزم بواسطة عميل QMI WDS المدمج بدلًا من ملفات حالة CID/PDH الخاصة بـ `qmi-network`. يثبّت برنامج التثبيت بنقرة واحدة الأدوات المقابلة ويتحقق منها. للنشر اليدوي، يستخدم Debian/Ubuntu الأمر `apt install libqmi-utils`، ويستخدم Arch Linux الأمر `pacman -S libqmi`، ويستخدم Alpine الأمر `apk add qmi-utils`، ويستخدم OpenWrt الأمر `opkg install qmi-utils`.

يتحقق `vocat doctor --repair-dji-qmi` من وجود `qmicli` قبل تغيير أي ربط لبرنامج تشغيل USB أو تفعيل إشارة DTR. إذا كانت الأداة غير متاحة، يتوقف الأمر مع إرشاد لتثبيتها ويترك حالة الجهاز الحالية دون تغيير.

### بيانات مضيف خلوية عبر AT

تتطلب بيانات ECM/RNDIS المُدارة عبر AT وجود Linux وiproute2 وBusyBox مع `udhcpc`.
يجب أن يدير VoCat واجهة شبكة المودم بشكل حصري؛ عطّل عملاء DHCP أو ملفات تعريف NetworkManager المتعارضة لتلك الواجهة. يحتفظ VoCat بعقد إيجار IPv4 ومسارات سياسة مُعلّمة دون تعديل التوجيه الافتراضي للنظام أو إعدادات محلل DNS. يتطلب هذا المسار حاليًا خدمة خلوية تدعم IPv4.

## الإعدادات

يقرأ Vocat ملف إعدادات JSON اختياريًا من `VOCAT_CONFIG`، ثم يطبق متغيرات البيئة `VOCAT_*`. متغيرات البيئة لها الأولوية.

| متغير البيئة | الافتراضي | الوصف |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | عنوان الاستماع HTTP. |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | مسار قاعدة بيانات SQLite. |
| `VOCAT_SESSION_TTL` | `24h` | مدة صلاحية جلسة المصادقة. |
| `VOCAT_SECURE_COOKIES` | `false` | يضع علامة آمنة على ملفات تعريف ارتباط الجلسة عند استخدام HTTPS. |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | مهلة الإيقاف السلس. |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | الحد الأقصى لحجم جسم طلب API. |
| `VOCAT_MCP_ENABLED` | `true` | إتاحة نقطة نهاية MCP عند `/mcp` لوكلاء الذكاء الاصطناعي. عيّن `false` للتعطيل. |
| `VOCAT_PROBE_UNKNOWN_USB` | `false` | تمكين الفحص الاستدلالي باستخدام أوامر AT على أجهزة USB التسلسلية غير المعروفة. يغير الفحص إعدادات المنفذ التسلسلي ويمسح الإدخال؛ لا تفعّله إلا عندما تكون تلك المنافذ مخصصة للمودمات. تُكتشف هويات المودم المؤكدة تلقائيًا. مفتاح JSON: `probe_unknown_usb`. |
| `VOCAT_REPO` | `MengMengCode/VoCat` | مستودع GitHub الموثوق الذي يستخدمه المحدّث الذاتي، بصيغة `owner/name`. |
| `GITHUB_TOKEN` | فارغ | رمز GitHub اختياري للمستودعات الخاصة أو حدود API أعلى. |

يمكن تحويل حزم إعدادات مشغّلي Apple التي يوفّرها المستخدم باستخدام `vocat carrier import-ipcc` إلى ملفات إعداد مشغّلين قابلة للمراجعة ومقيّدة بقائمة عناصر مسموح بها؛ انظر [docs/CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md).

تُخزَّن بيانات اعتماد المسؤول في SQLite فقط. هيّئ قاعدة بيانات فارغة مرة واحدة باستخدام `vocat bootstrap-admin`؛ لا يمكن لمتغيرات البيئة أو إعدادات JSON تعيين اسم مستخدم المسؤول أو كلمة مروره أو الكتابة فوقهما.

لا تخزّن رموز Telegram، أو كلمات مرور SMTP، أو أسرار webhook، أو بيانات اعتماد SIM، أو بيانات خاصة أخرى في المستودع. قم بإعدادها عبر إعدادات التطبيق أو ملفات البيئة المحمية.

## الدخول الموحد (SSO)

يدعم VoCat كلاً من Pocket ID عبر OpenID Connect، وتطبيقات OAuth على GitHub.com، ومزوّدي Generic OAuth2. اختر مزودًا واحدًا في **الإعدادات → الدخول الموحد (SSO)**. يبدأ SSO معطلاً ويبقى تسجيل الدخول بكلمة المرور متاحًا. سجّل الدخول بالمسؤول المحلي أولاً، واحفظ المزود، واربط حسابًا خارجيًا بشكل صريح قبل تمكين SSO. لا يمكن للحسابات الخارجية الأخرى التسجيل أو أن تصبح مسؤولة، بما في ذلك الحسابات التي تشترك في عنوان البريد الإلكتروني للحساب المرتبط.

### Pocket ID

1. استخدم أصل HTTPS لـ VoCat، مثل `https://vocat.example.com`، واضبط `VOCAT_SECURE_COOKIES=true`. في إعدادات SSO، اختر **Pocket ID**. يستخدم الأصل العام في البداية عنوان المتصفح الحالي؛ عدّل حقل **أصل VoCat العام** المرئي إذا لزم الأمر. يظهر عنوان URL المدمج لرد النداء وزر النسخ على الفور، مما يتيح نسخ الرابط قبل إنشاء عميل OIDC أو حفظ أي بيانات اعتماد: `https://vocat.example.com/api/auth/sso/callback`.
2. في Pocket ID، أنشئ عميل OIDC **سري** (Confidential) منفصلاً لـ VoCat. سجّل عنوان URL المطابق تمامًا لرد النداء وتحقق من السماح للمستخدم أو المجموعة المحددة باستخدام هذا العميل. يحتاج كل من VoCat وKomari إلى عملاء منفصلين وجلسات محلية مستقلة.
3. في VoCat، أدخل `APP_URL` العام الدقيق لـ Pocket ID كـ Issuer، وأدخل معرّف العميل (Client ID) وسر العميل (Client Secret) الصادرين للعميل المنشأ في الخطوة 2. يتضمن حقل Client ID تذكيرًا بمكان العثور على هذه القيمة. يتولى VoCat إدارة النطاقات (Scopes) وPKCE S256. يتم اختيار مصادقة الرمز المميز من بيانات تعريف Discovery التي تم التحقق منها، مع تفضيل `client_secret_basic` على `client_secret_post`. احفظ الإعدادات مع إبقاء SSO معطلاً. تؤدي تعديلات الأصل إلى تحديث عنوان رد النداء المعروض فورًا؛ وتسري لأغراض التفويض بعد الحفظ.
4. افحص الإعدادات المحفوظة؛ عند النجاح تظهر رسالة **اجتاز فحص الإعدادات**. ثم اربط الحساب. أتمم تسجيل الدخول عبر مفتاح المرور (Passkey) والموافقة في Pocket ID، وأكد الربط المُعاد في VoCat. يتحقق فحص الإعدادات من بيانات التعريف والاتصال؛ ولا يمكنه التحقق من سر العميل أو تفويض المستخدم بدون تسجيل الدخول هذا.
5. فعّل SSO واحفظ. استخدم نافذة متصفح خاصة للتحقق من تسجيل الدخول عبر SSO، والعودة إلى الصفحة المطلوبة، وأول طلب يغير الحالة.

تعرض صفحة الإعدادات الحساب المرتبط وخيارًا لفك الارتباط. لتغيير الحساب الخارجي، فك ارتباطه أولاً، ثم اربط الحساب المطلوب وفعّل SSO مجددًا.

تتم مطابقة Issuer بدقة تامة، بما في ذلك مساره والشرطة المائلة في نهايته. يستخدم تسجيل الدخول عبر Pocket ID القيمة `issuer + sub` من رمز ID المميز الموثوق؛ ولا يلزم وجود نقطة نهاية UserInfo أو مطالبة البريد الإلكتروني. يجب أن تستخدم نقطة نهاية Discovery ونقاط نهاية التفويض والرمز المميز وJWKS أصل الـ Issuer المضبوط. عمليات النشر التي تستخدم `INTERNAL_APP_URL` مع أصل خلفي مختلف غير مدعومة في هذا الإصدار. احتفظ بـ `APP_URL` العام كـ Issuer. يتم الاحتفاظ بطرق المصادقة والنطاقات المضبوطة صراحةً عند الحفظ.

### GitHub و Generic OAuth2

بالنسبة إلى GitHub، أنشئ تطبيق OAuth على GitHub.com بنفس عنوان URL الدقيق لرد النداء، واختر **GitHub**، وأدخل معرّف العميل وسره، واتبع تسلسل الحفظ والفحص والربط والتمكين الموضح أعلاه. نقاط النهاية ثابتة على GitHub.com؛ النطاق الافتراضي هو `read:user`، ويلزم استخدام PKCE S256. يعتمد الربط على المعرّف العددي الثابت لحساب GitHub، لذا فإن تغيير اسم تسجيل الدخول لا يغير الحساب. لا يقع GitHub Enterprise ضمن نطاق هذا الإصدار.

بالنسبة إلى Generic OAuth2، قدّم عناوين URL الخاصة بالتفويض والرمز المميز (Token) وUserInfo بالإضافة إلى بيانات اعتماد العميل. تظهر النطاقات، وحقل معرّف المستخدم عالي المستوى (الافتراضي `id`)، ومصادقة الرمز المميز، وخيارات توافق PKCE عند اختيار Generic OAuth2. يُطلب UserInfo باستخدام رمز وصول Bearer. يجب أن يكون المعرّف سلسلة غير فارغة أو عددًا صحيحًا؛ وتحتفظ المعرّفات الصحيحة الكبيرة بقيمتها الدقيقة. مصادقة الرمز المميز الافتراضية هي `client_secret_post` وPKCE هو S256. لا تعطّل PKCE إلا لمزود لا يدعمه. لا يقوم Generic OAuth2 بالتحقق من رمز OIDC ID المميز.

### الشبكة وبيانات الاعتماد والجلسات

تنطبق أيضًا سياسة شبكة المصدر الحالية في VoCat على مسارات SSO. إن ضبط SSO لا يجعل التثبيت متاحًا للعامة. يجب أن يتمكن المتصفح من الوصول إلى VoCat ونقطة نهاية التفويض؛ ويجب أن تتمكن عملية VoCat من الوصول إلى نقاط نهاية Discovery والرمز المميز وJWKS/UserInfo بشهادات TLS موثوقة. يؤدي حفظ الـ Issuer الدقيق عبر HTTPS لـ Pocket ID أو عناوين نقاط النهاية لـ Generic إلى الموافقة على تلك العناوين لاتصالات HTTPS، بما في ذلك العناوين الخاصة RFC1918/IPv6 ULA. تظل نقاط نهاية GitHub الثابتة مقتصرة على العناوين العامة.

بالنسبة لخدمة HTTP الداخلية لمزود Generic، أدخل صراحة عنوان URL الكامل عبر HTTP في حقل Token/UserInfo. يجب أن يكون كل عنوان DNS مرشح واتصال فعلي لهذه الوجهات عبر HTTP عنوانًا خاصًا مسموحًا به. لا يمكن لـ Discovery في Pocket ID الموافقة على أصل آخر؛ يجب أن تستخدم نقاط نهايته أصل الـ Issuer المضبوط. يتطلب دائمًا الـ Issuer، وعنوان جلب Discovery، ونقطة نهاية تفويض المتصفح استخدام HTTPS. تظل عناوين الاسترجاع (loopback) وعناوين الربط المحلي (link-local) والبيانات الوصفية السحابية والوجهات ذات الاستخدام الخاص الأخرى محظورة لاتصالات IdP. ثبّت شهادات CA الخاصة في مخزن الثقة بالمضيف/الحاوية؛ ولا يوجد خيار لتجاوز التحقق من TLS. يُعد وصول المتصفح إلى VoCat عبر localhost أو HTTPS مستقلاً عن أذونات اتصال IdP هذه.

يستخدم نشر Compose المقدّم شبكة المضيف. تحقق من DNS والتوجيه وثقة CA من داخل الحاوية الفعلية؛ ولا تفترض أن أسماء خدمات شبكة bridge متاحة. يجب أن تحافظ الخوادم الوكيلة العكسية (reverse proxies) على الأصل العام المضبوط. لا يشتق VoCat عناوين URL لرد النداء من ترويسات المضيف المُعاد توجيهها. يلزم النشر على مسار الجذر. للتطوير المحلي عبر Vite، استخدم أصل استرجاع متسقًا، مثل `http://127.0.0.1:5173`، للأصل العام وتسجيل رد النداء؛ وتقتصر استثناءات نقل IdP على بيئات الاختبار فقط.

تحتفظ ملفات تعريف الارتباط للجلسة وCSRF بالسمة `SameSite=Strict`؛ بينما تستخدم ملفات تعريف ارتباط التفويض قصيرة الأجل `SameSite=Lax` لعمليات رد النداء عبر GET من المستوى الأعلى. يؤدي حفظ أي تكوين لـ SSO، بما في ذلك تعطيله أو تدوير السر، إلى إبطال جميع جلسات SSO. يؤدي فك ارتباط حساب إلى إبطال جلسات SSO المرتبطة بذلك الربط. تستمر جلسات كلمة المرور ورموز API المميزة بعد هذه الإجراءات. يؤدي تغيير كلمة مرور المسؤول المحلي إلى إبطال جميع الجلسات ورموز API، وإلغاء التفويضات قيد التنفيذ، والاحتفاظ بالربط الخارجي. تطلب الواجهة تسجيل الدخول مجددًا عند نجاح عملية تبطل جلسة SSO الحالية.

اترك حقل السر فارغًا للاحتفاظ بالسر الحالي، أو أدخل سرًا جديدًا لاستبداله. لا تقدم صفحة الإعدادات خيارًا لمسح السر المحفوظ. يؤدي تغيير المزود أو معرّف العميل أو الـ Issuer أو نقاط نهاية الهوية/حقل المعرّف لـ Generic إلى تبديل مساحة اسم الهوية، ويتطلب بيانات اعتماد جديدة وربطًا جديدًا؛ وتتطلب عمليات الربط الحالية تأكيدًا صريحًا لإعادة التعيين. لتغيير الحساب المرتبط، فك ارتباطه أولاً ثم أنشئ ربطًا جديدًا. تُحفظ أسرار العميل فقط على الخادم داخل قاعدة بيانات SQLite المحمية؛ وتعمل علامة الإعداد الحساس على حجب القيم في الاستجابات ولا تشفّر قاعدة البيانات. لا يتم حفظ رموز الطرف الثالث أو إعادتها إلى المتصفح.

يؤدي تسجيل الخروج القياسي إلى إنهاء جلسة VoCat الحالية فقط. إن تعطيل حساب في IdP أو تسجيل الخروج من Pocket ID لا يُبطل على الفور جلسة VoCat الحالية؛ بل تنتهي صلاحيتها وفقًا لـ `VOCAT_SESSION_TTL`، أو يمكن إبطالها بتعطيل SSO أو فك الارتباط في VoCat. نقطة النهاية العامة للقدرات هي `GET /api/auth/providers`. تتطلب واجهات برمجة تطبيقات إعدادات SSO والربط جلسة متصفح وCSRF للكتابة؛ ولا يمكن لرموز Bearer إدارتها. تُعيد تعارضات مراجعة الإعدادات الرمز `409 sso_config_changed`؛ بينما تُعيد تغييرات مصادر الهوية الرمز `409 sso_rebind_required`.

### الترقية والتراجع

يضيف SSO إصدار مخطط SQLite رقم 29. تحافظ الترقيات على المسؤول الحالي، والجلسات، ورموز API، وبيانات الأجهزة؛ ويظل SSO معطلاً حتى يتم ضبطه. قبل الترقية، سجّل رقم الإصدار الثنائي القديم أو ملخص الصورة، واحتفظ بنسخة احتياطية من الإعدادات وشهادات CA ودليل البيانات الدائمة بالكامل أثناء إيقاف VoCat، أو استخدم واجهة برمجة تطبيقات النسخ الاحتياطي في SQLite أو أمر `.backup` للحصول على لقطة حية متسقة. إن نسخ ملف قاعدة بيانات قيد التشغيل في وضع WAL وحده لا يُعد نسخة احتياطية موثوقة.

عطّل SSO في الإصدار الجديد للتعافي من مشكلات المزود مع الحفاظ على تسجيل الدخول بكلمة المرور وبيانات العمل الحالية. تتطلب العودة إلى ملف ثنائي يعمل بالمخطط 28 إيقاف VoCat واستعادة لقطة البيانات والإعدادات المتطابقة السابقة للترقية. لا يمكن لمجرد التراجع عن صورة الحاوية أو خفض قيمة `PRAGMA user_version` تقليل إصدار قاعدة البيانات. استعد النسخة في دليل/وحدة تخزين منفصلة قدر الإمكان واحتفظ بقاعدة البيانات الأحدث للتحقيق. تؤدي استعادة لقطة إلى فقدان الرسائل وتغييرات المهام المسجلة بعد ذلك النسخ الاحتياطي. بالنسبة إلى Docker، حدّث بسحب الصورة الجديدة وإعادة إنشاء الحاوية.

قبل الإصدار الإنتاجي، أتمم الاختبار الفعلي لمفتاح المرور/سياسة الوصول في Pocket ID وتجربة الترقية والتراجع الخاصة بعملية النشر تلك.

## رموز API المميزة وMCP

تتم مصادقة العملاء بخلاف المتصفح على نفس نقاط النهاية `/api/...` باستخدام رمز Bearer المميز بدلاً من ملف تعريف ارتباط جلسة المتصفح وتدفق CSRF:

```bash
vocat token create --read-only --ttl 720h ci-runner   # يطبع الرمز المميز مرة واحدة فقط
curl -H "Authorization: Bearer vcat_..." http://127.0.0.1:7575/api/devices
```

يدير كل من `vocat token list` و`vocat token revoke <name>` قائمة الرموز. تعمل الرموز المميزة بصلاحيات المسؤول؛ ولا يمكن لرمز `--read-only` إلا إرسال طلبات `GET` (استثناء موثق واحد: قراءة سلسلة رسائل قصيرة تضع عليها علامة مقروءة محليًا — القاعدة هي «لا تعديلات على الأجهزة أو الشبكة»). يؤدي تدوير كلمة مرور المسؤول إلى إبطال كل الرموز المميزة. أنشئ الرموز بعد أول `bootstrap-admin`. يحتاج عملاء Bearer البعيدون أيضًا إلى سياسة وصول للشبكة تسمح بعنوان المصدر الخاص بهم؛ ولا يخدم الوضع الافتراضي `internal` سوى الشبكات الخاصة.

افتراضيًا (عيّن `VOCAT_MCP_ENABLED=false` للتعطيل)، تعرض نفس الخدمة نقطة نهاية MCP (Model Context Protocol) لوكلاء الذكاء الاصطناعي عند `http://<host>:7575/mcp` باستخدام نقل Streamable HTTP والمصادقة برمز Bearer. تغطي الأدوات حالة الأجهزة، وسلاسل الرسائل القصيرة مع الإرسال المحمي، وقائمة ملفات تعريف eSIM وتبديلها، وحالة وإعادة اتصال WiFi Calling؛ ولا يتم كشف العمليات الحساسة (أوامر AT المباشرة، وتنزيل/حذف eSIM، ورمز PIN لشريحة SIM، واختيار الشبكة، وتحديث النظام)، ولا يتضمن إخراج الأدوات أبدًا معرّف IMSI أو رمز PIN أو رموز التنشيط. يتلقى الرمز المخصص للقراءة فقط قائمة أدوات بدون أدوات الكتابة. يتبع إرسال الرسائل القصيرة نفس سياسة الوجهات المحظورة والحد الساعي الشامل لواجهة الويب ويتم عبر مسار AT الخلوي (يظل التوجيه المفضل لـ IMS في واجهة الويب عند تمكين WiFi Calling مقتصرًا على جانب الويب، ولا تُكتب الرسائل المرسلة عبر MCP في سجل محادثات الويب بعد).

مثال على إعداد العميل (باستخدام Claude Code كمثال):

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

انظر [docs/API_MCP_DEVELOPMENT_PLAN.md](API_MCP_DEVELOPMENT_PLAN.md) لسجل التصميم.

## بوت Telegram

عند تفعيل إشعارات Telegram وإعداد كلٍّ من Chat ID وAdmin ID، يدعم البوت:

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

تستخدم عمليتا تبديل الملفات الشخصية وإرسال الرسائل القصيرة أزرار تأكيد لمرة واحدة. لا يعرض البوت أوامر تنزيل أو حذف أو إعادة تسمية eSIM.

تتشارك طرفية الويب وTelegram توجيه USSD. مع تمكين WiFi Calling، تستخدم الطلبات IMS/USSI وتُرجع خطأ في الجاهزية حتى يتم تسجيل IMS، دون الرجوع إلى AT+CUSD الخلوي. مع تعطيل WiFi Calling، تستخدم الطلبات USSD الخلوي. تظل الردود على القوائم وعمليات الإلغاء مرتبطة بالجهاز والمحادثة الأصلية. يتم الإبلاغ عن أخطاء الإلغاء مع الاحتفاظ بالجلسة لإعادة المحاولة؛ ولا تتم إعادة إرسال طلب غير مؤكد تلقائيًا عبر قناة أخرى.

تتبع معالجة محادثات USSI المعيار [3GPP TS 24.390](https://www.etsi.org/deliver/etsi_ts/124300_124399/124390/18.00.00_60/ts_124390v180000p.pdf). يحتفظ فشل أمر BYE بسجل إلغاء، ولكن يتم حظر أي إدخال إضافي في القائمة بمجرد بدء الإلغاء ([RFC 3261، القسم 15.1.1](https://www.rfc-editor.org/rfc/rfc3261.html#section-15.1.1)).

## التحديث

تحقق من وجود GitHub Release أحدث:

```bash
vocat update --check --repo MengMengCode/VoCat
```

ثبّت أحدث إصدار:

```bash
sudo vocat update --repo MengMengCode/VoCat
```

ينزّل المحدّث الملف الثنائي المطابق لبنية Linux الحالية، ويتحقق منه باستخدام `SHA256SUMS` المنشور، ويستبدل الملف التنفيذي بشكل ذري، ويعيد تشغيل خدمة systemd `vocat` عند توفرها.

لتثبيتات Docker:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

أعد إنشاء الحاوية بعد سحب الصورة الجديدة.

## التطوير

المتطلبات:

- Go 1.25 أو أحدث
- Node.js 20 أو أحدث
- npm

تشغيل خادم تطوير الواجهة الأمامية:

```bash
cd web
npm install
npm run dev
```

بناء الواجهة الأمامية المضمّنة وتشغيل الواجهة الخلفية:

```bash
cd web
npm run build
cd ..
go run ./cmd/vocat
```

تشغيل جميع الاختبارات:

```bash
go test ./...
```

بناء ملف ثنائي للإنتاج:

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## أتمتة الإصدارات

يؤدي دفع وسم الإصدار إلى بدء سير عملَي GitHub Actions:

- `release-binaries` يبني وينشر ملفات `amd64` و`386` و`arm64` و`aarch64` و`armv7` الثنائية مع `SHA256SUMS`.
- `docker` يبني وينشر صورة متعددة البنى إلى GitHub Container Registry.

```bash
git tag v0.2.0
git push origin v0.2.0
```

## بنية المشروع

```text
cmd/vocat/                  نقطة دخول التطبيق وCLI
internal/device/            اكتشاف المودم والتحكم في الأجهزة
internal/modem/             جلسة AT ومعالجة الاستجابات
internal/server/            واجهة HTTP API والإشعارات وخادم الويب المضمّن
internal/store/             التخزين الدائم SQLite
internal/update/            المحدّث الذاتي لـ GitHub Release
internal/vowifi/            بيئة تشغيل IKE وEAP-AKA وIMS وWiFi Calling
scripts/install.sh          مثبّت ومحدّث Linux
web/src/                    الواجهة الأمامية React وTypeScript
.github/workflows/          أتمتة إصدارات الملفات الثنائية وDocker
```

## الاستخدام المسؤول

يمكن أن تؤثر عمليات المودم الخلوي وeSIM في خدمة المشترك، والملفات الشخصية المخزنة، وتسجيل الشبكة، وحالة الأجهزة. احتفظ بنسخ احتياطية، وراجع الإجراءات المدمّرة بعناية، واستخدم البرنامج فقط في بيئات قانونية يُسمح لك فيها بتشغيل الأجهزة وموارد الشبكة المتصلة.

لا يتجاوز Vocat مصادقة المشغّل، أو سياسة الشبكة، أو أمان الأجهزة، أو متطلبات الثقة لـ eSIM. إن دعم عملية ما يعني أن Vocat يمكن أن يطلبها من المودم أو eUICC؛ وقد يرفضها الجهاز أو الملف الشخصي أو الشبكة أو المشغّل مع ذلك.

## المساهمة

نرحّب بالمسائل (issues) وطلبات السحب (pull requests). حافظ على التغييرات مركّزة، وأضِف الاختبارات حيثما أمكن، وتجنّب إيداع بيانات الاعتماد أو بيانات المشتركين، ووثّق بوضوح السلوك الخاص بالأجهزة.

قبل إرسال تغيير:

```bash
go test ./...
cd web && npm run build
```

## شكر وتقدير
- [Nodeseek.com](https://www.nodeseek.com) — مجتمع مكرّس للخوادم
- [Linux.do](https://linux.do) — مجتمع تقني ملهم
- [iniwex5](https://github.com/iniwex5) — إرشادات الأسلوب والوظائف

## ادعُني إلى فنجان قهوة

| الشبكة | العنوان |
| ------- | ------- |
| USDT-TRON (TRC20) | `TWSAkvzVsFc7KqncDLmUfRxpPQbpV5CgTB` |
| USDT-BSC (BEP20) | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |
| USDT-Polygon | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |

## الرخصة

انظر [LICENSE](../LICENSE).

## Star History

<a href="https://www.star-history.com/?repos=mengmengcode%2Fvocat&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
 </picture>
</a>
