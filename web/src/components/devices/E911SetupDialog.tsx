import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiMessage } from "../../api";
import { Button, Input, Modal } from "../ui";
import { useI18n } from "../../lib/i18n";

export interface E911Carrier {
  id: string;
  name: string;
  method: "att_nsds" | "msp_websheet" | "ts43_websheet" | "account" | "phone" | "unsupported";
  network?: string;
  verified?: boolean;
  helpUrl?: string;
  portalUrl?: string;
}
interface Address { street: string; unit: string; city: string; state: string; zip: string }
interface CarrierStatus {
  address?: Address;
  addressStatus: string;
  wifiCalling: boolean;
  locationAccepted: boolean;
  termsAccepted: boolean;
  confirmed: boolean;
  confirmation?: "carrier_status";
  expiresAt?: string;
}
interface Session {
  id?: string;
  carrier: E911Carrier;
  terms?: string;
  expiresAt?: string;
  status?: CarrierStatus;
  reconnectScheduled?: boolean;
  portalAvailable?: boolean;
  setupError?: string;
}
interface Candidate { address: Address; match: boolean }
const EMPTY: Address = { street: "", unit: "", city: "", state: "", zip: "" };
const US_STATES = "AL AK AZ AR CA CO CT DE DC FL GA HI ID IL IN IA KS KY LA ME MD MA MI MN MS MO MT NE NV NH NJ NM NY NC ND OH OK OR PA RI SC SD TN TX UT VT VA WA WV WI WY AS GU MP PR VI".split(" ");

