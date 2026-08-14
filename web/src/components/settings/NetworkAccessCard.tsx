import {
  AddRegular,
  CheckmarkRegular,
  DeleteRegular,
  GlobeRegular,
  WarningRegular,
} from "@fluentui/react-icons";
import type { SecuritySettings } from "../../types";
import { useI18n } from "../../lib/i18n";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Switch } from "../ui/Switch";
import { CardDecor, CardIcon, CardTitle } from "./Cards";
import { SegmentedTabs } from "./controls";

export interface NetworkAccessForm {
  mode: SecuritySettings["mode"];
  allowedCidrs: string[];
  trustProxyHeaders: boolean;
}

const LABEL_CLASS = "text-xs font-bold text-gray-500 uppercase tracking-wider";

// 网络访问控制：默认仅放行内网网段，可切换对公网开放，并支持额外放行网段。
export function NetworkAccessCard({
  value,
  clientIp,
  clientAllowed,
  loading,
  saving,
  onChange,
  onSave,
}: {
  value: NetworkAccessForm;
  clientIp: string;
  clientAllowed: boolean;
  loading: boolean;
  saving: boolean;
  onChange: (patch: Partial<NetworkAccessForm>) => void;
  onSave: () => void;
}) {
  const { t } = useI18n();
  const cidrs = value.allowedCidrs;
  const setCidrs = (next: string[]) => onChange({ allowedCidrs: next });

  return (
    <div className="ui-card group relative overflow-hidden p-8 lg:col-span-2">
      <CardDecor />
      <div className="relative z-10 mb-6 flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-3">
          <CardIcon>
            <GlobeRegular className="text-[24px]" />
          </CardIcon>
          <CardTitle title={t("网络访问")} subtitle={t("控制允许访问系统的来源网段")} />
        </div>
        <Button
          variant="primary"
          loading={saving}
          disabled={loading}
          onClick={onSave}
          className="!border-0"
        >
          <CheckmarkRegular />
          {t("保存访问策略")}
        </Button>
      </div>

      {loading ? (
        <div className="p-6 text-sm text-gray-500 dark:text-gray-400">{t("正在加载访问策略…")}</div>
      ) : (
        <div className="relative z-10 space-y-6">
          <div>
            <SegmentedTabs
              tabs={[
                { key: "internal", label: t("内网优先") },
                { key: "public", label: t("对公网开放") },
              ]}
              value={value.mode}
              onChange={(mode) => onChange({ mode: mode as NetworkAccessForm["mode"] })}
            />
            <p className="-mt-3 text-xs leading-relaxed text-gray-500 dark:text-gray-400">
              {value.mode === "public"
                ? t("允许任意来源 IP 访问（含公网）。仅在已设置强密码且网络环境可信时使用。")
                : t("默认仅允许内网网段访问：10/8、172.16/12、192.168/16、169.254/16、127/8、::1、fe80::/10、fc00::/7。")}
            </p>
          </div>

          {value.mode === "public" ? (
            <div className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800 dark:border-amber-500/20 dark:bg-amber-500/10 dark:text-amber-200">
              <WarningRegular className="mt-0.5 shrink-0" />
              <span>{t("对公网开放会显著扩大攻击面，请务必使用强密码并尽快切回内网优先。")}</span>
            </div>
          ) : null}

          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <label className={LABEL_CLASS}>{t("额外放行网段")}</label>
              <Button size="small" variant="primary" plain onClick={() => setCidrs([...cidrs, ""])}>
                <AddRegular />
                <span className="ml-1">{t("添加网段")}</span>
              </Button>
            </div>
            <p className="text-[10px] text-gray-400">
              {t("在内置内网网段之外始终放行的 CIDR 或单个 IP；也允许通知推送访问这些目标地址（例如 198.18.0.0/15）。")}
            </p>
            {cidrs.length === 0 ? (
              <div className="rounded-lg border border-dashed border-gray-200 bg-gray-50/30 py-2 text-center text-xs text-gray-400 dark:border-white/10 dark:bg-white/5">
                {t("暂无额外放行网段")}
              </div>
            ) : null}
            {cidrs.map((cidr, index) => (
              <div key={index} className="flex items-center gap-2">
                <Input
                  value={cidr}
                  onChange={(event) =>
                    setCidrs(cidrs.map((item, i) => (i === index ? event.target.value : item)))
                  }
                  placeholder="192.168.0.0/16"
                  className="flex-1 font-mono"
                />
                <Button
                  variant="danger"
                  plain
                  onClick={() => setCidrs(cidrs.filter((_, i) => i !== index))}
                  aria-label={t("删除")}
                >
                  <DeleteRegular />
                </Button>
              </div>
            ))}
          </div>

          <div className="flex items-start justify-between gap-4 rounded-lg bg-gray-50 p-3 dark:bg-white/5">
            <div className="space-y-1">
              <div className="text-sm font-medium text-gray-800 dark:text-gray-100">
                {t("信任代理请求头")}
              </div>
              <p className="text-[11px] leading-relaxed text-gray-500 dark:text-gray-400">
                {t("仅在系统位于可信反向代理之后时开启，按 X-Forwarded-For 判定来源；否则客户端可伪造该头绕过内网限制。")}
              </p>
            </div>
            <Switch
              checked={value.trustProxyHeaders}
              onChange={(trustProxyHeaders) => onChange({ trustProxyHeaders })}
              ariaLabel={t("信任代理请求头")}
            />
          </div>

          {!clientAllowed && (
            <div className="flex items-center gap-2 rounded-lg border border-red-200 bg-red-50 p-3 text-xs text-red-700 dark:border-red-500/20 dark:bg-red-500/10 dark:text-red-300">
              <WarningRegular className="shrink-0" />
              <span className="font-mono">{clientIp || "--"}</span>
              <span className="font-semibold">
                {t("当前连接将被拒绝，保存后可能无法继续访问")}
              </span>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
