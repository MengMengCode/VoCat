import { useCallback, useEffect, useRef, useState } from "react";
import { api, callMediaSocketURL, deviceCallAction, deviceCalls } from "../api";
import { CallMediaBridge } from "../lib/callBridge";
import { cx } from "../lib/utils";

interface DeviceEntry {
  id: string;
  name?: string;
  running?: boolean;
}

interface CallEntry {
  id: string;
  number: string;
  direction: string;
  state: string;
  startedAt?: string;
  answeredAt?: string;
  sipCode?: number;
  reason?: string;
  mediaReady?: boolean;
  codec?: string;
  endedAt?: string;
}

const KEYPAD = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"];

export default function CallPage() {
  const [devices, setDevices] = useState<DeviceEntry[]>([]);
  const [deviceId, setDeviceId] = useState("");
  const [number, setNumber] = useState("");
  const [calls, setCalls] = useState<CallEntry[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [bridgeState, setBridgeState] = useState<"idle" | "connecting" | "open" | "closed" | "error">("idle");
  const [micOn, setMicOn] = useState(false);
  const bridgeRef = useRef<CallMediaBridge | null>(null);
  const activeCallIdRef = useRef<string | null>(null);

  useEffect(() => {
    api<{ devices?: DeviceEntry[] }>("/devices")
      .then((result) => {
        const list = result.devices ?? [];
        setDevices(list);
        if (list.length > 0) setDeviceId((current) => current || list[0].id);
      })
      .catch(() => setError("无法加载设备列表"));
  }, []);

  const refreshCalls = useCallback(() => {
    if (!deviceId) return;
    deviceCalls(deviceId)
      .then((result) => {
        const list = result.calls ?? [];
        setCalls(Array.isArray(list) ? (list as CallEntry[]) : []);
        setError("");
      })
      .catch((e) => {
        setError(e instanceof Error ? e.message : "无法获取通话状态");
      });
  }, [deviceId]);

  useEffect(() => {
    refreshCalls();
    const timer = window.setInterval(refreshCalls, 2000);
    return () => window.clearInterval(timer);
  }, [refreshCalls]);

  const ensureBridge = useCallback(() => {
    if (bridgeRef.current === null) {
      bridgeRef.current = new CallMediaBridge({
        onState: (state) => setBridgeState(state),
        onError: (message) => setError(message),
      });
    }
    return bridgeRef.current;
  }, []);

  useEffect(() => {
    const mediaCall = calls.find((call) => call.mediaReady && !call.endedAt);
    const shouldAttach = mediaCall !== undefined && bridgeState === "idle";
    if (shouldAttach && activeCallIdRef.current !== mediaCall.id) {
      activeCallIdRef.current = mediaCall.id;
      const bridge = ensureBridge();
      bridge
        .connect(callMediaSocketURL(deviceId, mediaCall.id))
        .catch((e) => setError(e instanceof Error ? e.message : "音频连接失败"));
    }
    if (mediaCall === undefined && bridgeState === "open") {
      activeCallIdRef.current = null;
      bridgeRef.current?.disconnect();
      setMicOn(false);
    }
  }, [calls, bridgeState, deviceId, ensureBridge]);

  useEffect(() => {
    if (bridgeState === "error") {
      setError("音频连接失败：可能通话尚未接通，或 IMS 媒体未就绪。请确认状态为“通话中”后重试。");
    }
  }, [bridgeState]);

  useEffect(() => () => bridgeRef.current?.destroy(), []);

  async function runAction(action: "dial" | "answer" | "hangup", callId?: string) {
    setBusy(true);
    setError("");
    try {
      await deviceCallAction(
        deviceId,
        action,
        action === "dial" ? { number } : { call_id: callId },
      );
      setTimeout(refreshCalls, 500);
    } catch (e) {
      setError(e instanceof Error ? e.message : `${action} 失败`);
    } finally {
      setBusy(false);
    }
  }

  async function toggleMic() {
    const next = !micOn;
    setMicOn(next);
    try {
      await ensureBridge().setMicActive(next);
    } catch (e) {
      setError(e instanceof Error ? e.message : "麦克风切换失败");
      setMicOn(false);
    }
  }

  const incoming = calls.find((call) => call.direction === "incoming" && !call.endedAt && !call.mediaReady);
  const active = calls.find((call) => call.mediaReady && !call.endedAt);

  function connectAudio() {
    if (!active) return;
    activeCallIdRef.current = active.id;
    ensureBridge()
      .connect(callMediaSocketURL(deviceId, active.id))
      .catch((e) => setError(e instanceof Error ? e.message : "音频连接失败"));
  }

  function startListening() {
    ensureBridge().resumeAudio();
    if (bridgeState === "idle" && active) connectAudio();
  }

  return (
    <div className="mx-auto w-full max-w-3xl space-y-4 p-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold text-gray-900 dark:text-gray-100">语音网关</h1>
        <select
          value={deviceId}
          onChange={(event) => setDeviceId(event.target.value)}
          className="rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm dark:border-gray-600 dark:bg-[#1b1b21] dark:text-gray-100"
        >
          {devices.length > 0 ? (
            devices.map((device) => (
              <option key={device.id} value={device.id}>
                {device.name || device.id}
              </option>
            ))
          ) : (
            <option value="">未发现设备</option>
          )}
        </select>
      </div>

      {devices.length === 0 && (
        <div className="flex items-center gap-2">
          <input
            type="text"
            value={deviceId}
            onChange={(event) => setDeviceId(event.target.value.trim())}
            placeholder="未发现设备时手动输入设备 ID（如 410）"
            className="w-full rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm dark:border-gray-600 dark:bg-[#1b1b21] dark:text-gray-100"
          />
        </div>
      )}

      {error !== "" && (
        <div className="rounded-lg border border-red-300 bg-red-50 px-4 py-2 text-sm text-red-700 dark:border-red-800 dark:bg-red-950 dark:text-red-300">
          {error}
        </div>
      )}

      <div className="rounded-2xl border border-gray-200 bg-white p-6 shadow-sm dark:border-gray-700 dark:bg-[#16161c]">
        <div className="mb-4 flex items-center justify-between rounded-lg bg-gray-100 px-4 py-3 dark:bg-[#1e1e26]">
          <span className="text-2xl font-mono tracking-widest text-gray-800 dark:text-gray-100">{number || "—"}</span>
          {active ? (
            <span className="rounded-full bg-green-100 px-3 py-1 text-sm font-medium text-green-700 dark:bg-green-900 dark:text-green-300">
              通话中
            </span>
          ) : incoming ? (
            <span className="rounded-full bg-amber-100 px-3 py-1 text-sm font-medium text-amber-700 dark:bg-amber-900 dark:text-amber-300">
              来电
            </span>
          ) : (
            <span className="rounded-full bg-gray-200 px-3 py-1 text-sm font-medium text-gray-600 dark:bg-gray-700 dark:text-gray-300">
              待机
            </span>
          )}
        </div>

        <div className="grid grid-cols-3 gap-2">
          {KEYPAD.map((key) => (
            <button
              key={key}
              type="button"
              onClick={() => setNumber((current) => current + key)}
              className="h-14 rounded-xl bg-gray-100 text-lg font-medium text-gray-800 transition hover:bg-gray-200 active:scale-95 dark:bg-[#22222a] dark:text-gray-100 dark:hover:bg-[#2a2a34]"
            >
              {key}
            </button>
          ))}
          <button
            type="button"
            onClick={() => setNumber((current) => current + "+")}
            className="h-14 rounded-xl bg-gray-100 text-lg font-medium text-gray-800 transition hover:bg-gray-200 active:scale-95 dark:bg-[#22222a] dark:text-gray-100 dark:hover:bg-[#2a2a34]"
          >
            +
          </button>
          <button
            type="button"
            onClick={() => setNumber((current) => current.slice(0, -1))}
            className="h-14 rounded-xl bg-gray-100 text-lg text-gray-800 transition hover:bg-gray-200 active:scale-95 dark:bg-[#22222a] dark:text-gray-100 dark:hover:bg-[#2a2a34]"
            aria-label="退格"
          >
            ⌫
          </button>
          <button
            type="button"
            disabled={!number.trim() || busy}
            onClick={() => void runAction("dial")}
            className="h-14 rounded-xl bg-green-600 text-lg font-semibold text-white transition hover:bg-green-500 active:scale-95 disabled:cursor-not-allowed disabled:opacity-40"
          >
            拨号
          </button>
        </div>

        <div className="mt-4 flex items-center justify-center gap-3">
          {active && (
            <button
              type="button"
              onClick={startListening}
              className="animate-pulse rounded-full bg-green-600 px-6 py-3 font-semibold text-white transition hover:bg-green-500 active:scale-95"
            >
              点击开始收听（通话中）
            </button>
          )}
          {incoming && (
            <button
              type="button"
              disabled={busy}
              onClick={() => void runAction("answer", incoming.id)}
              className="rounded-full bg-amber-500 px-6 py-3 font-semibold text-white transition hover:bg-amber-400 active:scale-95 disabled:opacity-40"
            >
              接听
            </button>
          )}
          {active && (
            <>
              <button
                type="button"
                onClick={() => void toggleMic()}
                className={cx(
                  "rounded-full px-6 py-3 font-semibold transition active:scale-95",
                  micOn
                    ? "bg-blue-600 text-white hover:bg-blue-500"
                    : "bg-gray-200 text-gray-700 hover:bg-gray-300 dark:bg-[#22222a] dark:text-gray-100 dark:hover:bg-[#2a2a34]",
                )}
              >
                {micOn ? "静音关闭（说话中）" : "开启麦克风"}
              </button>
              <button
                type="button"
                disabled={busy}
                onClick={() => void runAction("hangup", active.id)}
                className="rounded-full bg-red-600 px-6 py-3 font-semibold text-white transition hover:bg-red-500 active:scale-95 disabled:opacity-40"
              >
                挂断
              </button>
            </>
          )}
        </div>
      </div>

      <div className="rounded-2xl border border-gray-200 bg-white p-4 shadow-sm dark:border-gray-700 dark:bg-[#16161c]">
        <div className="mb-2 flex items-center justify-between">
          <h2 className="font-medium text-gray-800 dark:text-gray-100">通话列表</h2>
          <span className="text-sm text-gray-500 dark:text-gray-400">
            音频:{" "}
            <span
              className={cx(
                "font-medium",
                bridgeState === "open" ? "text-green-600 dark:text-green-400" : bridgeState === "error" ? "text-red-600 dark:text-red-400" : "text-gray-500 dark:text-gray-400",
              )}
            >
              {bridgeState === "open" ? "已连接" : bridgeState === "connecting" ? "连接中" : bridgeState === "error" ? "错误" : bridgeState === "closed" ? "已关闭" : "未连接"}
            </span>
          </span>
        </div>
        {calls.length === 0 ? (
          <p className="py-6 text-center text-sm text-gray-400">暂无通话记录</p>
        ) : (
          <ul className="divide-y divide-gray-100 dark:divide-gray-800">
            {calls.map((call) => (
              <li key={call.id} className="flex items-center justify-between py-2 text-sm">
                <div>
                  <div className="font-medium text-gray-800 dark:text-gray-100">
                    {call.direction === "incoming" ? "来电" : "去电"} · {call.number || "未知"}
                  </div>
                  <div className="text-xs text-gray-500 dark:text-gray-400">
                    状态: {call.state}
                    {call.codec ? ` · 编码: ${call.codec}` : ""}
                    {call.sipCode ? ` · SIP: ${call.sipCode}` : ""}
                    {call.reason ? ` · ${call.reason}` : ""}
                  </div>
                </div>
                {call.mediaReady && !call.endedAt && (
                  <span className="rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-700 dark:bg-green-900 dark:text-green-300">
                    通话中
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
