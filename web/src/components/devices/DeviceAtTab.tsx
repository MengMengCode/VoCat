import { useState } from "react";
import { WindowConsoleRegular, WarningRegular } from "@fluentui/react-icons";
import { api } from "../../api";
import { Button, Input, Select, Switch } from "../ui";
import { AT_COMMAND_GROUPS } from "./atCommands";
import { AtLogEntry, AtTypingBubble, type AtLogItem } from "./AtLogEntry";
import { useI18n } from "../../lib/i18n";

export interface DeviceAtTabProps {
  deviceId: string;
  backendMode?: string;
  atPort?: string;
  running: boolean;
}

export function DeviceAtTab({ deviceId, backendMode, atPort, running }: DeviceAtTabProps) {
  const { t } = useI18n();
  const [cmd, setCmd] = useState("");
  const [template, setTemplate] = useState("");
  const [timeoutMs, setTimeoutMs] = useState<number>(10000);
  const [sending, setSending] = useState(false);
  const [force, setForce] = useState(false);
  const [log, setLog] = useState<AtLogItem[]>([]);

  const hasAtPort = String(atPort || "").trim().length > 0;
  const usable = !!running && hasAtPort;
  const unavailableTitle = running ? (hasAtPort ? t("AT 终端暂不可用") : t("当前设备没有可用 AT 口")) : t("当前设备未运行");
  const unavailableDesc = running
    ? !hasAtPort && backendMode === "qmi"
      ? t("设备当前处于纯 QMI 模式，但没有解析到可用的 AT 口，因此无法提供 AT 串口终端。")
      : hasAtPort
        ? t("当前设备暂时无法提供 AT 串口终端，请稍后重试。")
        : t("设备当前没有可用的 AT 口，因此无法提供 AT 串口终端。")
    : t("设备当前未启动，AT 终端暂时不可用。待设备运行后，如果存在可用的 AT 口，即可在这里直接发送 AT 指令。");

  async function send() {
    const command = String(cmd || "").trim();
    if (!command) return;
    setSending(true);
    setCmd("");
    try {
      const res = await api<{ ok?: boolean; response?: string; result?: string }>(`/devices/${deviceId}/actions/at`, {
        method: "POST",
        body: { cmd: command, timeoutMs: timeoutMs || 10000, force },
      });
      setLog((prev) => [
        ...prev,
        { ts: Date.now(), cmd: command, ok: res?.ok ?? true, response: res?.response ?? res?.result ?? JSON.stringify(res ?? {}) },
      ]);
    } catch (e) {
      setLog((prev) => [...prev, { ts: Date.now(), cmd: command, ok: false, response: e instanceof Error ? e.message : t("请求异常") }]);
    } finally {
      setSending(false);
    }
  }

  return (
    <div>
      <div className="flex items-center gap-3">
        <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-indigo-50 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-400">
          <WindowConsoleRegular className="text-[22px]" />
        </div>
        <div>
          <div className="text-lg font-bold text-gray-900 dark:text-white">{t("AT 终端")}</div>
          <div className="mt-0.5 text-sm text-gray-500 dark:text-gray-400">{t("发送 AT 指令并查看回显（多行响应会完整返回）")}</div>
        </div>
      </div>
      {usable ? (
        <>
          <div className="ui-panel-muted relative mt-4 flex h-[320px] flex-col gap-3 overflow-auto rounded-xl border border-gray-100 p-4 dark:border-white/10">
            {log.length === 0 && !sending ? (
              <div className="absolute inset-0 flex items-center justify-center text-sm text-gray-400">{t("暂无 AT 会话记录")}</div>
            ) : null}
            {log.map((item, i) => (
              <AtLogEntry key={`${item.ts}-${item.cmd}-${i}`} item={item} />
            ))}
            {sending ? <AtTypingBubble label={t("等待模组响应...")} /> : null}
          </div>
          <div className="mt-4 grid grid-cols-1 gap-3 md:grid-cols-[200px_1fr_110px_auto]">
            <div className="space-y-1">
              <div className="text-[11px] font-bold uppercase tracking-wider text-gray-500">{t("快捷指令模板")}</div>
              <Select
                value={template}
                onChange={(v) => {
                  setTemplate(v);
                  if (v) setCmd(v);
                }}
                placeholder={t("选择常用命令（可选）")}
                options={AT_COMMAND_GROUPS.flatMap((g) => g.items.map((it) => ({ value: it.value, label: `${t(g.label)} · ${t(it.label)}` })))}
              />
            </div>
            <div className="space-y-1">
              <div className="text-[11px] font-bold uppercase tracking-wider text-gray-500">{t("命令")}</div>
              <Input
                value={cmd}
                onChange={(e) => setCmd(e.target.value)}
                placeholder={t("例如 AT+CSQ (可自由编辑)")}
                disabled={sending}
                onKeyDown={(e) => {
                  if (e.key === "Enter") send();
                }}
              />
            </div>
            <div className="space-y-1">
              <div className="text-[11px] font-bold uppercase tracking-wider text-gray-500">{t("超时(ms)")}</div>
              <Input
                type="number"
                inputMode="numeric"
                value={String(timeoutMs)}
                onChange={(e) => setTimeoutMs(Number(e.target.value))}
                placeholder="10000"
              />
            </div>
            <div className="space-y-1 self-end">
              <div className="select-none text-[11px] font-bold uppercase tracking-wider opacity-0">{t("操作")}</div>
              <div className="flex items-center justify-end gap-2">
                <Button onClick={() => setLog([])}>{t("清空")}</Button>
                <Button variant="primary" loading={sending} disabled={!cmd} onClick={send} className="!border-0">
                  {t("发送")}
                </Button>
              </div>
            </div>
          </div>
          <div className="mt-3 flex items-center justify-end gap-3">
            <div className="flex items-center gap-2 text-sm text-orange-600 dark:text-orange-400">
              <WarningRegular className="text-base" />
              <span>{t("强制模式允许发送默认被拦截的 AT 指令（如切网、拨号、短信、USSD），误操作可能导致断网或费用扣除。")}</span>
            </div>
            <Switch checked={force} onChange={setForce} ariaLabel={t("强制发送 AT 指令")} />
          </div>
        </>
      ) : (
        <div className="mt-4 flex flex-col items-center justify-center rounded-xl border border-orange-100 bg-orange-50 p-8 dark:border-orange-900/50 dark:bg-orange-900/20">
          <WarningRegular className="mb-4 text-[48px] text-orange-400" />
          <div className="text-lg font-bold text-orange-700 dark:text-orange-400">{unavailableTitle}</div>
          <div className="mt-2 max-w-md text-center text-sm text-orange-600 dark:text-orange-300">{unavailableDesc}</div>
        </div>
      )}
    </div>
  );
}
