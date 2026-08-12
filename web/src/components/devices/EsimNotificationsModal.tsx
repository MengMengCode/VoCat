import { Button, Modal, Tag } from "../ui";
import type { EsimNotification } from "./types";
import { tl, useI18n } from "../../lib/i18n";

function eventLabel(event?: string): string {
  switch (event) {
    case "install":
      return tl("安装");
    case "enable":
      return tl("启用");
    case "disable":
      return tl("禁用");
    case "delete":
      return tl("删除");
    case "":
      return tl("未知");
    default:
      return event || tl("未知");
  }
}

function MetaLine({ label, value }: { label: string; value?: string }) {
  if (!value) return null;
  return (
    <div className="w-full min-w-0 rounded-lg bg-gray-100 px-2 py-0.5 text-xs leading-[18px] text-gray-600 dark:bg-white/5 dark:text-gray-300">
      <span className="mr-1 text-gray-400 dark:text-gray-500">{label}</span>
      <span className="break-all">{value}</span>
    </div>
  );
}

export interface EsimNotificationsModalProps {
  open: boolean;
  loading: boolean;
  items: EsimNotification[];
  retryingSeq: number | null;
  onClose: () => void;
  onRetry: (item: EsimNotification) => void;
}

export function EsimNotificationsModal({ open, loading, items, retryingSeq, onClose, onRetry }: EsimNotificationsModalProps) {
  const { t } = useI18n();
  return (
    <Modal open={open} onClose={onClose} title={t("当前通知列表")} width="max-w-[min(500px,80vw)]" className="glass-modal">
      {loading ? (
        <div className="py-10 text-center text-sm text-gray-400">{t("正在加载通知...")}</div>
      ) : items.length === 0 ? (
        <div className="py-10 text-center text-sm text-gray-400">{t("当前没有可展示的通知")}</div>
      ) : (
        <div className="max-h-[420px] space-y-2 overflow-auto pr-1">
          {items.map((item) => (
            <div
              key={`${item.aidHex || "default"}:${item.sequenceNumber}`}
              className="flex flex-col gap-3 rounded-xl border border-gray-200 p-3 dark:border-white/10 sm:flex-row sm:items-start sm:justify-between"
            >
              <div className="min-w-0 flex-1 space-y-1">
                <div className="flex items-center gap-2 text-sm font-medium text-gray-900 dark:text-white">
                  <span>#{item.sequenceNumber}</span>
                  <Tag type="info">{eventLabel(item.event)}</Tag>
                </div>
                <div className="flex flex-col gap-1 pt-1">
                  <MetaLine label="ICCID" value={item.iccid} />
                  <MetaLine label={t("地址")} value={item.address} />
                  <MetaLine label="AID" value={item.aidHex} />
                </div>
              </div>
              <Button
                variant="primary"
                size="small"
                className="self-start sm:self-auto"
                disabled={!item.canRetry}
                loading={retryingSeq === item.sequenceNumber}
                onClick={() => onRetry(item)}
              >
                {t("重发")}
              </Button>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}
