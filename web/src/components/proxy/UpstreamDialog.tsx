import { Button, Input, Modal } from "../ui";
import { Field, SectionHeader, ToggleRow } from "./formUi";
import { ipv6Hint, type UpstreamForm, type UpstreamProbeResult } from "./shared";
import { tl, useI18n } from "../../lib/i18n";

export interface UpstreamDialogProps {
  open: boolean;
  editing: boolean;
  form: UpstreamForm;
  testing: boolean;
  probe: UpstreamProbeResult | null;
  onPatch: (patch: Partial<UpstreamForm>) => void;
  onTest: () => void;
  onClose: () => void;
  onSubmit: () => void;
}

type ProbeState = "ok" | "fail" | "pending";

function ProbeRow({ state, label, detail }: { state: ProbeState; label: string; detail?: string }) {
  const dot = state === "ok" ? "bg-green-500" : state === "fail" ? "bg-red-500" : "bg-gray-300 dark:bg-gray-600";
  const text =
    state === "ok" ? "text-green-600 dark:text-green-400" : state === "fail" ? "text-red-600 dark:text-red-400" : "text-gray-400";
  return (
    <div className="flex items-center gap-2 text-xs">
      <span className={`h-2 w-2 shrink-0 rounded-full ${dot}`} />
      <span className={`shrink-0 font-medium ${text}`}>{label}</span>
      {detail ? <span className="truncate text-gray-400">{detail}</span> : null}
    </div>
  );
}

function authMethodLabel(method?: string): string {
  if (method === "none") return tl("免鉴权");
  if (method === "username_password") return tl("用户名密码");
  return method || "";
}

function ProbeResultPanel({ probe }: { probe: UpstreamProbeResult }) {
  const { t } = useI18n();
  const reachable = !!probe.reachable;
  const handshakeOk = !!probe.handshakeOk;
  const udpOk = !!probe.udpAssociateOk;
  const handshakeState: ProbeState = !reachable ? "pending" : handshakeOk ? "ok" : "fail";
  const udpState: ProbeState = !handshakeOk ? "pending" : udpOk ? "ok" : "fail";
  return (
    <div className="ui-panel-muted space-y-2 rounded-lg p-3">
      <ProbeRow state={reachable ? "ok" : "fail"} label={t("TCP 连接")} detail={reachable ? t("可连通") : t("无法连接")} />
      <ProbeRow state={handshakeState} label={t("SOCKS5 握手")} detail={handshakeOk ? authMethodLabel(probe.authMethod) : undefined} />
      <ProbeRow
        state={udpState}
        label={t("UDP Associate（VoWiFi 依赖）")}
        detail={udpState === "pending" ? undefined : udpOk ? t("支持") : t("不支持")}
      />
      {probe.relayAddr ? (
        <div className="text-[11px] text-gray-400">
          {t("UDP 中继地址：")}<span className="font-mono">{probe.relayAddr}</span>
        </div>
      ) : null}
      {probe.hint ? <div className="text-[11px] text-gray-500 dark:text-gray-400">{probe.hint}</div> : null}
      {probe.error ? <div className="break-all text-[11px] text-red-500">{probe.error}</div> : null}
    </div>
  );
}

export function UpstreamDialog({ open, editing, form, testing, probe, onPatch, onTest, onClose, onSubmit }: UpstreamDialogProps) {
  const { t } = useI18n();
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={editing ? t("编辑前置代理") : t("新增前置代理")}
      width="max-w-lg"
      footer={
        <>
          <Button className="mr-auto" onClick={onTest} loading={testing} disabled={testing || !form.addr.trim()}>
            {t("检测连通性")}
          </Button>
          <Button onClick={onClose}>{t("取消")}</Button>
          <Button variant="primary" onClick={onSubmit} loading={testing} disabled={testing}>
            {editing ? t("更新") : t("创建")}
          </Button>
        </>
      }
    >
      <div className="space-y-6 pb-6">
        <div className="space-y-4">
          <SectionHeader tone="indigo" title={t("代理信息")} />
          <div className="grid grid-cols-2 gap-4">
            <Field label={t("代理 ID")}>
              <Input value={form.id} disabled={editing} placeholder={t("唯一标识，如 jp-proxy-01")} onChange={(e) => onPatch({ id: e.target.value })} />
            </Field>
            <Field label={t("名称")}>
              <Input value={form.name} placeholder={t("例如：日本代理")} onChange={(e) => onPatch({ name: e.target.value })} />
            </Field>
          </div>
          <Field label={t("Socks5 地址")}>
            <Input
              value={form.addr}
              placeholder={t("host:port，例如 1.2.3.4:1080 或 [2001:db8::1]:1080")}
              onChange={(e) => onPatch({ addr: e.target.value })}
            />
            <div className="mt-1 text-xs text-gray-400">
              {t("VoWiFi 通过此 Socks5 代理连接运营商，实现跨区域本地 VoWiFi。")}
              {ipv6Hint()}
              {t("。点下方「检测连通性」可在保存前验证 Socks5 握手与 UDP Associate。")}
            </div>
          </Field>
          <ToggleRow
            title={t("启用代理")}
            subtitle={t("禁用后，已绑定 Profile 的 VoWiFi 将停止使用该线路，不会泄漏到直连")}
            checked={form.enabled}
            onChange={(v) => onPatch({ enabled: v })}
          />
        </div>
        <div className="space-y-4">
          <SectionHeader tone="amber" title={t("鉴权设置（可选）")} />
          <div className="grid grid-cols-2 gap-4">
            <Field label={t("用户名")}>
              <Input value={form.username} placeholder={t("留空则免鉴权")} onChange={(e) => onPatch({ username: e.target.value })} />
            </Field>
            <Field label={t("密码")}>
              <Input type="password" value={form.password} placeholder={t("留空则免鉴权")} onChange={(e) => onPatch({ password: e.target.value })} />
              <div className="mt-1 text-xs text-gray-400">{t("编辑已有代理时留空会保持原密码不变。")}</div>
            </Field>
          </div>
        </div>
        {probe ? (
          <div className="space-y-3">
            <SectionHeader tone={probe.udpAssociateOk ? "green" : "amber"} title={t("连通性检测结果")} />
            <ProbeResultPanel probe={probe} />
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
