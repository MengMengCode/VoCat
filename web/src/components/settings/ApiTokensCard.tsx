import { DeleteRegular, KeyRegular } from "@fluentui/react-icons";
import type { ApiTokenListItem } from "../../types";
import { tf, useI18n } from "../../lib/i18n";
import { Button, confirmDialog } from "../ui";
import { CardDecor, CardIcon, CardTitle } from "./Cards";

const CELL_CLASS = "px-4 py-3 text-sm text-gray-700 dark:text-gray-300";
const HEAD_CLASS =
  "px-4 py-2 text-left text-xs font-bold uppercase tracking-wider text-gray-500";

function formatTime(value: string, never: string): string {
  if (!value) return never;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

// API 令牌管理：只展示与吊销。令牌凭据仅在 CLI 创建时显示一次，
// 明文不经过浏览器会话，因此这里刻意不提供创建入口。
export function ApiTokensCard({
  tokens,
  loading,
  revoking,
  onRevoke,
}: {
  tokens: ApiTokenListItem[];
  loading: boolean;
  revoking: string | null;
  onRevoke: (name: string) => void;
}) {
  const { t } = useI18n();

  return (
    <div className="ui-card group relative overflow-hidden p-8 lg:col-span-2">
      <CardDecor />
      <div className="relative z-10 mb-6 flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-3">
          <CardIcon>
            <KeyRegular className="text-[24px]" />
          </CardIcon>
          <CardTitle
            title={t("API 令牌")}
            subtitle={t("供脚本与 MCP 客户端使用的 Bearer 凭据；创建请在设备上执行 vocat token create")}
          />
        </div>
      </div>

      {loading ? (
        <div className="text-sm text-gray-500">{t("加载中...")}</div>
      ) : tokens.length === 0 ? (
        <div className="rounded-lg border border-dashed border-gray-300 p-4 text-sm text-gray-500 dark:border-gray-700">
          {t("尚未创建任何 API 令牌。在设备上运行 vocat token create --read-only --ttl 720h <名称> 后，令牌会显示在这里。")}
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse">
            <thead>
              <tr className="border-b border-gray-200 dark:border-gray-700">
                <th className={HEAD_CLASS}>{t("名称")}</th>
                <th className={HEAD_CLASS}>{t("权限")}</th>
                <th className={HEAD_CLASS}>{t("创建时间")}</th>
                <th className={HEAD_CLASS}>{t("最后使用")}</th>
                <th className={HEAD_CLASS}>{t("过期时间")}</th>
                <th className={HEAD_CLASS}></th>
              </tr>
            </thead>
            <tbody>
              {tokens.map((token) => (
                <tr
                  key={token.name}
                  className="border-b border-gray-100 last:border-b-0 dark:border-gray-800"
                >
                  <td className={`${CELL_CLASS} font-mono`}>{token.name}</td>
                  <td className={CELL_CLASS}>
                    <span
                      className={
                        token.readOnly
                          ? "rounded-full bg-emerald-100 px-2 py-0.5 text-xs font-semibold text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300"
                          : "rounded-full bg-amber-100 px-2 py-0.5 text-xs font-semibold text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
                      }
                    >
                      {token.readOnly ? t("只读") : t("读写")}
                    </span>
                  </td>
                  <td className={CELL_CLASS}>{formatTime(token.createdAt, "-")}</td>
                  <td className={CELL_CLASS}>{formatTime(token.lastUsedAt, t("从未使用"))}</td>
                  <td className={CELL_CLASS}>{formatTime(token.expiresAt, t("永不过期"))}</td>
                  <td className={CELL_CLASS}>
                    <Button
                      variant="danger"
                      loading={revoking === token.name}
                      disabled={revoking !== null}
                      onClick={async () => {
                        const ok = await confirmDialog(
                          tf("确定吊销令牌「{name}」？使用它的脚本与 MCP 客户端将立即失去访问权限。", { name: token.name }),
                          t("确认吊销"),
                          { type: "warning", confirmText: t("吊销"), cancelText: t("取消") },
                        );
                        if (ok) onRevoke(token.name);
                      }}
                    >
                      <span className="flex items-center gap-1.5">
                        <DeleteRegular className="text-[16px]" />
                        {t("吊销")}
                      </span>
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
