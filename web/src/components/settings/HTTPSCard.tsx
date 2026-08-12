import { LockClosedRegular } from "@fluentui/react-icons";
import type { HTTPSSettings } from "../../types";
import { useI18n } from "../../lib/i18n";
import { Button } from "../ui/Button";
import { Switch } from "../ui/Switch";
import { CardDecor, CardIcon, CardTitle } from "./Cards";

export function HTTPSCard({
  value,
  loading,
  saving,
  onToggle,
}: {
  value: HTTPSSettings | null;
  loading: boolean;
  saving: boolean;
  onToggle: (enabled: boolean) => void;
}) {
  const { lang } = useI18n();
  const zh = lang === "zh";
  const enabled = !!value?.enabled;
  return (
    <div className="ui-card group relative overflow-hidden p-8">
      <CardDecor />
      <div className="relative z-10 mb-6 flex items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <CardIcon>
            <LockClosedRegular className="text-[24px]" />
          </CardIcon>
          <CardTitle
            title={zh ? "本机自签 HTTPS" : "Local self-signed HTTPS"}
            subtitle={zh ? "为安全连接提供 HTTPS" : "HTTPS for secure connections"}
          />
        </div>
        <Switch checked={enabled} disabled={loading || saving} loading={saving} onChange={onToggle} />
      </div>
      <div className="relative z-10 space-y-4 text-sm text-gray-600 dark:text-gray-300">
        <p>
          {enabled
            ? (zh ? "已强制 HTTPS；HTTP 请求会自动跳转。关闭后立即恢复 HTTP。" : "HTTPS is enforced and HTTP redirects automatically. Disable it to return to HTTP immediately.")
            : (zh ? "当前使用 HTTP。启用后会生成并持久保存本机自签证书。" : "HTTP is active. Enabling generates and persists a local self-signed certificate.")}
        </p>
        {value?.fingerprint ? (
          <div className="rounded-xl bg-gray-50 p-3 dark:bg-white/5">
            <div className="mb-1 text-xs font-bold uppercase tracking-wider text-gray-500">SHA-256</div>
            <div className="break-all font-mono text-xs">{value.fingerprint}</div>
          </div>
        ) : null}
        <p className="text-xs text-amber-600 dark:text-amber-400">
          {zh
            ? "自签证书需要在系统或浏览器中信任，否则浏览器可能继续提示连接不安全。"
            : "Trust the self-signed certificate in the operating system or browser; otherwise the browser may keep warning that the connection is not secure."}
        </p>
        <Button onClick={() => window.open("/api/settings/https/certificate", "_blank")} disabled={loading}>
          {zh ? "下载自签证书" : "Download certificate"}
        </Button>
      </div>
    </div>
  );
}
