package vowifi

// The EC20/Quectel dialect of the shared AT-modem core: the AT+CCID family
// for the ICCID, CUAD-first application discovery, and a strict PDP
// inventory in the radio phases.
var ec20VendorProfile = ATVendorProfile{
	Name:          "EC20",
	ICCIDCommands: []string{"AT+CCID", "AT+QCCID", "AT+MCCID"},
	ICCIDPrefixes: []string{"+CCID:", "+QCCID:", "+MCCID:"},
	CUADDiscovery: true,
}

// Compatibility aliases: the EC20 adapter predated the shared core, so its
// exported error and type names keep their identity for existing callers.
var (
	ErrEC20SIMNotReady       = ErrATSIMNotReady
	ErrEC20MNCUnavailable    = ErrATMNCUnavailable
	ErrEC20ApplicationAbsent = ErrATApplicationAbsent
	ErrEC20IdentityChanged   = ErrATIdentityChanged
	ErrEC20AKACommand        = ErrATAKACommand
	ErrEC20AKAResponse       = ErrATAKAResponse
	ErrEC20AKAMACFailure     = ErrATAKAMACFailure
)

type (
	// EC20ATExecutor is the executor interface the EC20 adapter was built on.
	EC20ATExecutor = ATModemExecutor
	// EC20UICCLocker mirrors device.Manager's UICC transaction boundary.
	EC20UICCLocker = ATUICCLocker
	// EC20AdapterOptions configures the EC20 adapter.
	EC20AdapterOptions = ATModemAdapterOptions
)

// EC20Adapter implements SIMIdentityReader, AKAProvider, and RadioController
// for Quectel-class modems using standardized AT commands on the AT port
// selected by device.Manager. It never discovers or opens serial ports
// itself, so ttyUSB0 diagnostic cannot be selected accidentally.
type EC20Adapter struct {
	*atModemAdapter
}

var (
	_ SIMIdentityReader    = (*EC20Adapter)(nil)
	_ AKAProvider          = (*EC20Adapter)(nil)
	_ PreferredAKAProvider = (*EC20Adapter)(nil)
	_ RadioController      = (*EC20Adapter)(nil)
)

func NewEC20Adapter(
	executor EC20ATExecutor,
	options EC20AdapterOptions,
) (*EC20Adapter, error) {
	adapter, err := newATModemAdapter(executor, options, ec20VendorProfile)
	if err != nil {
		return nil, err
	}
	return &EC20Adapter{atModemAdapter: adapter}, nil
}
