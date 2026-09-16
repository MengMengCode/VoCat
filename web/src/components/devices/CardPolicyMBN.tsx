import { useEffect, useState } from "react";
import { Select, message } from "../ui";
import { useI18n } from "../../lib/i18n";
import { apiMessage } from "../../api";
import { updateCardPolicy } from "./deviceActions";
import type { CardPolicy } from "../../types";

interface CardPolicyMBNProps {
  iccid: string;
  policy: CardPolicy | null;
  disabled?: boolean;
  compact?: boolean;
  onSaved: (policy: CardPolicy) => void;
}

export function CardPolicyMBN({ iccid, policy, disabled, onSaved }: CardPolicyMBNProps) {
  const { t } = useI18n();
  const saved = policy?.mbnProfile || "";
  const [value, setValue] = useState(saved);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setValue(policy?.mbnProfile || "");
  }, [iccid, policy?.mbnProfile]);

  const options = [
    { value: "", label: t("自动（按卡的 HPLMN 选择）") },
    { value: "OpenMkt-Commercial-CU", label: t("强制中国联通 OpenMkt") },
    { value: "Volte_OpenMkt-Commercial-CMCC", label: t("强制中国移动 VoLTE") },
    { value: "OpenMkt-Commercial-CT", label: t("强制中国电信 OpenMkt") },
  ];

  const save = async (next: string) => {
    if (!iccid || saving || next === saved) {
      setValue(saved);
      return;
    }
    setSaving(true);
    setValue(next);
    try {
      const updated = await updateCardPolicy(iccid, { mbnProfile: next });
      onSaved(updated);
      message.success(
        updated.mbnProfile
          ? t("已保存强制 MBN；若此卡正在使用，模组可能会重启")
          : t("已恢复自动选择 MBN"),
      );
    } catch (error) {
      setValue(saved);
      message.error(apiMessage(error) || t("保存 MBN 失败"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="ui-panel-muted p-3">
      <div className="mb-1.5 text-xs font-bold uppercase tracking-wider text-gray-500">{t("MBN 配置")}</div>
      <Select
        value={value}
        options={options}
        disabled={disabled || saving}
        onChange={save}
      />
      <div className="mt-1.5 text-[11px] leading-4 text-gray-500 dark:text-gray-400">
        {t("EC20-CE 没有 ROW_Generic_3GPP。海外卡请按卡指定运营商 MBN；正在使用的卡更改后模组可能会重启。")}
      </div>
    </div>
  );
}
