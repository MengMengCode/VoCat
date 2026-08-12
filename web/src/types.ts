export type ApiStatus = "ok" | "error";

export type DeviceType = "wifi_410" | "dji_4g" | "pcie_ec20_ec25";

export interface Session {
  authenticated: boolean;
  username: string;
  role: string;
  expiresAt?: string;
  csrfToken?: string;
}

export interface LoginResponse {
  status: ApiStatus;
  username?: string;
  role?: string;
  expiresAt?: string;
  csrfToken?: string;
}

export interface ApiErrorBody {
  status?: string;
  code?: string;
  error?: string;
  message?: string;
  requestId?: string;
  warning?: string;
  busy?: boolean;
}

export interface VoWiFiRuntime {
  deviceId: string;
  phase: string;
  dataplaneMode: string;
  iccid: string;
  imsi: string;
  simReady: boolean;
  accessReady: boolean;
  tunnelReady: boolean;
  imsReady: boolean;
  smsReady: boolean;
  carrierPlmn?: string;
  carrierPresetId?: string;
  carrierSource?: string;
  epdgSource?: string;
  imsIdentitySource?: string;
  regStatus: number;
  regStatusText: string;
  networkMode: string;
  localPhone?: string;
  phoneNumberSource?: string;
  lastErrorClass: string;
  lastError: string;
  lastReason: string;
  updatedAt: string;
  tunnel?: Record<string, unknown>;
  imscore?: Record<string, unknown>;
  smsip?: Record<string, unknown>;
}

export interface ModemSummary {
  operator: string;
  nativeMcc: string;
  nativeMnc: string;
  operatorCountryCode?: string;
  nativeSpn?: string;
  cardMcc?: string;
  cardMnc?: string;
  cardCountry?: string;
  serviceBlocked?: boolean;
  blockedReason?: string;
  networkMode: string;
  networkDuplex?: string;
  radioBand: string;
  radioChannel: number;
  signalDbm: number;
  signalRsrp?: number;
  signalRsrq?: number;
  signalSinr: number;
  imei: string;
  iccid: string;
  imsi?: string;
  firmware?: string;
  model?: string;
  regStatus: number;
  regStatusText?: string;
  registrationLimited?: boolean;
  psAttached?: boolean;
  simInserted?: boolean;
  operatingMode?: number;
  phoneNumber?: string;
  phoneNumberSource?: string;
}

export interface DeviceListItem {
  id: string;
  name: string;
  deviceType: DeviceType;
  running: boolean;
  healthy: boolean;
  controlOnline: boolean;
  physicalPresent?: boolean;
  workerRunning?: boolean;
  dataConnected?: boolean;
  radioRegistered?: boolean;
  lifecyclePhase?: string;
  lifecycleReason?: string;
  publicIp: string;
  privateIp?: string;
  interface: string;
  esimTransport: string;
  smsEnabled: boolean;
	  networkEnabled: boolean;
  vowifiEnabled: boolean;
  vowifiActive?: boolean;
  vowifiRuntime: VoWiFiRuntime;
  modem: ModemSummary;
  networkConnected: boolean;
  registrationStateLabel: "registered" | "searching" | "denied" | "limited" | "unknown";
  flightMode?: boolean;
}

export interface DevicesResponse {
  deviceLimit: number;
  devices: DeviceListItem[];
}

export interface DashboardDevice {
  id: string;
  name: string;
  deviceType: DeviceType;
  interface: string;
  proxyPort: number;
  publicIp: string;
  healthy: boolean;
  operator: string;
  signalDbm: number;
  networkMode: string;
  networkDuplex?: string;
  vowifiActive: boolean;
  vowifiRuntime: VoWiFiRuntime;
  networkConnected: boolean;
  model?: string;
}

export interface DeviceOverview extends DeviceListItem {
  atPort?: string;
  audioDevice?: string;
  backendMode?: string;
  controlDevice?: string;
  radioLiveOk?: boolean | null;
  traffic?: Record<string, string>;
  trafficRaw?: Record<string, number>;
  trafficMeta?: Record<string, unknown>;
}

export interface DeviceStatus {
  id: string;
  name: string;
  healthy: boolean;
  interface: string;
  publicIp: string;
  proxyPort: number;
  lastHardwareRefresh?: string;
  networkConnected: boolean;
  modem: ModemSummary;
  vowifi?: Record<string, unknown>;
  simServiceTable?: Record<string, unknown>;
  pnn?: Array<Record<string, unknown>>;
  opl?: Array<Record<string, unknown>>;
}

export interface DiscoveredDevice {
  discoveryKey: string;
  controlPath: string;
  netInterface: string;
  usbPath: string;
  vendorId: number;
  productId: number;
  driverName: string;
  atPorts: string[];
  atPort: string;
  audioDevice?: string;
  imei?: string;
  mode: string;
  networkCapable: boolean;
  configured: boolean;
  configuredId?: string;
  degraded?: boolean;
  usbnetMode?: number | null;
}

