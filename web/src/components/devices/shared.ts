import { useSyncExternalStore } from "react";
import { message } from "../ui";
import type { DeviceDetail, DeviceModem, ModemPnn } from "./types";
import { tl } from "../../lib/i18n";
import { lookupCarrier } from "../../lib/carrier";
import { notifyUnauthorized } from "../../api";

/* ---------------------------------------------------------------------------
 * Lifecycle / status helpers (ported from the VoHive reference).
 * ------------------------------------------------------------------------- */

// Wt: phases where the control plane is recovering.
export function isRecoveringPhase(phase?: string): boolean {
  return (
    phase === "rebooting" ||
    phase === "usb_wait" ||
    phase === "worker_starting" ||
    phase === "qmi_starting" ||
    phase === "recovering" ||
    phase === "evicting"
  );
}

// ra: human label for a lifecycle phase.
export function lifecycleLabel(phase?: string): string {
  switch (phase) {
    case "rebooting":
      return tl("重启中");
    case "usb_wait":
      return tl("等待设备重新枚举");
    case "worker_starting":
      return tl("设备启动中");
    case "qmi_starting":
      return tl("QMI 启动中");
    case "recovering":
      return tl("控制面恢复中");
    case "degraded":
      return tl("控制面不稳定");
    case "evicting":
      return tl("重新接管中");
    case "online":
      return tl("在线");
    case "offline":
      return tl("离线");
    default:
      return "";
  }
}

// qt: device is online (running + responsive control plane) and not recovering.
export function isDeviceOnline(device?: { running?: boolean; controlOnline?: boolean; healthy?: boolean; lifecyclePhase?: string } | null): boolean {
  if (!device || isRecoveringPhase(device.lifecyclePhase)) return false;
  return !!device.running && (device.controlOnline ?? device.healthy) === true;
}

// Ia: registered on the network (reg_status 1=home, 5=roaming).
export function isRegistered(device?: { modem?: { regStatus?: number } } | null): boolean {
  const s = device?.modem?.regStatus;
  return s === 1 || s === 5;
}

// The stored device flag is the desired policy, while runtime.enabled is the
// live owner of RF/IKE/IMS. A stale desired flag must not replace a healthy
// cellular overview with an all-red "disabled" VoWiFi pipeline.
export function isVoWiFiInUse(device?: {
  vowifiEnabled?: boolean;
  vowifiRuntime?: { enabled?: boolean };
} | null): boolean {
  if (!device?.vowifiEnabled) return false;
  return device.vowifiRuntime?.enabled !== false;
}

export interface StatusMeta {
  label: string;
  tag: "success" | "warning" | "danger";
  tone: "success" | "warning" | "danger";
  animated: boolean;
}

// us: status meta for a device list item.
export function deviceStatusMeta(device?: { running?: boolean; controlOnline?: boolean; healthy?: boolean; lifecyclePhase?: string } | null): StatusMeta {
  const phase = device?.lifecyclePhase;
  if (isRecoveringPhase(phase)) return { label: lifecycleLabel(phase) || tl("恢复中"), tag: "warning", tone: "warning", animated: true };
  if (phase === "degraded") return { label: tl("不稳定"), tag: "warning", tone: "warning", animated: true };
  if (device?.running) {
    return isDeviceOnline(device)
      ? { label: tl("在线"), tag: "success", tone: "success", animated: true }
      : { label: tl("恢复中"), tag: "warning", tone: "warning", animated: true };
  }
  return { label: tl("离线"), tag: "danger", tone: "danger", animated: false };
}

/* ---------------------------------------------------------------------------
 * Clipboard copy (Ea).
 * ------------------------------------------------------------------------- */
export async function copyText(value: unknown, msg = tl("已复制")): Promise<boolean> {
  const text = String(value ?? "").trim();
  if (!text) return false;
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      message.success(msg);
      return true;
    }
  } catch {
    /* fall through */
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "true");
    ta.className = "fixed left-0 top-0 h-px w-px opacity-0 pointer-events-none";
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    ta.setSelectionRange(0, ta.value.length);
    const ok = document.execCommand("copy");
    document.body.removeChild(ta);
    if (ok) {
      message.success(msg);
      return true;
    }
  } catch {
    /* fall through */
  }
  message.warning(tl("浏览器限制，请手动复制"));
  return false;
}

