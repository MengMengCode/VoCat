import { cx } from "../../lib/utils";
import { CountryFlag } from "../CountryFlag";
import { EsimProfileRow } from "./EsimProfileRow";
import type { EsimChipInfo, EsimEid, EsimProfileGroup } from "./types";
import { useI18n } from "../../lib/i18n";

export interface SpaceNotice {
  aidHex: string;
  message: string;
}

export interface EsimEuiccGroupProps {
  deviceId: string;
  deviceOnline: boolean;
  group: EsimProfileGroup;
  index: number;
  chipInfo: EsimChipInfo | null;
  showSensitive: boolean;
  spaceNotice: SpaceNotice | null;
  renamingIccid: string | null;
  renameValue: string;
  switchingIccid: string | null;
  deletingIccid: string | null;
  policyIccid: string | null;
  onRenameValueChange: (v: string) => void;
  onSwitch: (iccid: string, state: number | undefined, aidHex?: string) => void;
  onStartRename: (iccid: string, name?: string) => void;
  onSubmitRename: (iccid: string, aidHex?: string) => void;
  onCancelRename: () => void;
  onTogglePolicy: (iccid: string) => void;
  onDelete: (iccid: string, name: string | undefined, aidHex?: string) => void;
  onPolicyChanged: () => void;
}

function normAid(aid?: string): string {
  return (aid || "").trim().toUpperCase();
}

function manufacturerCountryCode(manufacturer?: string): string {
  const value = (manufacturer || "").toLowerCase();
  if (value.includes("eastcompeace") || value.includes("watchdata") || value.includes("hutopt")) return "CN";
  if (value.includes("giesecke") || value.includes("g+d")) return "DE";
  if (value.includes("thales") || value.includes("idemia")) return "FR";
  if (value.includes("gemalto")) return "CH";
  return "";
}

function PkiInfo({ eid }: { eid: EsimEid }) {
  const { t } = useI18n();
  const has =
    eid.manufacturer ||
    (eid.certificates && eid.certificates.length) ||
    eid.defaultSmdpAddress ||
    eid.rootDsAddress ||
    eid.sasAccreditationNumber;
  if (!has) return null;
  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-gray-400 dark:text-gray-500">
      {eid.manufacturer ? (
        <span className="inline-flex items-center gap-1">
          <span className="text-[10px]">{t("生产商:")}</span>
          <span>{eid.manufacturer}</span>
          <CountryFlag countryCode={manufacturerCountryCode(eid.manufacturer)} />
        </span>
      ) : null}
      {eid.certificates && eid.certificates.length ? (
        <span className="inline-flex items-center gap-1">
          <span className="text-[10px]">{t("证书:")}</span> {eid.certificates.join(" · ")}
        </span>
      ) : null}
      {eid.defaultSmdpAddress ? (
        <span className="inline-flex items-center gap-1">
          <span className="text-[10px]">Default SM-DP+:</span> {eid.defaultSmdpAddress}
        </span>
      ) : null}
      {eid.rootDsAddress ? (
        <span className="inline-flex items-center gap-1">
          <span className="text-[10px]">Root SM-DS:</span> {eid.rootDsAddress}
        </span>
      ) : null}
      {eid.sasAccreditationNumber ? (
        <span className="inline-flex items-center gap-1">
          <span className="text-[10px]">SAS:</span> {eid.sasAccreditationNumber}
        </span>
      ) : null}
      {eid.aid ? (
        <span className="inline-flex min-w-0 items-center gap-1">
          <span className="text-[10px]">ISD-R AID:</span>
          <span className="break-all font-mono">{eid.aid}</span>
        </span>
      ) : null}
      {eid.trustedCiKeyIds && eid.trustedCiKeyIds.length ? (
        <span className="inline-flex min-w-0 items-center gap-1">
          <span className="text-[10px]">Trusted CI:</span>
          <span className="break-all font-mono">{eid.trustedCiKeyIds.join(" · ")}</span>
        </span>
      ) : null}
    </div>
  );
}

export function EsimEuiccGroup(props: EsimEuiccGroupProps) {
  const { t } = useI18n();
  const { group, index, chipInfo } = props;
  const eidEntry =
    chipInfo?.eids?.find((e) => e.eid === group.eid) ||
    (chipInfo?.eids?.length === 1 ? chipInfo.eids[0] : chipInfo?.eids?.[index]);
  return (
    <div className="ui-panel-muted overflow-hidden">
      <div className="border-b border-gray-100 px-4 py-3 dark:border-white/10">
        <div className="flex items-center justify-between">
          <div>
            <span className="text-sm font-bold text-gray-900 dark:text-white">eUICC #{index + 1}</span>
            <span className={cx("ml-2 font-mono text-xs text-gray-400 transition-all", !props.showSensitive && "select-none blur-sm")}>
              {group.eid}
            </span>
          </div>
          {eidEntry ? (
            <div className="text-xs text-gray-500">
              <span className="inline-flex flex-col items-end gap-1">
                <span className="inline-flex items-center gap-1">
                  <span className={cx("h-2 w-2 rounded-full", (eidEntry.freeNvramBytes ?? 0) > 1e5 ? "bg-green-500" : "bg-yellow-500")} />
                  {t("可用")} {eidEntry.freeNvram}
                </span>
                {props.spaceNotice && normAid(group.aidHex) === props.spaceNotice.aidHex ? (
                  <span className="text-[11px] text-emerald-600 dark:text-emerald-400">{props.spaceNotice.message}</span>
                ) : null}
              </span>
            </div>
          ) : null}
        </div>
        {eidEntry ? <PkiInfo eid={eidEntry} /> : null}
      </div>
      {(group.profiles || []).length === 0 ? (
        <div className="p-4 text-sm text-gray-400">{t("暂无 Profile")}</div>
      ) : (
        <div className="divide-y divide-gray-100 dark:divide-white/10">
          {(group.profiles || []).map((p) => (
            <EsimProfileRow
              key={p.iccid}
              deviceId={props.deviceId}
              deviceOnline={props.deviceOnline}
              aidHex={group.aidHex}
              profile={p}
              showSensitive={props.showSensitive}
              renaming={props.renamingIccid === p.iccid}
              renameValue={props.renameValue}
              switching={props.switchingIccid === p.iccid}
              deleting={props.deletingIccid === p.iccid}
              policyOpen={props.policyIccid === p.iccid}
              onRenameValueChange={props.onRenameValueChange}
              onSwitch={() => props.onSwitch(p.iccid, p.state, group.aidHex)}
              onStartRename={() => props.onStartRename(p.iccid, p.name)}
              onSubmitRename={() => props.onSubmitRename(p.iccid, group.aidHex)}
              onCancelRename={props.onCancelRename}
              onTogglePolicy={() => props.onTogglePolicy(p.iccid)}
              onDelete={() => props.onDelete(p.iccid, p.name, group.aidHex)}
              onPolicyChanged={props.onPolicyChanged}
            />
          ))}
        </div>
      )}
    </div>
  );
}
