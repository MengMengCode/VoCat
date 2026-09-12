import { useEffect, useState } from "react";
import { SettingsRegular } from "@fluentui/react-icons";
import { api, apiMessage } from "../../api";
import { useI18n } from "../../lib/i18n";
import { message } from "../ui";
import { Switch } from "../ui/Switch";
import { CardDecor, CardIcon, CardTitle } from "./Cards";

interface VoWiFiSettings { mtuCompatibility: boolean }

export function VoWiFiMTUCard() {
  const { t } = useI18n();
  const [enabled, setEnabled] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    let active = true;
    api<VoWiFiSettings>("/settings/vowifi").then((data) => {
      if (active) { setEnabled(data.mtuCompatibility === true); setLoaded(true); }
    }).catch(() => { if (active) message.error(t("VoWiFi 兼容设置加载失败")); });
    return () => { active = false; };
  }, [t]);
  const onToggle = async (value: boolean) => {
    setSaving(true);
    try {
      const data = await api<VoWiFiSettings>("/settings/vowifi", {
        method: "PUT", body: { mtuCompatibility: value },
      });
      setEnabled(data.mtuCompatibility === true);
      message.success(t("设置已保存，请重连 VoWiFi 后生效"));
    } catch (error) {
      message.error(apiMessage(error) || t("VoWiFi 兼容设置保存失败"));
    } finally { setSaving(false); }
  };
  return (
    <div className="ui-card group relative overflow-hidden p-8">
      <CardDecor />
      <div className="relative z-10 mb-6 flex items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <CardIcon><SettingsRegular className="text-[24px]" /></CardIcon>
          <CardTitle title={t("VoWiFi MTU 兼容模式")} subtitle={t("改善部分系统因网络包大小限制导致的注册失败")} />
        </div>
        <Switch checked={enabled} disabled={!loaded || saving} loading={saving} onChange={onToggle} />
      </div>
      <p className="relative z-10 text-xs leading-5 text-gray-500 dark:text-gray-400">
        {t("默认关闭。遇到 MTU 不足导致的 VoWiFi 连接问题时可尝试开启。此设置适用于所有设备，保存后请重连 VoWiFi。")}
      </p>
    </div>
  );
}