/* ---------------------------------------------------------------------------
 * Sensitive-info visibility store (vohive_show_sensitive), shared across tabs.
 * ------------------------------------------------------------------------- */
const SENSITIVE_KEY = "vocat_show_sensitive";
let sensitiveValue = readSensitive();
const sensitiveListeners = new Set<() => void>();

function readSensitive(): boolean {
  try {
    return typeof window === "undefined" ? false : window.localStorage.getItem(SENSITIVE_KEY) === "1";
  } catch {
    return false;
  }
}
function writeSensitive(value: boolean) {
  try {
    if (typeof window !== "undefined") window.localStorage.setItem(SENSITIVE_KEY, value ? "1" : "0");
  } catch {
    /* ignore */
  }
}
function setSensitive(value: boolean) {
  if (sensitiveValue === value) return;
  sensitiveValue = value;
  writeSensitive(value);
  for (const listener of sensitiveListeners) listener();
}
function subscribeSensitive(listener: () => void) {
  sensitiveListeners.add(listener);
  return () => {
    sensitiveListeners.delete(listener);
  };
}

export function useShowSensitive(): [boolean, () => void] {
  const value = useSyncExternalStore(subscribeSensitive, () => sensitiveValue);
  return [value, () => setSensitive(!sensitiveValue)];
}

/* ---------------------------------------------------------------------------
 * Control-device backend detection (Ut): wwan<N>qmi<N> => fixed QMI backend.
 * ------------------------------------------------------------------------- */
export function isQmiControl(controlDevice?: string): boolean {
  const s = String(controlDevice || "").trim();
  if (!s) return false;
  const base = s.replace(/\\/g, "/").split("/").filter(Boolean).pop() || s;
  return /^wwan\d+qmi\d+$/.test(base);
}

/* ---------------------------------------------------------------------------
 * Signal helpers (5-bar overview variant).
 * Thresholds are RSSI-calibrated (AT+CSQ style dBm, range ~-113..-51), NOT
 * RSRP — RSSI runs ~20 dB hotter than RSRP on LTE, so RSRP-scaled thresholds
 * peg near-full for any real signal and the bars never move with strength.
 * ------------------------------------------------------------------------- */
export function signalValid(dbm?: number | null): boolean {
  return typeof dbm === "number" && Number.isFinite(dbm) && dbm !== 0 && dbm !== -999;
}
// 0..5 bars. RSSI dBm bands: ≥-70 excellent · -70..-85 good · -85..-100 fair · -100..-110 poor · <-110 edge
export function signalLevel(dbm?: number | null): number {
  if (!signalValid(dbm)) return 0;
  const d = dbm as number;
  return d >= -70 ? 5 : d >= -85 ? 4 : d >= -100 ? 3 : d >= -110 ? 2 : 1;
}
export type SignalTone = "green" | "amber" | "red" | "gray";
export function signalTone(dbm?: number | null): SignalTone {
  if (!signalValid(dbm)) return "gray";
  const d = dbm as number;
  return d >= -85 ? "green" : d >= -100 ? "amber" : "red";
}

/* ---------------------------------------------------------------------------
 * SIM operator display (native SPN / OPL+PNN / PLMN).
 * ------------------------------------------------------------------------- */
function plmnOf(modem?: DeviceModem): string {
  const mcc = String(modem?.nativeMcc ?? "").trim();
  const mnc = String(modem?.nativeMnc ?? "").trim();
  return mcc && mnc ? `${mcc}${mnc}` : "";
}
function pnnName(pnn?: ModemPnn): string {
  return String(pnn?.fullName ?? "").trim() || String(pnn?.shortName ?? "").trim();
}
function firstPnnName(list?: ModemPnn[]): string {
  if (!Array.isArray(list)) return "";
  for (const item of list) {
    const name = pnnName(item);
    if (name) return name;
  }
  return "";
}
// $t: PLMN match supporting "x" wildcards.
function plmnMatch(pattern: unknown, plmn: string): boolean {
  const p = String(pattern ?? "").trim().toLowerCase();
  if (!p || !plmn) return false;
  if (p === plmn) return true;
  if (!p.includes("x")) return p.length < plmn.length && plmn.startsWith(p);
  if (p.length !== plmn.length) return false;
  for (let i = 0; i < p.length; i++) if (p[i] !== "x" && p[i] !== plmn[i]) return false;
  return true;
}
// Ct: derive operator name from OPL -> PNN records.
function oplPnnName(modem?: DeviceModem): string {
  const plmn = plmnOf(modem);
  if (!plmn || !Array.isArray(modem?.opl) || !Array.isArray(modem?.pnn)) return "";
  for (const opl of modem!.opl!) {
    if (!plmnMatch(opl?.plmn, plmn)) continue;
    const record = Number(opl?.pnnRecord ?? 0);
    if (!record) continue;
    const name = pnnName(modem!.pnn!.find((p) => p.record === record));
    if (name) return name;
  }
  return "";
}

