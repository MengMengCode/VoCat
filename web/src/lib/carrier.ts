// MCC/MNC → carrier lookup. The offline table combines Android's maintained
// carrier ID database with the legacy global table as a fallback. Refresh it
// with scripts/update-carriers.py; runtime registration never depends on an
// external lookup service.
import table from "./mccmnc.json";

interface MccMncTable {
  c: Record<string, string[]>;
  i: Record<string, string>;
  t: string[];
}

const data = table as unknown as MccMncTable;
const THREE_DIGIT = new Set(data.t);

export interface CarrierInfo {
  mcc: string;
  mnc: string;
  name: string;
  iso: string;
}

function imsiDigits(imsi?: string): string {
  return String(imsi ?? "").replace(/\D/g, "");
}

// lookupCarrier resolves a SIM's home ("original") carrier from its IMSI. MNC length
// varies by MCC (2 vs 3 digits); North-American/Mexican MCCs are 3-digit-first, the
// rest 2-digit-first, with a fallback to the other length so real carriers resolve.
export function lookupCarrier(imsi?: string): CarrierInfo | null {
  const digits = imsiDigits(imsi);
  if (digits.length < 5) return null;
  const mcc = digits.slice(0, 3);
  const order = THREE_DIGIT.has(mcc) ? [3, 2] : [2, 3];
  for (const len of order) {
    const mnc = digits.slice(3, 3 + len);
    const hit = data.c[mcc + mnc];
    if (hit) return { mcc, mnc, name: hit[0], iso: hit[1] };
  }
  return null;
}

// carrierIso returns the alpha-2 country code for the SIM's home carrier, falling
// back to the MCC's country when the exact MNC is not catalogued.
export function carrierIso(imsi?: string): string {
  const hit = lookupCarrier(imsi);
  if (hit) return hit.iso;
  return data.i[imsiDigits(imsi).slice(0, 3)] ?? "";
}

// carrierBrandIso keeps the normal IMSI country flag for branded/MVNO SIMs.
// Lebara UK's Vodafone-NL-hosted 204-04 eSIM is the one known exception: its
// customer-facing country is GB even though AKA must continue using 204-04.
export function carrierBrandIso(spn?: string, imsi?: string): string {
  const brand = String(spn ?? "").trim().toLowerCase();
  const digits = imsiDigits(imsi);
  if (brand.includes("lebara") && digits.startsWith("20404")) return "gb";
  return carrierIso(imsi);
}