export interface DeviceConfig {
  id: string;
  name: string;
  deviceType: DeviceType;
  interface: string;
  controlDevice: string;
  atPort: string;
  usbPath: string;
  audioDevice?: string;
  modemImei?: string;
  apn: string;
  imsApn: string;
  imsPrivateIdentity: string;
  imsPublicIdentity: string;
  imsSmsCenter: string;
  imsTransport: "tcp" | "udp";
  imsAllowImsiDerivedIdentity: boolean;
  vowifiEapMethod: "aka" | "aka-prime";
  vowifiAllowSha1: boolean;
  vowifiUseModp1024: boolean;
  proxyPort: number;
  baudRate: number;
  dataBits: number;
  stopBits: number;
  parity: string;
  deviceBackend: "at" | "qmi";
  esimTransport: "at" | "qmi";
  qmiUseProxy: boolean;
  qmiProxyPath?: string;
  qmiProxyExecutable?: string;
  smsEnabled: boolean;
  vowifiEnabled: boolean;
}

export interface USBNetMode {
  mode: number;
  name: string;
  rebootRequired?: boolean;
}

export interface OperatorSelection {
  mode: number;
  format: number;
  operator: string;
  accessTechnology?: string;
}

export interface CardPolicy {
  iccid: string;
  vowifiEnabled: boolean;
  airplaneEnabled: boolean;
  apn?: string;
  ipVersion?: string;
  source?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface SMSContact {
  deviceId: string;
  deviceName?: string;
  modemImei?: string;
  imsi: string;
  localPhone?: string;
  peer: string;
  displayName?: string;
  lastMessage?: string;
  lastContent?: string;
  lastTimestamp: string;
  direction?: string;
  lastType?: string;
  lastSmsId?: number;
  unreadCount: number;
  messageCount?: number;
}

export interface SMSMessage {
  id: number;
  messageId?: string;
  deviceId: string;
  modemImei?: string;
  imsi: string;
  peer: string;
  direction: "inbound" | "outbound" | "received" | "sent";
  body?: string;
  content?: string;
  sender?: string;
  recipient?: string;
  localPhone?: string;
  deviceName?: string;
  type?: string;
  timestamp: string;
  status: string;
  source?: string;
  deliveryState?: string;
}

export interface UpstreamProxy {
  id: string;
  name: string;
  addr: string;
  username: string;
  password?: string;
  enabled: boolean;
}

export interface UpstreamProxyProbe {
  reachable?: boolean;
  handshakeOk?: boolean;
  udpAssociateOk?: boolean;
  authMethod?: string;
  relayAddr?: string;
  diagnosis?: string;
  hint?: string;
  error?: string;
}

export interface UpstreamProxySaveResponse {
  status?: string;
  proxy?: UpstreamProxy;
  probe?: UpstreamProxyProbe;
  message?: string;
}

export interface Country {
  countryCode: string;
  countryName: string;
  mccs: string[];
}

export interface CountryRule {
  countryCode: string;
  countryName?: string;
  upstreamProxyId: string;
  enabled: boolean;
}

export interface DeviceProxyBinding {
  deviceId: string;
  upstreamProxyId: string;
  reconnectRequested?: boolean;
  reconnectError?: string;
}

export interface LogEntry {
  time: string;
  level: "debug" | "info" | "warn" | "error" | string;
  message: string;
  caller?: string;
  fields?: string | Record<string, unknown>;
}

export interface EsimProfile {
  iccid: string;
  name: string;
  serviceProviderName: string;
  state: number;
  stateText: string;
  classText?: string;
}

export interface EsimEuiccProfiles {
  eid: string;
  aidHex: string;
  profiles: EsimProfile[];
}

export interface EsimOverview {
  available?: boolean;
  reason?: string;
  chipInfo?: {
    serialNumber?: string;
    skuName?: string;
    firmware?: string;
    eids?: Array<Record<string, unknown>>;
  };
  profiles: EsimEuiccProfiles[];
}

export interface NotificationSettings {
  telegram: Record<string, unknown>;
  webhook: Record<string, unknown>;
  bark: Record<string, unknown>;
  email: Record<string, unknown>;
  pushplus: Record<string, unknown>;
}

// 网络访问控制策略：默认仅放行内网网段，可切换到对公网开放。
export interface SecuritySettings {
  mode: "internal" | "public";
  allowedCidrs: string[];
  trustProxyHeaders: boolean;
  clientIp: string;
  clientAllowed: boolean;
}

// 运行日志保留策略：默认不限制，可按条数或天数限制。
export interface LoggingSettings {
  mode: "unlimited" | "count" | "days";
  count: number;
  days: number;
  storedLogs: number;
}

export interface SystemInfo {
  version: string;
  buildTime: string;
  config: string;
  os?: string;
  architecture?: string;
  uptime?: string;
  developer?: boolean;
}

export interface HTTPSSettings {
  enabled: boolean;
  httpUrl: string;
  httpsUrl: string;
  fingerprint?: string;
  notAfter?: string;
}

export interface DeveloperSettings {
  deviceLimit: number;
  defaultDeviceLimit: number;
  maxDeviceLimit: number;
}

export type Notice = {
  kind: "success" | "error" | "warning" | "info";
  title: string;
  detail?: string;
} | null;
