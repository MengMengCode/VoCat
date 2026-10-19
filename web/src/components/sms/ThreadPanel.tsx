import type { PointerEvent, RefObject, UIEvent, KeyboardEvent } from "react";
import { DeleteRegular, SendRegular } from "@fluentui/react-icons";
import { Button, EmptyState, Textarea } from "../ui";
import { cx } from "../../lib/utils";
import type { SMSMessage } from "../../types";
import {
  messageBody,
  messageDeliveryIndicator,
  messageType,
  type MessageGroup,
  type SmsEncodingInfo,
  type SmsThread,
} from "./smsText";
import { tf, useI18n } from "../../lib/i18n";

export interface ThreadPanelProps {
  isMobile: boolean;
  isDesktop: boolean;
  selectedDevice: string;
  activeThread: SmsThread | null;
  canLoadMore: boolean;
  loadingMore: boolean;
  groups: MessageGroup[];
  deletingMessageId: number | null;
  canHover: boolean;
  composer: string;
  composerInfo: SmsEncodingInfo;
  composerLength: number;
  sending: boolean;
  detailRef: RefObject<HTMLDivElement | null>;
  composerRef: RefObject<HTMLTextAreaElement | null>;
  onBack: () => void;
  onScrollToBottom: () => void;
  onLoadMore: () => void;
  onDeleteMessage: (m: SMSMessage) => void;
  onComposerChange: (v: string) => void;
  onSend: () => void;
  onDetailScroll: (e: UIEvent<HTMLDivElement>) => void;
  onMsgPointerDown: (m: SMSMessage, e: PointerEvent) => void;
  onMsgPointerMove: (e: PointerEvent) => void;
  onMsgPointerEnd: (e: PointerEvent) => void;
}

