import { useState } from "react";
import { OverviewNetworkCard } from "./OverviewNetworkCard";
import { OverviewVowifiCard } from "./OverviewVowifiCard";
import { OverviewSimPanel } from "./OverviewSimPanel";
import { OverviewNetworkPanel } from "./OverviewNetworkPanel";
import { OverviewTrafficChart } from "./OverviewTrafficChart";
import { OperatorSelectionDialog } from "./OperatorSelectionDialog";
import type { DeviceDetail } from "./types";
import { useI18n } from "../../lib/i18n";

export interface DeviceOverviewTabProps {
  device: DeviceDetail;
  simOperatorDisplay: string;
  trafficSpeedRx: string;
  trafficSpeedTx: string;
  trafficMinuteRx: string;
  trafficMinuteTx: string;
  onRefresh: () => void;
}

export function DeviceOverviewTab(props: DeviceOverviewTabProps) {
  const { t } = useI18n();
  const [operatorOpen, setOperatorOpen] = useState(false);
  const { device } = props;
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div className="ui-panel-muted p-4">
          <div className="mb-3 text-xs font-bold uppercase tracking-wider text-gray-500">{t("运行状态")}</div>
          {device?.vowifiEnabled ? (
            <OverviewVowifiCard device={device} />
          ) : (
            <OverviewNetworkCard device={device} onOpenOperatorSelection={() => setOperatorOpen(true)} />
          )}
        </div>
        <OverviewSimPanel
          device={device}
          simOperatorDisplay={props.simOperatorDisplay}
        />
        <OverviewNetworkPanel
          device={device}
          trafficMinuteRx={props.trafficMinuteRx}
          trafficMinuteTx={props.trafficMinuteTx}
          trafficSpeedRx={props.trafficSpeedRx}
          trafficSpeedTx={props.trafficSpeedTx}
        />
      </div>
      {device.developerEnabled && device.networkEnabled && device.id ? <OverviewTrafficChart deviceId={device.id} /> : null}
      {device?.id ? (
        <OperatorSelectionDialog
          open={operatorOpen}
          deviceId={device.id}
          scanBlockedReason={
            device.flightMode || device.modem?.operatingMode === 0 || device.modem?.operatingMode === 4
              ? t("运营商扫描需要开启蜂窝射频；请先关闭飞行模式，再手动开始扫描。")
              : ""
          }
          onClose={() => setOperatorOpen(false)}
          onUpdated={props.onRefresh}
        />
      ) : null}
    </div>
  );
}
