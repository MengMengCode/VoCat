import type { DeviceType } from "../types";

export const DEFAULT_DEVICE_TYPE: DeviceType = "pcie_ec20_ec25";

export const DEVICE_TYPES: ReadonlyArray<{ value: DeviceType; label: string; image: string }> = [
  { value: "wifi_410", label: "410 WiFi 棒（高通芯片）", image: "/410.png" },
  { value: "dji_4g", label: "大疆 4G 模块（移远芯片）", image: "/dj.png" },
  { value: "pcie_ec20_ec25", label: "PCIe EC20/EC25（移远芯片）", image: "/ec20.png" },
	{ value: "usb_sim_reader", label: "USB SIM 读卡器（仅 WiFi Calling）", image: "/sim-reader.svg" },
];

export function normalizeDeviceType(value?: string | null): DeviceType {
  return DEVICE_TYPES.some((item) => item.value === value) ? (value as DeviceType) : DEFAULT_DEVICE_TYPE;
}

export function deviceTypeImage(value?: string | null): string {
  const normalized = normalizeDeviceType(value);
  return DEVICE_TYPES.find((item) => item.value === normalized)?.image || "/ec20.png";
}
