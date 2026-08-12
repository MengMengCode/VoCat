import { useEffect, type ReactNode } from "react";
import { SaveRegular } from "@fluentui/react-icons";
import { cx } from "../../lib/utils";
import { Button, Input, Modal, Select, Spinner, Tag } from "../ui";
import { isQmiControl } from "./shared";
import { DiscoveredDeviceRow } from "./DiscoveredDeviceRow";
import type { DiscoveredDevice } from "../../types";
import type { AddDeviceForm } from "./types";
import { useI18n } from "../../lib/i18n";
import { DEVICE_TYPES, deviceTypeImage } from "../../lib/deviceTypes";

export interface DeviceAddDialogProps {
  open: boolean;
  discovering: boolean;
  unconfiguredDiscovered: DiscoveredDevice[];
  addSelected: DiscoveredDevice | null;
  addConfig: AddDeviceForm;
  addSaving: boolean;
  onClose: () => void;
  onSelectDevice: (d: DiscoveredDevice) => void;
  onConfigChange: (next: AddDeviceForm) => void;
  onSave: () => void;
}

function discoveryKey(d?: DiscoveredDevice | null): string {
  return d ? String(d.discoveryKey || `${d.usbPath || ""}|${d.atPort || ""}`) : "";
}
function isQmiMode(d?: DiscoveredDevice | null): boolean {
  return String(d?.mode || "").toLowerCase() === "qmi";
}
function modeLabel(d?: DiscoveredDevice | null): string {
  const m = String(d?.mode || "unknown").toLowerCase();
  return m === "pcsc" ? "PC/SC" : m === "qmi" ? "QMI" : m === "mbim" ? "MBIM" : m === "ecm" ? "ECM" : m === "rndis" ? "RNDIS" : m === "ncm" ? "NCM" : "UNKNOWN";
}

function Field({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="space-y-1">
      <label className="text-xs font-bold uppercase tracking-wider text-gray-500">{label}</label>
      {children}
    </div>
  );
}

