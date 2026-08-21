import { api } from "../../api";
import type { CardPolicy } from "../../types";

async function ok(p: Promise<unknown>): Promise<{ ok: boolean }> {
  try {
    await p;
    return { ok: true };
  } catch {
    return { ok: false };
  }
}

export function enableVoWiFi(deviceId: string) {
  return ok(api(`/devices/${deviceId}/vowifi`, { method: "PATCH", body: { enabled: true } }));
}
export function disableVoWiFi(deviceId: string) {
  return ok(api(`/devices/${deviceId}/vowifi`, { method: "PATCH", body: { enabled: false } }));
}
export function setFlightMode(deviceId: string, enabled: boolean) {
  return ok(api(`/devices/${deviceId}/flight-mode`, { method: "PATCH", body: { enabled } }));
}
export function getCardPolicy(iccid: string) {
  return api<CardPolicy>(`/cards/${iccid}/policy`);
}
export interface CardPolicyUpdate {
  vowifiEnabled?: boolean;
  airplaneEnabled?: boolean;
  apn?: string;
  ipVersion?: "IP" | "IPV6" | "IPV4V6";
  customPhoneNumber?: string;
  cellularImsEnabled?: boolean;
}

export interface CellularIMSStatus {
  iccid: string;
  desiredEnabled: boolean;
  supported: boolean;
  configured: boolean;
  registered: boolean;
  csKnown: boolean;
  csRegistered: boolean;
  changed: boolean;
  rebooting: boolean;
}
export function getCellularIMS(deviceId: string) {
  return api<CellularIMSStatus>(`/devices/${deviceId}/cellular-ims`);
}
export function setCellularIMS(deviceId: string, enabled: boolean) {
  return api<CellularIMSStatus>(`/devices/${deviceId}/cellular-ims`, { method: "PATCH", body: { enabled } });
}
export function updateCardPolicy(iccid: string, body: CardPolicyUpdate) {
  return api<CardPolicy>(`/cards/${iccid}/policy`, { method: "PUT", body });
}
export function putCardPolicy(iccid: string, body: { vowifiEnabled: boolean; airplaneEnabled: boolean }) {
  return ok(updateCardPolicy(iccid, body));
}

export interface ModemAPNProfile {
  cid: number;
  apn: string;
  ipVersion: "IP" | "IPV6" | "IPV4V6";
}
export function getDeviceAPNs(deviceId: string) {
  return api<{ items: ModemAPNProfile[] }>(`/devices/${deviceId}/network/apns`);
}

export interface CardAPNProfile {
  id: number;
  iccid: string;
  apn: string;
  username: string;
  hasPassword: boolean;
  proxy: string;
  mcc: string;
  mnc: string;
  ipVersion: "IP" | "IPV6" | "IPV4V6";
  roamingIpVersion: "IP" | "IPV6" | "IPV4V6";
  authType: "NONE" | "PAP" | "CHAP" | "PAP_OR_CHAP";
  createdAt?: string;
  updatedAt?: string;
}
export function getCardAPNs(iccid: string) {
  return api<{ items: CardAPNProfile[] }>(`/cards/${iccid}/apns`);
}
export interface CardAPNCreate {
  apn: string;
  username: string;
  password: string;
  proxy: string;
  mcc: string;
  mnc: string;
  ipVersion: "IP" | "IPV6" | "IPV4V6";
  roamingIpVersion: "IP" | "IPV6" | "IPV4V6";
  authType: "NONE" | "PAP" | "CHAP" | "PAP_OR_CHAP";
}
export function createCardAPN(iccid: string, body: CardAPNCreate) {
  return api<CardAPNProfile>(`/cards/${iccid}/apns`, { method: "POST", body });
}
export function updateCardAPN(iccid: string, id: number, body: Omit<CardAPNCreate, "password"> & { password?: string; clearPassword?: boolean }) {
  return api<CardAPNProfile>(`/cards/${iccid}/apns/${id}`, { method: "PATCH", body });
}
export function deleteCardAPN(iccid: string, id: number) {
  return api<{ deleted: boolean; id: number }>(`/cards/${iccid}/apns/${id}`, { method: "DELETE" });
}
