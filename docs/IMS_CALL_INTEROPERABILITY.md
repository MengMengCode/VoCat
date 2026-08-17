# IMS call interoperability validation

This document records the sanitized evidence behind VoCat's MMTel call-dialog
changes. It contains no ICCID, IMSI, MSISDN, proxy credential, APDU, eSIM
activation code, public exit address or private host address.

## Hardware environment

The 2026-08-17 comparison used:

- a native Linux x86-64 host;
- a DJI/Baiwang QDC507 modem with its original `2ca3:4006` USB identity;
- a CTExcel/EE UK profile with home PLMN `23433`;
- modem flight mode for the entire run (`AT+CFUN?` reported `4`);
- a UK SOCKS5 exit with a verified UDP round trip;
- one explicitly authorized call to the carrier's free `888` service.

Emergency and chargeable numbers were excluded.

## Trace-derived requirements

The working carrier dialog differed from VoCat's original generic INVITE in
several observable ways:

- the target and preferred identity came from the number explicitly associated
  by IMS registration, never from IMSI digits;
- the request advertised the MMTel ICSI in `P-Preferred-Service` and
  `Accept-Contact`;
- `P-Access-Network-Info`, the registered Contact identity and
  carrier-specific `tel:`/`sip:` URI rules were retained in the dialog;
- reliable provisional responses were acknowledged with PRACK;
- Record-Route, Contact, session timers, re-INVITE and UPDATE were handled as
  dialog state rather than as one-shot INVITE metadata;
- rejected in-dialog INVITEs still received the required transaction ACK.

The implementation logs only a bounded, sanitized SIP diagnostic. It does not
persist raw IMS headers or subscriber identities.

## Results and scope

Unit coverage verifies outgoing MMTel headers, associated-number selection,
carrier URI construction, provisional/final dialog updates, PRACK, session
refresh, re-INVITE handling and ACK behavior for rejected INVITEs.

On the full integration candidate, the carrier returned SIP `200 OK` and
selected `AMR/8000`; a later browser-media run lasted about 79.1 seconds with
both uplink and downlink counters advancing. That end-to-end run also used the
separate optional AMR worker and browser media changes, which are intentionally
not part of this focused signaling PR.

This PR therefore claims IMS signaling interoperability improvements, not a
complete carrier-independent voice implementation. AMR media, browser audio,
DTMF, inbound-call hardware acceptance, AMR-WB and a five-minute call remain
separate follow-up work.
