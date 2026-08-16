import type { UpstreamProxy } from "../../types";
import { tl } from "../../lib/i18n";

export interface LoadError {
  message: string;
  status?: number;
}

export interface UpstreamForm {
  id: string;
  name: string;
  addr: string;
  username: string;
  password: string;
  enabled: boolean;
}

// Result of the SOCKS5 handshake and UDP Associate connectivity probe.
export interface UpstreamProbeResult {
  reachable?: boolean;
  handshakeOk?: boolean;
  udpAssociateOk?: boolean;
  udpExchangeOk?: boolean;
  authMethod?: string;
  relayAddr?: string;
  dnsServer?: string;
  dnsName?: string;
  dnsRcode?: number;
  roundTripMs?: number;
  diagnosis?: string;
  hint?: string;
  error?: string;
}

export interface UpstreamRow extends UpstreamProxy {
  bindingCount: number;
  countryNames: string[];
}

export function ipv6Hint(): string {
  return tl("IPv6 地址请使用 [IPv6]:port，例如 [2001:db8::1]:1080");
}

export function ipv6AddrError(addr: string): string {
  const value = String(addr || "").trim();
  if (!value || value.startsWith("[")) return "";
  return (value.match(/:/g) || []).length > 1 ? ipv6Hint() : "";
}

export function emptyUpstreamForm(): UpstreamForm {
  return { id: "", name: "", addr: "", username: "", password: "", enabled: true };
}
