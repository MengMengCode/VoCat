export interface AutomaticTaskProfileGroup {
  aidHex?: string;
  profiles?: Array<{
    iccid: string;
    name?: string;
    serviceProviderName?: string;
  }>;
}

export interface AutomaticTaskProfileOption {
  iccid: string;
  aidHex: string;
  label: string;
}

export interface AutomaticTaskProfileRequestGuard {
  begin: () => number;
  invalidate: () => void;
  isCurrent: (requestID: number) => boolean;
}

export function createAutomaticTaskProfileRequestGuard(): AutomaticTaskProfileRequestGuard {
  let latestRequestID = 0;
  return {
    begin: () => ++latestRequestID,
    invalidate: () => { latestRequestID += 1; },
    isCurrent: (requestID) => requestID === latestRequestID,
  };
}

export function buildAutomaticTaskProfileOptions(
  groups: AutomaticTaskProfileGroup[],
  currentICCID: string,
  currentSIMLabel: string,
): AutomaticTaskProfileOption[] {
  const options = groups.flatMap((group, groupIndex) =>
    (group.profiles || []).map((profile) => ({
      iccid: profile.iccid,
      aidHex: group.aidHex || "",
      label: `${profile.name || profile.serviceProviderName || `Profile ${groupIndex + 1}`} · ${profile.iccid}`,
    })),
  );
  const iccid = currentICCID.trim();
  if (iccid && !options.some((option) => option.iccid.trim() === iccid)) {
    options.push({ iccid, aidHex: "", label: `${currentSIMLabel} · ${iccid}` });
  }
  return options;
}

export function selectAutomaticTaskProfileOption(
  options: AutomaticTaskProfileOption[],
  requestedICCID: string,
): AutomaticTaskProfileOption | undefined {
  const iccid = requestedICCID.trim();
  if (iccid) return options.find((option) => option.iccid.trim() === iccid);
  return options[0];
}