export function DeviceAddDialog(props: DeviceAddDialogProps) {
  const { t } = useI18n();
  const { addSelected, addConfig } = props;
  const fixedQmi = isQmiControl(addSelected?.controlPath || addConfig?.controlDevice);
  const isMbim = String(addSelected?.mode || "").toLowerCase() === "mbim";
	const isReader = addSelected?.hardwareKind === "pcsc" || String(addSelected?.mode || "").toLowerCase() === "pcsc";

  useEffect(() => {
    if (fixedQmi && addConfig.deviceBackend !== "qmi") props.onConfigChange({ ...addConfig, deviceBackend: "qmi" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fixedQmi]);
  useEffect(() => {
    if (isMbim && addConfig.deviceBackend !== "mbim") props.onConfigChange({ ...addConfig, deviceBackend: "mbim" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isMbim]);

  const backendOptions = isReader ? [{ value: "pcsc", label: "PC/SC" }] : [
    ...(isMbim
      ? []
      : [
          { value: "at", label: "AT", disabled: fixedQmi },
          { value: "qmi", label: "QMI", disabled: !addConfig.controlDevice },
        ]),
    ...(isMbim ? [{ value: "mbim", label: "MBIM" }] : []),
  ];
  const set = (patch: Partial<AddDeviceForm>) => props.onConfigChange({ ...addConfig, ...patch });

  return (
    <Modal
      open={props.open}
      onClose={props.onClose}
      title={t("添加设备配置")}
      width="max-w-[min(720px,92vw)]"
      className="glass-modal"
      footer={
        <div className="flex justify-end gap-2">
          <Button onClick={props.onClose}>{t("取消")}</Button>
          <Button variant="primary" loading={props.addSaving} onClick={props.onSave} className="!border-0" icon={<SaveRegular />}>
            {t("保存")}
          </Button>
        </div>
      }
    >
      <div className="mb-3 text-sm text-gray-500">{t("选择一个“未配置”的设备，系统将自动填充 AT 端口与识别信息。")}</div>
      <div className="max-h-[260px] space-y-2 overflow-auto pr-1">
        {props.discovering ? (
          <div className="flex flex-col items-center justify-center py-10 text-gray-400">
            <Spinner className="mb-3 h-8 w-8" />
            <div className="text-xs">{t("正在探测设备...")}</div>
          </div>
        ) : (
          <>
            {props.unconfiguredDiscovered.map((d) => (
              <DiscoveredDeviceRow
                key={discoveryKey(d)}
                device={d}
                selected={discoveryKey(addSelected) === discoveryKey(d)}
                modeLabel={modeLabel(d)}
                isQmi={isQmiMode(d)}
                onSelect={props.onSelectDevice}
              />
            ))}
            {props.unconfiguredDiscovered.length === 0 ? (
              <div className="p-3 text-sm text-gray-500">{t("暂无可添加设备（或系统未发现新的模组）")}</div>
            ) : null}
          </>
        )}
      </div>
      {addSelected ? (
        <div className="mt-4 space-y-2 rounded-xl border border-gray-200 bg-gray-50 p-4">
          <div className="text-xs font-bold uppercase tracking-wider text-gray-500">{t("选定设备状态")}</div>
          <div className="flex items-center gap-4 text-sm">
            <div className="flex items-center gap-2">
              <span className="text-gray-600">{t("模式:")}</span>
              <Tag type={isQmiMode(addSelected) ? "success" : "warning"}>{modeLabel(addSelected)}</Tag>
              {fixedQmi ? <Tag type="success">{t("仅 QMI 后端")}</Tag> : null}
              {isMbim ? <Tag type="success">{t("仅 MBIM 后端")}</Tag> : null}
            </div>
          </div>
          {fixedQmi ? <div className="text-xs text-emerald-700">{t("此类 WWAN QMI 设备运行后端固定为 QMI；AT 口仍会保留给 AT 终端。")}</div> : null}
        </div>
      ) : null}
      <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="sm:col-span-2">
          <Field label={t("设备类型")}>
            <div className="flex items-center gap-3">
              <div className="flex h-14 w-14 shrink-0 items-center justify-center rounded-xl border border-gray-200 bg-white p-1.5">
                {addConfig.deviceType ? <img src={deviceTypeImage(addConfig.deviceType)} alt="" className="h-full w-full object-contain" /> : null}
              </div>
              <Select
                value={addConfig.deviceType}
                onChange={(v) => set({ deviceType: v as AddDeviceForm["deviceType"] })}
                placeholder={t("请选择设备类型")}
                size="large"
                options={DEVICE_TYPES.map((item) => ({ value: item.value, label: t(item.label) }))}
              />
            </div>
          </Field>
        </div>
        <Field label="ID">
          <Input value={addConfig.id} onChange={(e) => set({ id: e.target.value })} placeholder={t("例如 ec20_3")} />
        </Field>
        <Field label={t("名称")}>
          <Input value={addConfig.name} onChange={(e) => set({ name: e.target.value })} placeholder={t("显示名称（可选）")} />
        </Field>
        <Field label={t("IMEI 绑定")}>
          <Input value={addConfig.modemImei} disabled placeholder={t("自动识别（从发现设备填充）")} />
        </Field>
        <Field label={t("USB 路径")}>
          <Input value={addConfig.usbPath} disabled />
        </Field>
        <Field label={t("网卡接口")}>
          <Input value={addConfig.interface} disabled />
        </Field>
        <Field label={t("AT 端口")}>
          <Input value={addConfig.atPort} disabled />
        </Field>
        <Field label={t("控制设备")}>
          <Input value={addConfig.controlDevice} disabled />
        </Field>
		{isReader ? <Field label="SIM PIN">
		  <Input type="password" value={addConfig.simPin} onChange={(e) => set({ simPin: e.target.value })} maxLength={8} inputMode="numeric" placeholder={t("仅在 SIM 启用 PIN 时填写")} />
		</Field> : null}
        <div className="flex items-center justify-between rounded-xl border border-gray-200 bg-gray-50 p-3">
          <div>
            <div className="text-sm font-bold text-gray-800">{t("设备后端模式")}</div>
            <div className="text-xs text-gray-500">
              {fixedQmi ? t("固定 QMI，AT 口仅用于终端") : isMbim ? t("固定 MBIM，AT 口仅用于终端") : t("AT=串口 / QMI=纯 QMI")}
            </div>
          </div>
          <Select
            value={addConfig.deviceBackend}
            onChange={(v) => set({ deviceBackend: v })}
            className="w-[110px]"
            placeholder="AT"
			disabled={fixedQmi || isMbim || isReader}
            options={backendOptions}
          />
        </div>
      </div>
    </Modal>
  );
}
