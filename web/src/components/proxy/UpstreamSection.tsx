import { DeleteRegular, DesktopRegular, EditRegular, GlobeRegular } from "@fluentui/react-icons";
import type { UpstreamProxy } from "../../types";
import { Button, Tag } from "../ui";
import type { LoadError, UpstreamRow } from "./shared";
import { useI18n } from "../../lib/i18n";

export interface UpstreamSectionProps {
  rows: UpstreamRow[];
  loading: boolean;
  error: LoadError | null;
  onRetry: () => void;
  onEdit: (proxy: UpstreamProxy) => void;
  onDelete: (proxy: UpstreamProxy) => void;
  onOpenBindings: (proxy: UpstreamProxy) => void;
}

export function UpstreamSection({ rows, loading, error, onRetry, onEdit, onDelete, onOpenBindings }: UpstreamSectionProps) {
  const { t } = useI18n();
  return (
    <div className="ui-card overflow-hidden">
      {error ? (
        <div className="flex items-center justify-between gap-3 border-b border-red-200 bg-red-50 p-3 text-sm text-red-600 dark:border-red-500/20 dark:bg-red-500/10 dark:text-red-300">
          <span className="min-w-0 truncate">
            {t("加载上游代理失败")}：{error.message}
            {error.status ? `（${error.status}）` : ""}
          </span>
          <button type="button" className="shrink-0 font-medium underline underline-offset-2" onClick={onRetry}>
            {t("重试")}
          </button>
        </div>
      ) : null}
      <div className="overflow-x-auto">
        <table className="w-full min-w-[900px] text-left text-sm">
          <thead className="border-b border-gray-100 bg-gray-50/70 text-xs uppercase tracking-wide text-gray-500 dark:border-white/10 dark:bg-white/[0.025]">
            <tr>
              <th className="px-4 py-3">{t("名称")}</th>
              <th className="px-4 py-3">{t("协议")}</th>
              <th className="px-4 py-3">{t("地址")}</th>
              <th className="px-4 py-3">{t("鉴权")}</th>
              <th className="px-4 py-3">{t("状态")}</th>
              <th className="px-4 py-3">{t("Profile 绑定")}</th>
              <th className="px-4 py-3 text-right">{t("操作")}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-white/10">
            {rows.map((row) => (
              <tr key={row.id} className="hover:bg-sky-50/40 dark:hover:bg-sky-500/[0.04]">
                <td className="px-4 py-3 font-semibold">{row.name || row.id}</td>
                <td className="px-4 py-3"><Tag type="primary">SOCKS5</Tag></td>
                <td className="px-4 py-3 font-mono text-xs">{row.addr}</td>
                <td className="px-4 py-3">{row.username || t("无")}</td>
                <td className="px-4 py-3"><Tag type={row.enabled ? "success" : "info"}>{row.enabled ? t("已启用") : t("已禁用")}</Tag></td>
                <td className="px-4 py-3">
                  <div className="inline-flex items-center gap-1 rounded border border-indigo-200/60 bg-indigo-50 px-2 py-0.5 text-[11px] font-medium text-indigo-600 dark:border-indigo-800/40 dark:bg-indigo-900/20 dark:text-indigo-400">
                    <DesktopRegular className="text-[14px]" />
                    <span>{row.bindingCount} {t("个 Profile")}</span>
                  </div>
                </td>
                <td className="px-4 py-3">
                  <div className="flex justify-end gap-2">
                    <Button size="small" icon={<DesktopRegular />} onClick={() => onOpenBindings(row)}>{t("Profile 绑定")}</Button>
                    <Button size="small" icon={<EditRegular />} onClick={() => onEdit(row)}>{t("编辑")}</Button>
                    <Button size="small" variant="danger" plain icon={<DeleteRegular />} onClick={() => onDelete(row)}>{t("删除")}</Button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {!loading && !rows.length ? (
        <div className="flex flex-col items-center justify-center px-6 py-16 text-center text-gray-400">
          <GlobeRegular className="mb-3 text-4xl" />
          <div className="text-sm">{t("暂无上游代理")}</div>
          <div className="mt-1 text-xs">{t("点击“新增代理”创建 SOCKS5 上游代理，再按 ICCID 绑定需要使用它的 eSIM Profile；未绑定 Profile 默认直连。")}</div>
        </div>
      ) : null}
      {loading ? <div className="px-6 py-16 text-center text-sm text-gray-400">{t("加载中...")}</div> : null}
    </div>
  );
}
