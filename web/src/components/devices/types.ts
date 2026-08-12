import type { DeviceOverview, DeviceType, ModemSummary } from "../../types";

// PNN record read from the modem (opl/pnn drive the SIM operator display).
export interface ModemPnn {
  record?: number;
  fullName?: string;
  shortName?: string;
}

export interface ModemOpl {
  plmn?: string;
  pnnRecord?: number;
}

// Modem fields the overview tab reads beyond the shared ModemSummary.
export interface DeviceModem extends ModemSummary {
  nr5gSignalSinr?: number;
  pnn?: ModemPnn[];
  opl?: ModemOpl[];
}

// Device detail (`/devices/:id/overview` -> devices[0]) with the extra fields
// the reference page reads. All camelCase (api auto-converts).
export interface DeviceDetail extends Omit<DeviceOverview, "modem" | "traffic"> {
  developerEnabled?: boolean;
  modem: DeviceModem;
  localPhone?: string;
  publicIpv6?: string;
  activeEsimProfileName?: string;
  usbPath?: string;
  traffic?: Record<string, string>;
}

// Add-device form (subset of DeviceConfig posted as `config`).
export interface AddDeviceForm {
  id: string;
  name: string;
  deviceType: DeviceType | "";
  interface: string;
  modemImei: string;
  usbPath: string;
  esimTransport: string;
  atPort: string;
  controlDevice: string;
  deviceBackend: string;
	 simPin: string;
}

export interface LoadError {
  message: string;
  status?: number;
  method?: string;
  url?: string;
}

// Operator-selection scan candidate.
export interface OperatorCandidate {
  plmn?: string;
  operatorName?: string;
  shortName?: string;
  countryCode?: string;
  status?: string;
  rats?: Array<string | null>;
  includesPcsDigit?: boolean;
}

/* ---- eSIM ---- */
export interface EsimEid {
  eid: string;
  aid?: string;
  freeNvram?: string;
  freeNvramBytes?: number;
  manufacturer?: string;
  certificates?: string[];
  trustedCiKeyIds?: string[];
  defaultSmdpAddress?: string;
  rootDsAddress?: string;
  sasAccreditationNumber?: string;
}
export interface EsimChipInfo {
  serialNumber?: string;
  skuName?: string;
  firmware?: string;
  eids?: EsimEid[];
}
export interface EsimProfileItem {
  iccid: string;
  name?: string;
  serviceProviderName?: string;
  state?: number;
  stateText?: string;
  classText?: string;
}
export interface EsimProfileGroup {
  eid?: string;
  aidHex?: string;
  profiles: EsimProfileItem[];
}
export interface EsimNotification {
  sequenceNumber: number;
  event?: string;
  iccid?: string;
  address?: string;
  aidHex?: string;
  canRetry?: boolean;
}
export interface EsimDownloadForm {
  smdp: string;
  matchingId: string;
  confirmationCode: string;
  aidHex: string;
}
