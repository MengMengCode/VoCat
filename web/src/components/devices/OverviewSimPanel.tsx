import { EyeRegular, EyeOffRegular } from "@fluentui/react-icons";
import { Button } from "../ui";
import { FieldRow } from "./FieldRow";
import { useShowSensitive } from "./shared";
import type { DeviceDetail } from "./types";
import { useI18n } from "../../lib/i18n";
import { carrierIso } from "../../lib/carrier";
import { CountryFlag } from "../CountryFlag";

export interface OverviewSimPanelProps {
  device: DeviceDetail;
  simOperatorDisplay: string;
  customPhoneNumber?: string;
  e911Starting: boolean;
  onSetupE911: () => void;
}

export function OverviewSimPanel({ device, simOperatorDisplay, customPhoneNumber, e911Starting, onSetupE911 }: OverviewSimPanelProps) {
  const { t } = useI18n();
  const [showSensitive, toggleSensitive] = useShowSensitive();
  const modem = device.modem;
  const sensitive = !showSensitive;
  const activeEsim = (device.activeEsimProfileName || "").trim();
  const flightOn = device.vowifiActive || modem?.operatingMode === 0 || modem?.operatingMode === 4;
  const carrierCountryCode = String(modem?.homeCarrierCountryCode ?? "").trim() || carrierIso(modem?.imsi);
  const displayedPhoneNumber = customPhoneNumber?.trim() || device.localPhone || "--";
  const backendLabel =
    device.backendMode === "qmi" ? "QMI" : device.backendMode === "mbim" ? "MBIM" : device.backendMode === "at" ? "AT" : "Auto";

  return (
    <div className="ui-panel-muted relative min-w-0 overflow-hidden p-4">
      <div className="mb-2 flex items-center justify-between">
        <div className="text-xs font-bold uppercase tracking-wider text-gray-500">{t("SIM / 设备")}</div>
        <div
          className="-mr-1 -mt-1 cursor-pointer text-gray-400 hover:text-gray-600 dark:hover:text-gray-200"
          onClick={toggleSensitive}
        >
          {showSensitive ? <EyeRegular className="text-[18px]" /> : <EyeOffRegular className="text-[18px]" />}
        </div>
      </div>
      <div className="space-y-1.5 text-sm text-gray-700 dark:text-gray-200">
        <FieldRow label="IMEI" value={modem?.imei} sensitive={sensitive} monospace copyable />
        <FieldRow label="ICCID" value={modem?.iccid} sensitive={sensitive} monospace copyable />
        <FieldRow label="IMSI" value={modem?.imsi} sensitive={sensitive} monospace copyable />
        <FieldRow label={t("本机号码")} value={displayedPhoneNumber} sensitive={sensitive} monospace copyable />
        {device?.e911SetupAvailable ? (
          <div className="flex justify-between gap-3">
            <span className="text-gray-500">{t("E911地址")}</span>
            <Button variant="primary" size="small" plain loading={e911Starting} className="!border-0" onClick={onSetupE911}>
              {t("设置")}
            </Button>
          </div>
        ) : null}
        {activeEsim ? <FieldRow label={t("当前eSIM")} value={activeEsim} monospace copyable /> : null}
        <FieldRow
          label={t("原运营商")}
          value={simOperatorDisplay}
          prefix={simOperatorDisplay !== "--" ? <CountryFlag countryCode={carrierCountryCode} /> : null}
          copyable
        />
        <FieldRow label={t("固件版本")} value={modem?.firmware} monospace copyable />
        <div className="flex justify-between gap-3">
          <span className="text-gray-500">{t("飞行模式")}</span>
          <span>{flightOn ? t("是") : t("否")}</span>
        </div>
        <FieldRow label={t("运行模式")} value={backendLabel} monospace />
      </div>
    </div>
  );
}
