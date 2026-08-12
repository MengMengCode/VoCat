import { useEffect, useRef, useState } from "react";

export interface PolicyFlags {
  vowifiEnabled: boolean;
  airplaneEnabled: boolean;
}

export interface PolicyToggleImpl {
  applyVoWiFi: (value: boolean, next: PolicyFlags) => Promise<{ ok: boolean }>;
  applyAirplane: (value: boolean, next: PolicyFlags) => Promise<{ ok: boolean }>;
  onChanged?: () => void;
}

type Field = "vowifi" | "airplane";

// RF-safe merge: VoWiFi always implies airplane mode. Turning VoWiFi off keeps
// airplane mode on; only the separate airplane switch can explicitly restore RF.
function mergePolicy(current: PolicyFlags, field: Field, value: boolean): PolicyFlags {
  if (field === "vowifi") {
    return value
      ? { vowifiEnabled: true, airplaneEnabled: true }
      : { vowifiEnabled: false, airplaneEnabled: true };
  }
  return value ? { vowifiEnabled: false, airplaneEnabled: true } : { ...current, airplaneEnabled: false };
}

const EMPTY: PolicyFlags = { vowifiEnabled: false, airplaneEnabled: false };

// shared toggle logic for the card-policy switches, with optimistic update
// and revert-on-failure.
export function useCardPolicyToggles(source: PolicyFlags | null, impl: PolicyToggleImpl) {
  const [local, setLocal] = useState<PolicyFlags>(EMPTY);
  const [vowifiPending, setVowifiPending] = useState(false);
  const [vowifiFailed, setVowifiFailed] = useState(false);
  const [airplanePending, setAirplanePending] = useState(false);
  const [airplaneFailed, setAirplaneFailed] = useState(false);
  const localRef = useRef(local);
  localRef.current = local;

  useEffect(() => {
    if (!source) return;
    setLocal({ vowifiEnabled: source.vowifiEnabled, airplaneEnabled: source.airplaneEnabled });
    setVowifiFailed(false);
    setAirplaneFailed(false);
  }, [source]);

  async function toggle(
    field: Field,
    value: boolean,
    apply: (value: boolean, next: PolicyFlags) => Promise<{ ok: boolean }>,
    setPending: (v: boolean) => void,
    setFailed: (v: boolean) => void,
  ) {
    const key: keyof PolicyFlags = field === "vowifi" ? "vowifiEnabled" : "airplaneEnabled";
    const next = mergePolicy(localRef.current, field, value);
    setLocal((prev) => ({ ...prev, [key]: value }));
    setPending(true);
    setFailed(false);
    const res = await apply(value, next);
    setPending(false);
    if (!res.ok) {
      setLocal((prev) => ({ ...prev, [key]: !value }));
      setFailed(true);
      return;
    }
    setLocal(next);
    impl.onChanged?.();
  }

  return {
    local,
    vowifiPending,
    vowifiFailed,
    airplanePending,
    airplaneFailed,
    onVoWiFiToggle: (v: boolean) => toggle("vowifi", v, impl.applyVoWiFi, setVowifiPending, setVowifiFailed),
    onAirplaneToggle: (v: boolean) => toggle("airplane", v, impl.applyAirplane, setAirplanePending, setAirplaneFailed),
  };
}