function DeleteMsgButton({
  loading,
  id,
  canHover,
  onDelete,
}: {
  loading: boolean;
  id: number;
  canHover: boolean;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  return (
    <Button
      variant="text"
      size="small"
      loading={loading}
      aria-label={t("删除短信 ") + id}
      title={t("删除短信")}
      onClick={onDelete}
      className={cx("sms-danger-ghost-btn sms-delete-trigger sms-message-delete-btn", !canHover && "sms-delete-visible")}
      icon={<DeleteRegular />}
    />
  );
}

export function ThreadPanel(props: ThreadPanelProps) {
  const { t } = useI18n();
  const {
    isMobile,
    isDesktop,
    selectedDevice,
    activeThread,
    canLoadMore,
    loadingMore,
    groups,
    deletingMessageId,
    canHover,
    composer,
    composerInfo,
    composerLength,
    sending,
    detailRef,
    composerRef,
    onBack,
    onScrollToBottom,
    onLoadMore,
    onDeleteMessage,
    onComposerChange,
    onSend,
    onDetailScroll,
    onMsgPointerDown,
    onMsgPointerMove,
    onMsgPointerEnd,
  } = props;

  const subtitle =
    selectedDevice === "all"
      ? activeThread && (activeThread.localPhone || activeThread.lastDeviceName)
        ? tf("本机：{phone}", { phone: activeThread.localPhone || activeThread.lastDeviceName })
        : t("全部设备")
      : tf("设备：{device}", { device: selectedDevice });

  function onComposerKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey) {
      e.preventDefault();
      onSend();
    }
  }

  return (
    <div className="flex min-h-0 min-w-0 flex-col">
      <div className="flex items-center justify-between gap-3 border-b border-gray-100 p-4 dark:border-white/10">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            {isMobile && activeThread ? (
              <Button variant="text" onClick={onBack}>
                {t("返回")}
              </Button>
            ) : null}
            <div className="truncate text-sm font-extrabold text-gray-900 dark:text-white">
              {activeThread?.peer || t("请选择会话")}
            </div>
          </div>
          <div className="mt-1 text-xs text-gray-400">{subtitle}</div>
        </div>
        {activeThread ? (
          <div className="flex items-center gap-2">
            <Button variant="text" onClick={onScrollToBottom}>
              {t("最新")}
            </Button>
          </div>
        ) : null}
      </div>
      {activeThread ? (
        <div ref={detailRef} onScroll={onDetailScroll} className="sms-detail-scroll min-h-0 flex-1 overflow-y-auto">
          <div className="space-y-5 p-5">
            {canLoadMore ? (
              <div className="flex justify-center">
                <Button variant="text" loading={loadingMore} onClick={onLoadMore}>
                  {t("加载更多")}
                </Button>
              </div>
            ) : null}
{groups.map((g) => (
  <div key={g.date} className="space-y-4">
    <div className="flex justify-center">
      <div className="rounded-full border border-gray-200/60 bg-gray-100/80 px-3 py-1 text-[11px] font-bold text-gray-500 dark:border-white/10 dark:bg-white/5 dark:text-gray-300">
        {g.date}
      </div>
    </div>
    {g.items.map((m) => {
      const outbound = messageType(m) !== 1;
      const delivery = messageDeliveryIndicator(m);
      const hasDevice = !!m.deviceName;
      return (
        <div key={m.id} className={cx("flex", outbound ? "justify-end" : "justify-start")}>
<div
  className="sms-msg-wrapper group"
  onPointerDown={(e) => onMsgPointerDown(m, e)}
  onPointerMove={onMsgPointerMove}
  onPointerUp={onMsgPointerEnd}
  onPointerCancel={onMsgPointerEnd}
>
<div className={cx("mb-1 flex items-center gap-2", outbound && "justify-end")}>
  {!outbound ? <span className="text-xs font-bold text-gray-700 dark:text-gray-200">{m.sender}</span> : null}
  {isDesktop && outbound && hasDevice ? (
    <DeleteMsgButton loading={deletingMessageId === m.id} id={m.id} canHover={canHover} onDelete={() => onDeleteMessage(m)} />
  ) : null}
  {hasDevice ? (
    <span className="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-bold text-gray-400 dark:bg-white/5">{m.deviceName}</span>
  ) : null}
  <span className="font-mono text-[11px] text-gray-400">{new Date(m.timestamp).toLocaleString()}</span>
  {outbound && delivery === "delivered" ? (
    <span className="text-xs font-bold text-green-500" title="已确认送达">✓✓</span>
  ) : outbound && delivery === "accepted_unconfirmed" ? (
    <span className="text-xs font-bold text-amber-500" title="已提交，尚未确认送达">✓</span>
  ) : outbound && delivery === "failed" ? (
    <span className="text-xs font-bold text-red-500" title="发送失败">✗</span>
  ) : outbound ? (
    <span className="text-xs font-bold text-gray-400" title="发送状态未知">?</span>
  ) : null}
  {isDesktop && (!outbound || !hasDevice) ? (
    <DeleteMsgButton loading={deletingMessageId === m.id} id={m.id} canHover={canHover} onDelete={() => onDeleteMessage(m)} />
  ) : null}
</div>
<div
  className={cx(
    "rounded-2xl border px-5 py-4 text-sm leading-[1.75] shadow-sm",
    outbound
      ? "border-indigo-100 bg-indigo-50 text-gray-800 dark:border-indigo-500/20 dark:bg-indigo-500/10 dark:text-gray-100"
      : "border-gray-100 bg-white/90 text-gray-700 dark:border-white/10 dark:bg-white/5 dark:text-gray-200",
  )}
>
  {m.decodeError ? (
    <div className="mb-1 text-xs text-amber-700 dark:text-amber-400" role="status">
      {t("短信解析失败")}：{m.decodeError}
    </div>
  ) : null}
  {messageBody(m) || (
    <span className="text-gray-400">{t("短信正文为空")}</span>
  )}
</div>
</div>
        </div>
      );
    })}
  </div>
))}
            <div className="h-2" />
          </div>
        </div>
      ) : (
        <div className="flex flex-1 items-center justify-center p-6">
          <EmptyState title={t("请选择一个会话")} subtitle={t("从左侧联系人列表进入短信明细")} />
        </div>
      )}
{activeThread ? (
  <div className="border-t border-gray-100 p-4 dark:border-white/10">
    <div className="mb-2 text-left text-[11px] text-gray-400">
      {tf("{encoding} · 预计 {parts} 段 · {length} 字", { encoding: composerInfo.encoding, parts: composerInfo.parts, length: composerLength })}
    </div>
    <div className="flex items-end gap-3">
      <Textarea
        ref={composerRef}
        value={composer}
        onChange={(e) => onComposerChange(e.target.value)}
        onKeyDown={onComposerKeyDown}
        placeholder={t("回复（Enter 发送）")}
        rows={1}
        className="max-h-40 resize-none [field-sizing:content]"
      />
      <Button variant="primary" loading={sending} onClick={onSend} className="!border-0 self-end" icon={<SendRegular />}>
        {t("发送")}
      </Button>
    </div>
  </div>
) : null}
    </div>
  );
}
