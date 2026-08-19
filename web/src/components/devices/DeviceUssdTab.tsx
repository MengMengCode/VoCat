import { useState } from "react";
import { CallRegular } from "@fluentui/react-icons";
import { api } from "../../api";
import { Button, Input } from "../ui";
import { UssdLogEntry, type UssdLogItem } from "./UssdLogEntry";
import { AtTypingBubble } from "./AtLogEntry";
import { tf, useI18n } from "../../lib/i18n";

interface UssdResult {
  status?: string;
  text?: string;
  rawText?: string;
  dcs?: number;
  sessionId?: string;
  channel?: string;
}

export function DeviceUssdTab({ deviceId }: { deviceId: string }) {
  const { t } = useI18n();
  const [input, setInput] = useState("");
  const [timeoutMs, setTimeoutMs] = useState<number>(45000);
  const [loading, setLoading] = useState(false);
  const [sessionId, setSessionId] = useState("");
  const [channel, setChannel] = useState("");
  const [log, setLog] = useState<UssdLogItem[]>([]);

  const inSession = !!sessionId;
  const placeholder = inSession ? t("输入菜单选项数字") : t("例如 *100# 或菜单回复数字");

  function clearSession() {
    setSessionId("");
    setChannel("");
  }

  async function callUssd(command: string): Promise<UssdResult> {
    const body = inSession
      ? { sessionId, input: command, timeoutMs: timeoutMs || 45000 }
      : { command, timeoutMs: timeoutMs || 45000 };
    const path = inSession ? `/devices/${deviceId}/actions/ussd/continue` : `/devices/${deviceId}/actions/ussd`;
    const res = await api<{ result?: Record<string, unknown>; channel?: string }>(path, { method: "POST", body });
    const r = (res?.result || {}) as Record<string, unknown>;
    return {
      status: r.status as string | undefined,
      text: (r.text as string) || "",
      rawText: ((r.rawText as string) || (r.rawXml as string) || "") as string,
      dcs: r.dcs as number | undefined,
      sessionId: (r.sessionId as string) || "",
      channel: res?.channel || "",
    };
  }

  async function send() {
    const command = String(input || "").trim();
    if (!command) return;
    setLog((prev) => [...prev, { ts: Date.now(), type: "req", content: command }]);
    setLoading(true);
    setInput("");
    try {
      const v = await callUssd(command);
      if (v.channel) setChannel(v.channel);
      const text = v.text || v.rawText || t("[空响应]");
      if (v.status === "failed") {
        setLog((prev) => [...prev, { ts: Date.now(), type: "err", content: tf("[网络不支持/无响应]\n{text}", { text }), dcs: v.dcs, channel: v.channel }]);
        clearSession();
      } else if (v.status === "terminated") {
        setLog((prev) => [...prev, { ts: Date.now(), type: "err", content: tf("[被网络终止]\n{text}", { text }), dcs: v.dcs, channel: v.channel }]);
        clearSession();
      } else {
        setLog((prev) => [...prev, { ts: Date.now(), type: "res", content: text, dcs: v.dcs, channel: v.channel }]);
        if (v.status === "awaiting_input" && v.sessionId) setSessionId(v.sessionId);
        else clearSession();
      }
    } catch (e) {
      setLog((prev) => [...prev, { ts: Date.now(), type: "err", content: e instanceof Error ? e.message : t("请求异常") }]);
      clearSession();
    } finally {
      setLoading(false);
    }
  }

  async function cancelSession() {
    if (!sessionId) return;
    try {
      await api(`/devices/${deviceId}/actions/ussd/cancel`, { method: "POST", body: { sessionId } });
      setLog((prev) => [...prev, { ts: Date.now(), type: "sys", content: t("会话已手动取消") }]);
    } catch {
      /* ignore */
    }
    clearSession();
  }

  return (
    <div>
      <div className="flex items-center gap-3">
        <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-indigo-50 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-400">
          <CallRegular className="text-[22px]" />
        </div>
        <div className="flex-1">
          <div className="text-lg font-bold text-gray-900 dark:text-white">{t("USSD 交互终端")}</div>
          <div className="mt-0.5 text-sm text-gray-500 dark:text-gray-400">{t("发送 USSD 代码 (如 *100#) 并等待网络菜单响应")}</div>
        </div>
        {inSession ? (
          <div className="flex items-center gap-2">
            <span className="inline-flex items-center gap-1.5 rounded-full border border-emerald-200 bg-emerald-50 px-2.5 py-1 text-xs font-medium text-emerald-600 dark:border-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-400">
              <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-emerald-500" />
              {t("多轮会话中")}
            </span>
            <Button variant="warning" size="small" plain onClick={cancelSession} disabled={loading}>
              {t("取消会话")}
            </Button>
          </div>
        ) : null}
      </div>
      <div className="ui-panel-muted relative mt-4 flex h-[320px] flex-col gap-3 overflow-auto rounded-xl border border-gray-100 p-4 dark:border-white/10">
        {log.length === 0 && !loading ? (
          <div className="absolute inset-0 flex items-center justify-center text-sm text-gray-400">{t("暂无 USSD 会话记录")}</div>
        ) : null}
        {log.map((item, i) => (
          <UssdLogEntry key={i} item={item} />
        ))}
        {loading ? <AtTypingBubble label={t("等待网络响应...")} /> : null}
      </div>
      <div className="mt-4 grid grid-cols-1 gap-3 md:grid-cols-[1fr_110px_auto]">
        <div className="space-y-1">
          <div className="text-[11px] font-bold uppercase tracking-wider text-gray-500">{inSession ? t("菜单回复") : t("命令 / 回复")}</div>
          <Input
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder={placeholder}
            disabled={loading}
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
            placeholder="45000"
          />
        </div>
        <div className="space-y-1 self-end">
          <div className="select-none text-[11px] font-bold uppercase tracking-wider opacity-0">{t("操作")}</div>
          <div className="flex items-center justify-end gap-2">
            <Button
              onClick={() => {
                setLog([]);
                clearSession();
              }}
            >
              {t("清空")}
            </Button>
            <Button variant="primary" loading={loading} disabled={!input} onClick={send} className="!border-0">
              {inSession ? t("回复") : t("发送")}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
