package vowifi

// The ZXIC dialect of the shared AT-modem core, verified on the UZ901 lab
// unit (USB 19d2:0536). The platform firmware implements neither ATI, the
// AT+CCID family, nor AT+CLCC (each answers vendor +CME ERROR: 6003),
// exposes the ICCID only through the +ZICCID query, and refuses the
// AT+CGACT? inventory while the radio is off (vendor +CME ERROR: 148). The
// profile pins exactly those hardware-verified differences: an unadapted
// command must never be issued to an adapted chipset. Only the USB identity
// verified in internal/modem/chipset.go reaches this profile, so an
// unverified product keeps the standard command set.
// See docs/zxic-lab-checklist.md.
var zxicVendorProfile = ATVendorProfile{
	Name:          "ZXIC",
	ICCIDCommands: []string{"AT+ZICCID?"},
	ICCIDPrefixes: []string{"+ZICCID:"},
	// The platform manual does not document AT+CUAD; application discovery
	// goes straight to the standards-based EF_DIR read over the basic
	// channel, which the live firmware answers.
	CUADDiscovery: false,
	// With the radio off the firmware refuses the PDP inventory, so the
	// radio phases degrade to "no active contexts" instead of failing
	// VoWiFi setup.
	ToleratePDPInventoryRefusal: true,
}

// ZXICAdapter implements SIMIdentityReader, AKAProvider, and RadioController
// for the verified ZXIC platform on the shared AT-modem core. The vendor
// profile selects the ZXIC command dialect; every other flow (UICC locking,
// AKA, radio checkpointing) is the shared implementation.
type ZXICAdapter struct {
	*atModemAdapter
}

var (
	_ SIMIdentityReader    = (*ZXICAdapter)(nil)
	_ AKAProvider          = (*ZXICAdapter)(nil)
	_ PreferredAKAProvider = (*ZXICAdapter)(nil)
	_ RadioController      = (*ZXICAdapter)(nil)
)

// NewZXICAdapter builds the ZXIC adapter over the same executor surface as
// the EC20 adapter. Production code passes the device manager directly;
// tests can use an evidence transcript.
func NewZXICAdapter(
	executor ATModemExecutor,
	options ATModemAdapterOptions,
) (*ZXICAdapter, error) {
	adapter, err := newATModemAdapter(executor, options, zxicVendorProfile)
	if err != nil {
		return nil, err
	}
	return &ZXICAdapter{atModemAdapter: adapter}, nil
}
