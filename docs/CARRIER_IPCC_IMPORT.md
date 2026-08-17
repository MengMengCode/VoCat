# Apple carrier bundle import

VoCat can convert a user-supplied Apple `.ipcc` archive into a reviewable
carrier-profile document. Importing is offline: VoCat does not download,
redistribute, or retain the source archive.

## Preview first

```sh
vocat carrier import-ipcc Carrier_iPhone.ipcc
```

The JSON output includes the source SHA-256, selected bundle, generated
profile, and structured warnings. To print only the installable profile:

```sh
vocat carrier import-ipcc --document-only Carrier_iPhone.ipcc
```

When an archive contains multiple bundles, select one explicitly:

```sh
vocat carrier import-ipcc --bundle O2_Giffgaff_UK.bundle Carrier_iPhone.ipcc
```

## Install explicitly

```sh
vocat carrier import-ipcc --install Carrier_iPhone.ipcc
```

The default destination is `carrier-profiles.d` beside the configured VoCat
database. `--profile-dir` selects another destination and `--id` replaces the
generated profile ID. Existing files are never overwritten. Restart VoCat
after installation; startup validates every installed JSON document before
the modem or VoWiFi runtime starts.

Installed rules are evaluated by selector specificity. A PLMN-only imported
rule cannot hide a more specific built-in GID/SPN/ICCID rule. At equal
specificity, the explicitly installed rule wins.

## Imported fields

The importer accepts both XML and binary property lists and converts only
portable, reviewable values:

- `SupportedSIMs` and `SupportedPLMNs`;
- MCC/MNC, GID1/GID2, SPN, and ICCID selectors represented by Apple;
- an unambiguous IKE `RemoteAddress` ePDG hostname;
- supported IKE DH/EAP-AKA intent;
- IMS IPsec intent while retaining VoCat's safe algorithm default.

Multiple device override plists are treated as independent evidence. A key
network value is omitted when overrides disagree.

## Never imported automatically

VoCat reports but drops settings that are unsafe, device-specific, or outside
the carrier interoperability schema:

- `ValidateRemoteCertificate=false`;
- device-specific DPD timing overrides;
- Wi-Fi Calling entitlement bypasses;
- APN usernames, passwords, and provisioning credentials;
- emergency/E911 behavior;
- device-family media and codec overrides;
- unsupported EAP methods or IKE groups.

An IPCC profile can only identify a SIM when the card exposes selectors that
the bundle actually describes. A generic EE bundle, for example, cannot by
itself distinguish every EE-hosted MVNO.