function withPlmn(name: string, plmn: string): string {
  return plmn ? `${name} (${plmn})` : name;
}

// cardPlmnOf: the SIM's home PLMN from the card MCC/MNC (last-resort display).
function cardPlmnOf(modem?: DeviceModem): string {
  const mcc = String(modem?.cardMcc ?? "").trim();
  const mnc = String(modem?.cardMnc ?? "").trim();
  return mcc && mnc ? `${mcc}${mnc}` : "";
}

// Dt: the "原运营商" display string.
export function simOperatorDisplay(device?: DeviceDetail | null): string {
  const modem = device?.modem;
  if (!device) return "--";
  const spn = String(modem?.nativeSpn ?? "").trim();
  const name = oplPnnName(modem) || firstPnnName(modem?.pnn);
  const plmn = plmnOf(modem);
  // "Original Carrier" means the IMSI home/authentication network. EF_SPN is
  // only a profile-supplied display brand (travel eSIMs and MVNOs may put their
  // storefront name there), so it must not override a known home PLMN.
  const resolvedName = String(modem?.homeCarrierName ?? "").trim();
  const resolvedPLMN = String(modem?.homeCarrierPlmn ?? "").trim();
  if (resolvedName) return withPlmn(resolvedName, resolvedPLMN);
  const carrier = lookupCarrier(modem?.imsi);
  if (carrier) return withPlmn(carrier.name, carrier.mcc + carrier.mnc);
  // If the bundled carrier database cannot resolve the home PLMN, retain the
  // card's own labels as graceful, data-driven fallbacks.
  if (spn) return withPlmn(spn, cardPlmnOf(modem));
  if (name) return withPlmn(name, plmn);
  if (plmn) return plmn;
  const cardPlmn = cardPlmnOf(modem);
  if (cardPlmn) return cardPlmn;
  return "--";
}

/* ---------------------------------------------------------------------------
 * Minimal SSE reader (fetch + ReadableStream), cookie-authenticated.
 * onData fires per `data:` line; onEvent fires per blank-line dispatch.
 * ------------------------------------------------------------------------- */
export interface EventStreamHandlers {
  onEvent?: (event: string, data: string) => void;
  onData?: (data: string) => void;
  signal?: AbortSignal;
}

export async function readEventStream(
  path: string,
  params: Record<string, string | undefined>,
  handlers: EventStreamHandlers,
): Promise<void> {
  const qs = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") qs.set(key, value);
  }
  const query = qs.toString();
  const url = `${path.startsWith("/api") ? path : `/api${path}`}${query ? `?${query}` : ""}`;
  const response = await fetch(url, {
    method: "GET",
    headers: { Accept: "text/event-stream" },
    credentials: "include",
    signal: handlers.signal,
  });
  if (response.status === 401) notifyUnauthorized();
  if (!response.ok) throw new Error((await response.text()) || `HTTP ${response.status}`);
  if (!response.body) throw new Error("No stream body");

  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8");
  let buffer = "";
  let event = "";
  let dataLines: string[] = [];
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    for (;;) {
      const idx = buffer.indexOf("\n");
      if (idx < 0) break;
      let line = buffer.slice(0, idx);
      buffer = buffer.slice(idx + 1);
      if (line.endsWith("\r")) line = line.slice(0, -1);
      if (line === "") {
        const data = dataLines.join("\n");
        if (event && handlers.onEvent) handlers.onEvent(event, data);
        event = "";
        dataLines = [];
        continue;
      }
      if (line.startsWith("event:")) {
        event = line.slice(6).trim();
        continue;
      }
      if (line.startsWith("data:")) {
        const payload = line.slice(5).replace(/^\s*/, "");
        dataLines.push(payload);
        handlers.onData?.(payload);
        continue;
      }
    }
  }
}
