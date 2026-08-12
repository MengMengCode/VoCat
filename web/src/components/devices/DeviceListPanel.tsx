import type { DeviceListItem } from "../../types";
import { Input, Select, Tag, ListSkeleton, EmptyState } from "../ui";
import { isDeviceOnline, isRegistered, isVoWiFiInUse, lifecycleLabel } from "./shared";
import { DeviceListItemCard } from "./DeviceListItemCard";
import { tl, useI18n } from "../../lib/i18n";

export type StatusFilter = "all" | "online" | "offline";
export type SortKey = "name" | "signal";
export type SortDir = "asc" | "desc";

export interface DeviceListPanelProps {
  loading: boolean;
  query: string;
  statusFilter: StatusFilter;
  sortKey: SortKey;
  sortDir: SortDir;
  selectedId: string;
  filteredDevices: DeviceListItem[];
  deviceCount: number;
  deviceLimit: number;
  onQueryChange: (v: string) => void;
  onStatusFilterChange: (v: StatusFilter) => void;
  onSortKeyChange: (v: SortKey) => void;
  onSortDirChange: (v: SortDir) => void;
  onSelectDevice: (id: string) => void;
}

function primaryLine(d: DeviceListItem): string {
  const phaseLabel = lifecycleLabel(d.lifecyclePhase);
  if (phaseLabel && d.lifecyclePhase !== "online" && d.lifecyclePhase !== "offline") return phaseLabel;
  if (isRegistered(d)) {
    const operator = d.modem?.operator || "--";
    const mode = [d.modem?.networkDuplex, d.modem?.networkMode].filter(Boolean).join(" ") || "--";
    return `${operator} · ${mode}`;
  }
  if (isDeviceOnline(d)) {
    return d.registrationStateLabel === "searching" ? tl("搜索网络中") : d.registrationStateLabel === "denied" ? tl("驻网被拒") : tl("未驻网");
  }
  return tl("控制面恢复中");
}

function statusLine(d: DeviceListItem): string {
  if (isVoWiFiInUse(d)) return "WiFi-Calling";
  return primaryLine(d);
}

export function DeviceListPanel(props: DeviceListPanelProps) {
  const { t } = useI18n();
  const { loading, query, statusFilter, sortKey, sortDir, selectedId, filteredDevices, deviceCount, deviceLimit } = props;
  return (
    <div className="ui-card p-5">
      <div className="mb-4 flex items-center gap-3">
        <Input value={query} onChange={(e) => props.onQueryChange(e.target.value)} placeholder={t("搜索设备 / ICCID / IMEI / 网卡")} />
      </div>
      <div className="mb-4 grid grid-cols-2 gap-2">
        <Select
          value={statusFilter}
          onChange={(v) => props.onStatusFilterChange(v as StatusFilter)}
          placeholder={t("在线")}
          options={[
            { value: "all", label: t("全部状态") },
            { value: "online", label: t("仅在线") },
            { value: "offline", label: t("仅离线") },
          ]}
        />
        <Select
          value={sortKey}
          onChange={(v) => props.onSortKeyChange(v as SortKey)}
          placeholder={t("排序")}
          options={[
            { value: "name", label: t("排序：名称") },
            { value: "signal", label: t("排序：信号") },
          ]}
        />
        <Select
          value={sortDir}
          onChange={(v) => props.onSortDirChange(v as SortDir)}
          placeholder={t("方向")}
          options={[
            { value: "asc", label: t("升序") },
            { value: "desc", label: t("降序") },
          ]}
        />
        {deviceLimit > 0 ? (
          <div className="flex items-center">
            <Tag type={deviceCount >= deviceLimit ? "warning" : "info"} className="w-full justify-center">
              {t("配额")} {deviceCount} / {deviceLimit}
            </Tag>
          </div>
        ) : null}
      </div>
      {loading && filteredDevices.length === 0 ? (
        <ListSkeleton rows={8} />
      ) : filteredDevices.length === 0 ? (
        <EmptyState title={t("暂无设备")} subtitle={t("点击右上角“添加设备”开始接管")} />
      ) : (
        <div className="device-list-scroll max-h-[65vh] overflow-y-auto pr-1">
          <div className="device-list-grid">
            {filteredDevices.map((d) => (
              <DeviceListItemCard
                key={d.id}
                device={d}
                selected={selectedId === d.id}
                statusText={statusLine(d)}
                onSelect={props.onSelectDevice}
              />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