export function E911SetupDialog({ open, deviceId, onClose, onDone }: {
  open: boolean; deviceId: string; onClose: () => void; onDone: () => void;
}) {
  const { t } = useI18n();
  const [session, setSession] = useState<Session | null>(null);
  const [address, setAddress] = useState<Address>(EMPTY);
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [selected, setSelected] = useState(0);
  const [accepted, setAccepted] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [outcome, setOutcome] = useState<"" | "confirmed" | "pending">("");
  const [generation, setGeneration] = useState(0);
  const activeSession = useRef("");

  useEffect(() => {
    if (!open || !deviceId) return;
    let cancelled = false;
    const controller = new AbortController();
    setSession(null); setAddress(EMPTY); setCandidates([]); setAccepted(false); setError(""); setOutcome(""); setBusy("start");
    void api<Session>(`/devices/${deviceId}/vowifi/e911`, { method: "POST", signal: controller.signal }).then(data => {
      if (cancelled) {
        if (data.id) void api(`/devices/${deviceId}/vowifi/e911/${data.id}`, { method: "DELETE" }).catch(() => {});
        return;
      }
      activeSession.current = data.id || "";
      setSession(data);
      setAddress(data.status?.address || EMPTY);
      setError(data.setupError || "");
    }).catch(e => { if (!cancelled) setError(apiMessage(e)); }).finally(() => { if (!cancelled) setBusy(""); });
    return () => {
      cancelled = true;
      controller.abort();
      if (activeSession.current) {
        void api(`/devices/${deviceId}/vowifi/e911/${activeSession.current}`, { method: "DELETE" }).catch(() => {});
        activeSession.current = "";
      }
    };
  }, [open, deviceId, generation]);

  const change = useCallback((key: keyof Address, value: string) => {
    setAddress(previous => ({ ...previous, [key]: value }));
    setCandidates([]); setOutcome(""); setError("");
  }, []);

  async function verify() {
    if (!session?.id) return;
    setBusy("verify"); setError(""); setOutcome(""); setCandidates([]);
    try {
      const result = await api<{ candidates: Candidate[] }>(`/devices/${deviceId}/vowifi/e911/${session.id}/verify`, { method: "POST", body: { address } });
      setCandidates(result.candidates);
      const exact = result.candidates.findIndex(candidate => candidate.match);
      setSelected(exact < 0 ? 0 : exact);
    } catch (e) { setError(apiMessage(e)); }
    finally { setBusy(""); }
  }

  async function submit(refresh = false) {
    if (!session?.id) return;
    setBusy(refresh ? "refresh" : "submit"); setError("");
    try {
      const result = await api<Session>(`/devices/${deviceId}/vowifi/e911/${session.id}/${refresh ? "refresh" : "submit"}`, {
        method: "POST", body: refresh ? undefined : { candidateIndex: selected, termsAccepted: accepted },
      });
      setSession(result);
      setOutcome(result.status?.confirmed ? "confirmed" : "pending");
      if (result.status?.confirmed) onDone();
    } catch (e) {
      setError(apiMessage(e));
      setOutcome("");
      if (refresh) setSession(previous => previous ? { ...previous, status: undefined, portalAvailable: false } : previous);
    }
    finally { setBusy(""); }
  }

  const valid = address.street.trim() && address.city.trim() && US_STATES.includes(address.state) && /^\d{5}(-\d{4})?$/.test(address.zip);
  const direct = session?.carrier.method === "att_nsds";
  const websheet = session?.carrier.method === "msp_websheet" || session?.carrier.method === "ts43_websheet";
  const close = () => { if (busy !== "submit") onClose(); };
  return (
    <Modal open={open} onClose={close} closeOnOverlay={busy !== "submit"} showClose={busy !== "submit"} title={t("E911地址设置")}>
      <div className="space-y-4 text-sm">
        {busy === "start" ? <div role="status" className="py-10 text-center text-gray-500">{t("正在识别运营商并读取E911状态...")}</div> : null}
        {session ? <div className="ui-panel-muted rounded-xl p-3">
          <div className="font-medium">{session.carrier.name || t("美国运营商")}</div>
          <p className="mt-1 text-xs text-gray-500">{direct ? t("通过SIM鉴权向运营商登记，提交后读取运营商结果。") : websheet ? t("使用当前SIM向运营商获取E911登记页面，填写地址后返回此处查询结果。") : session.carrier.method === "phone" ? t("此运营商需要在兼容手机的Wi-Fi Calling设置中登记地址。") : session.carrier.method === "account" ? t("此运营商通过账户门户登记E911地址，请登录对应线路完成设置。") : t("尚未适配此运营商的地址登记接口，请联系运营商确认开通方式。")}</p>
          {session.carrier.verified === false ? <p className="mt-2 text-xs text-amber-600 dark:text-amber-400">{t("此线路的自动登记已接入，尚未完成实卡验证。")}</p> : null}
          {session.status ? <div className="mt-2 text-xs text-gray-500">{t("运营商当前状态")}: {session.status.confirmed ? session.status.confirmation === "carrier_status" ? t("运营商已确认E911登记状态，Wi-Fi Calling已开通。") : t("地址已验证，Wi-Fi Calling已开通") : session.status.addressStatus === "NotRequired" && session.status.wifiCalling ? t("运营商未要求地址登记，Wi-Fi Calling已开通。") : t("地址或Wi-Fi Calling尚未完成开通")}</div> : null}
        </div> : null}
        {websheet && session?.id ? <div className="space-y-3">
          {session.portalAvailable ? <>
            <p className="text-xs text-gray-500">{t("在运营商页面填写真实地址并接受其条款。保存后返回此弹窗，点击刷新运营商状态。")}</p>
            <a className="inline-flex min-h-9 items-center rounded-lg bg-sky-600 px-4 font-medium text-white hover:bg-sky-700" href={`/api/devices/${encodeURIComponent(deviceId)}/vowifi/e911/${encodeURIComponent(session.id)}/portal`} target="_blank" rel="noopener noreferrer">{t("打开运营商E911登记页")}</a>
          </> : <p className="text-xs text-gray-500">{session.status?.confirmed ? t("运营商已确认E911登记状态，Wi-Fi Calling已开通。") : t("运营商未返回可用的E911登记页面，可通过下方账户入口或官方说明完成设置。")}</p>}
          <Button onClick={() => void submit(true)} loading={busy === "refresh"} disabled={!!busy}>{t("刷新运营商状态")}</Button>
        </div> : null}
        {session && (!direct || !session.id) ? <div className="flex flex-wrap gap-3">
          {session.carrier.portalUrl ? <a className="text-sky-600 hover:underline" href={session.carrier.portalUrl} target="_blank" rel="noopener noreferrer">{t("打开运营商账户")}</a> : null}
          {session.carrier.helpUrl ? <a className="text-sky-600 hover:underline" href={session.carrier.helpUrl} target="_blank" rel="noopener noreferrer">{t("查看官方设置说明")}</a> : null}
        </div> : null}
        {direct && session?.id && outcome !== "confirmed" ? <>
          <form onSubmit={event => { event.preventDefault(); void verify(); }} className="space-y-3">
            <div className="text-xs text-gray-500">{t("填写用于紧急呼叫定位的真实美国地址。")}</div>
            <label className="block space-y-1"><span>{t("街道及门牌号")}</span><Input autoComplete="address-line1" value={address.street} onChange={e => change("street", e.target.value)} maxLength={128} required disabled={!!busy} /></label>
            <label className="block space-y-1"><span>{t("公寓、房间号（可选）")}</span><Input autoComplete="address-line2" value={address.unit} onChange={e => change("unit", e.target.value)} maxLength={64} disabled={!!busy} /></label>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <label className="block space-y-1"><span>{t("城市")}</span><Input autoComplete="address-level2" value={address.city} onChange={e => change("city", e.target.value)} maxLength={64} required disabled={!!busy} /></label>
              <label className="block space-y-1"><span>{t("州")}</span><select autoComplete="address-level1" aria-label={t("州")} value={address.state} onChange={e => change("state", e.target.value)} required disabled={!!busy} className="h-[34px] w-full rounded-lg border border-gray-300 bg-white px-2 dark:border-white/15 dark:bg-gray-900"><option value="">{t("请选择")}</option>{US_STATES.map(state => <option key={state}>{state}</option>)}</select></label>
              <label className="block space-y-1"><span>{t("邮编")}</span><Input autoComplete="postal-code" value={address.zip} onChange={e => change("zip", e.target.value)} maxLength={10} pattern="[0-9]{5}(-[0-9]{4})?" required disabled={!!busy} /></label>
            </div>
            <Button type="submit" loading={busy === "verify"} disabled={!valid || !!busy}>{t("校验地址")}</Button>
          </form>
          {candidates.length ? <fieldset className="space-y-2 rounded-xl border border-gray-200 p-3 dark:border-white/15">
            <legend className="px-1 font-medium">{t("选择运营商规范化后的地址")}</legend>
            {candidates.map((candidate, index) => <label key={index} className="flex cursor-pointer items-start gap-2 rounded-lg p-2 hover:bg-black/5 dark:hover:bg-white/5">
              <input type="radio" name="e911-address" checked={selected === index} onChange={() => setSelected(index)} disabled={!!busy} className="mt-1" />
              <span>{formatAddress(candidate.address)}</span>
            </label>)}
          </fieldset> : null}
          <details className="rounded-xl border border-gray-200 p-3 dark:border-white/15"><summary className="cursor-pointer">{t("运营商Wi-Fi Calling条款")}</summary><p className="mt-3 max-h-48 overflow-y-auto whitespace-pre-line text-xs leading-5 text-gray-500">{session?.terms}</p></details>
          <label className="flex items-start gap-2"><input type="checkbox" checked={accepted} onChange={e => setAccepted(e.target.checked)} disabled={!!busy} className="mt-1" /><span>{t("我已阅读并同意运营商Wi-Fi Calling条款，确认地址准确。")}</span></label>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" onClick={() => void submit()} loading={busy === "submit"} disabled={!candidates.length || !accepted || !!busy}>{t("提交给运营商")}</Button>
            <Button onClick={() => void submit(true)} loading={busy === "refresh"} disabled={!!busy}>{t("刷新运营商状态")}</Button>
          </div>
        </> : null}
        {outcome ? <div role="status" className={outcome === "confirmed" ? "rounded-xl bg-emerald-50 p-3 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300" : "rounded-xl bg-amber-50 p-3 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300"}>
          {outcome === "confirmed" ? session?.status?.confirmation === "carrier_status" ? t("运营商已确认E911登记状态，Wi-Fi Calling已开通。") : t("运营商已确认E911地址，Wi-Fi Calling已开通。") : t("运营商尚未确认完成，请稍后刷新状态。")}
          {outcome === "confirmed" && session?.status?.address ? <div className="mt-1 font-mono text-xs">{formatAddress(session.status.address)}</div> : null}
          {session?.reconnectScheduled ? <div className="mt-1 text-xs">{t("已安排VoWiFi重连。")}</div> : null}
        </div> : null}
        {error ? <div role="alert" className="rounded-xl bg-red-50 p-3 text-red-600 dark:bg-red-950/40 dark:text-red-300">{error}</div> : null}
        {!busy && !session ? <Button onClick={() => setGeneration(value => value + 1)}>{t("重试")}</Button> : null}
        {!busy && error && session ? <Button onClick={() => setGeneration(value => value + 1)}>{t("重新开始登记")}</Button> : null}
      </div>
    </Modal>
  );
}

function formatAddress(address: Address) {
  return [address.street, address.unit, address.city, address.state, address.zip].filter(Boolean).join(", ");
}
