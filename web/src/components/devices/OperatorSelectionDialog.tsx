import { useEffect, useRef, useState } from "react";
import { api, apiMessage } from "../../api";
import { cx } from "../../lib/utils";
import { Button, Modal, message } from "../ui";
import { readEventStream } from "./shared";
import { CandidateRow } from "./CandidateRow";
import type { OperatorCandidate } from "./types";
import { tf, useI18n } from "../../lib/i18n";

interface ScanState {
  scanId?: string;
  status?: string;
  candidates?: OperatorCandidate[];
  message?: string;
  error?: string;
  retryable?: boolean;
}
interface CurrentSelection {
  mode?: string;
  plmn?: string;
}

function ratsText(c: OperatorCandidate): string {
  const list = (c.rats || []).filter(Boolean) as string[];
  return list.length ? list.map((r) => r.toUpperCase()).join(" / ") : "--";
}
function firstRat(c: OperatorCandidate): string | undefined {
  return (c.rats || []).find((r) => !!r) || undefined;
}

export interface OperatorSelectionDialogProps {
  open: boolean;
  deviceId: string;
  scanBlockedReason?: string;
  onClose: () => void;
  onUpdated: () => void;
}

export function OperatorSelectionDialog({ open, deviceId, scanBlockedReason = "", onClose, onUpdated }: OperatorSelectionDialogProps) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [current, setCurrent] = useState<CurrentSelection | null>(null);
  const [scan, setScan] = useState<ScanState | null>(null);
  const [registering, setRegistering] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const registerAbortRef = useRef<AbortController | null>(null);
  const scanInFlightRef = useRef(false);
  const notifiedRef = useRef("");

  const scanning = scan?.status === "running";
  const candidates = scan?.candidates || [];
  const scanMessage = scan?.message || scanBlockedReason;
  const errorText = scan?.retryable ? "" : scan?.error || "";
  const retryable = !!scan?.retryable || !!scanBlockedReason;
  function stopStream() {
    abortRef.current?.abort();
    abortRef.current = null;
    scanInFlightRef.current = false;
  }

  async function loadCurrent() {
    if (!deviceId) return;
    setBusy(true);
    try {
      const data = await api<CurrentSelection>(`/devices/${deviceId}/operator_selection`);
      setCurrent(data || null);
    } catch (e) {
      message.error(apiMessage(e) || t("加载当前配置失败"));
    } finally {
      setBusy(false);
    }
  }

  function startStream() {
    if (!deviceId || scanInFlightRef.current) return;
    if (scanBlockedReason) {
      setScan({ status: "blocked", message: scanBlockedReason, retryable: true });
      return;
    }
    stopStream();
    const controller = new AbortController();
    abortRef.current = controller;
    scanInFlightRef.current = true;
    setScan({ status: "running", message: t("正在请求模组扫描可用网络...") });
    readEventStream(
      `/devices/${deviceId}/operator_selection/scan/stream`,
      {},
      {
        signal: controller.signal,
        onEvent: (event, data) => {
          if (event !== "operator_scan") return;
          try {
            const parsed = JSON.parse(data) as ScanState;
            setScan(parsed);
            if (parsed.status !== "running") stopStream();
          } catch {
            /* ignore */
          }
        },
      },
    ).catch((e) => {
      scanInFlightRef.current = false;
      if (!controller.signal.aborted) message.error(apiMessage(e) || t("扫描网络失败"));
    });
  }

  async function restoreAuto() {
    const controller = new AbortController();
    registerAbortRef.current = controller;
    setRegistering(t("正在恢复自动选网..."));
    setBusy(true);
    try {
      await api(`/devices/${deviceId}/operator_selection`, { method: "POST", body: { mode: "automatic" }, signal: controller.signal });
      message.success(t("已恢复自动选网"));
      onUpdated();
      await loadCurrent();
    } catch (e) {
      if (controller.signal.aborted) message.info(t("已取消"));
      else message.error(apiMessage(e) || t("设置失败"));
    } finally {
      setRegistering(null);
      registerAbortRef.current = null;
      setBusy(false);
    }
  }

  async function reRegister() {
	const controller = new AbortController();
	registerAbortRef.current = controller;
	setRegistering(t("正在按当前选网配置重新驻网，请稍候..."));
	setBusy(true);
	try {
	  await api(`/devices/${deviceId}/operator_selection/reregister`, { method: "POST", signal: controller.signal });
	  message.success(t("已重新发起驻网"));
	  onUpdated();
	  await loadCurrent();
	} catch (e) {
	  if (controller.signal.aborted) message.info(t("已取消"));
	  else message.error(apiMessage(e) || t("重新驻网失败"));
	} finally {
	  setRegistering(null);
	  registerAbortRef.current = null;
	  setBusy(false);
	}
  }

  async function lock(c: OperatorCandidate) {
    const controller = new AbortController();
    registerAbortRef.current = controller;
    setRegistering(tf("正在注册到 {plmn}，请稍候（可能需要 1-2 分钟）...", { plmn: c.plmn }));
    setBusy(true);
    try {
      await api(`/devices/${deviceId}/operator_selection`, {
        method: "POST",
        body: { mode: "manual", plmn: c.plmn, includesPcsDigit: c.includesPcsDigit, rat: firstRat(c) },
        signal: controller.signal,
      });
      message.success(tf("已锁定网络 {plmn}", { plmn: c.plmn }));
      onUpdated();
      await loadCurrent();
    } catch (e) {
      if (controller.signal.aborted) message.info(t("已取消"));
      else message.error(apiMessage(e) || t("设置失败"));
    } finally {
      setRegistering(null);
      registerAbortRef.current = null;
      setBusy(false);
    }
  }

  function cancelRegister() {
    registerAbortRef.current?.abort();
  }

  useEffect(() => {
    if (open) {
      setScan(null);
      notifiedRef.current = "";
      loadCurrent();
    } else {
      stopStream();
      registerAbortRef.current?.abort();
      registerAbortRef.current = null;
      setRegistering(null);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, deviceId]);

  // Notify on scan completion transitions.
  useEffect(() => {
    if (!scan || !open) return;
    const key = `${scan.scanId}:${scan.status}`;
    if (key === notifiedRef.current) return;
    if (scan.status === "complete") message.success(t("运营商扫描完成"));
    notifiedRef.current = key;
  }, [scan, open]);

  useEffect(() => () => {
    stopStream();
    registerAbortRef.current?.abort();
  }, []);
  return (
    <Modal open={open} onClose={onClose} title={t("运营商网络选择")}>
      <div className="mt-2 text-sm text-gray-600 dark:text-gray-300">
        <div className="mb-4 rounded-lg border border-gray-100 bg-gray-50 p-4 dark:border-white/5 dark:bg-white/5">
          <div className="mb-2 flex items-center justify-between">
            <span className="font-medium text-gray-700 dark:text-gray-200">{t("当前模式")}</span>
            <span
              className={cx(
                "rounded px-2 py-0.5 text-xs font-medium",
                current?.mode === "automatic"
                  ? "bg-indigo-100 text-indigo-700 dark:bg-indigo-500/20 dark:text-indigo-300"
                  : "bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-300",
              )}
            >
              {current?.mode === "automatic" ? t("自动") : t("手动锁定")}
            </span>
          </div>
          {current?.mode === "manual" ? (
            <div className="flex items-center justify-between">
              <span className="text-gray-500">{t("已锁定 PLMN")}</span>
              <span className="font-mono text-gray-900 dark:text-white">{current.plmn || "--"}</span>
            </div>
          ) : null}
        </div>
        <div className="mb-4 grid grid-cols-1 gap-3 sm:grid-cols-3">
          <Button variant="primary" plain onClick={startStream} loading={scanning} disabled={busy || !!scanBlockedReason} className="flex-1">
            {scanning ? t("扫描中...") : t("扫描可用网络")}
          </Button>
          <Button onClick={reRegister} disabled={busy} className="flex-1">
            {t("重新驻网")}
          </Button>
          <Button onClick={restoreAuto} disabled={busy || current?.mode === "automatic"} className="flex-1">
            {t("恢复自动选网")}
          </Button>
        </div>
        <div className="mb-4 text-xs leading-5 text-gray-500 dark:text-gray-400">
          {t("扫描结果只代表模组在当前位置实际收到的运营商信号，不代表模组支持的全部运营商；禁用网络表示当前 SIM 不允许注册。")}
        </div>
        {registering ? (
          <div className="mb-4 flex items-center justify-between gap-3 rounded-lg border border-indigo-200 bg-indigo-50 px-3 py-2 text-xs text-indigo-700 dark:border-indigo-500/20 dark:bg-indigo-500/10 dark:text-indigo-300">
            <span className="flex items-center gap-2">
              <span className="inline-block h-3 w-3 animate-spin rounded-full border-2 border-indigo-400 border-t-transparent" />
              {registering}
            </span>
            <button type="button" onClick={cancelRegister} className="shrink-0 rounded px-2 py-0.5 font-medium text-indigo-700 hover:bg-indigo-100 dark:text-indigo-300 dark:hover:bg-indigo-500/20">
              {t("取消")}
            </button>
          </div>
        ) : scanning || scanMessage || errorText ? (
          <div
            className={cx(
              "mb-4 rounded-lg border px-3 py-2 text-xs",
              errorText
                ? "border-red-200 bg-red-50 text-red-700 dark:border-red-500/20 dark:bg-red-500/10 dark:text-red-300"
                : retryable
                  ? "border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-500/20 dark:bg-amber-500/10 dark:text-amber-300"
                : "border-indigo-200 bg-indigo-50 text-indigo-700 dark:border-indigo-500/20 dark:bg-indigo-500/10 dark:text-indigo-300",
            )}
          >
            {errorText || scanMessage}
          </div>
        ) : null}
        {candidates.length > 0 ? (
          <div className={cx("max-h-[min(55vh,440px)] divide-y divide-gray-200 overflow-y-auto rounded-lg border border-gray-200 dark:divide-white/10 dark:border-white/10", (!!registering || busy) && "pointer-events-none opacity-60")}>
            {candidates.map((c) => (
              <CandidateRow key={`${c.plmn}-${ratsText(c)}`} candidate={c} onLock={lock} />
            ))}
          </div>
        ) : scanning ? (
          <div className="flex flex-col items-center justify-center space-y-3 py-8 text-center text-gray-500">
            <span>{t("正在搜索周围网络，这可能需要 1-3 分钟...")}</span>
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
