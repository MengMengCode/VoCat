package modem

import "strings"

// Chipset identifies a verified modem platform behind a USB identity.
// A vendor ID alone does not identify a chipset or its AT dialect.
type Chipset string

const (
	// ChipsetGeneric covers USB modems without a known chipset family.
	ChipsetGeneric Chipset = "generic"
	// ChipsetZXIC covers verified ZXIC devices.
	ChipsetZXIC Chipset = "zxic"
)

// ZTE's vendor ID is shared by several platforms; only the lab UZ901's
// product ID has been verified to need the ZXIC command adaptations.
const (
	zxicVendorID       = "19d2"
	zxicUZ901ProductID = "0536"
)

// ChipsetForUSB reports the chipset family for a USB vendor/product pair.
// Unverified USB identities retain generic capabilities. Firmware-reported
// manufacturer and product strings do not influence chipset detection.
func ChipsetForUSB(vendorID, productID string) Chipset {
	if strings.EqualFold(strings.TrimSpace(vendorID), zxicVendorID) &&
		strings.EqualFold(strings.TrimSpace(productID), zxicUZ901ProductID) {
		return ChipsetZXIC
	}
	return ChipsetGeneric
}

// ChipsetForCandidate reports the chipset family of a discovered candidate.
func ChipsetForCandidate(candidate Candidate) Chipset {
	return ChipsetForUSB(candidate.VendorID, candidate.ProductID)
}

// IsZXICFamily reports whether a USB identity belongs to the ZXIC chipset
// family.
func IsZXICFamily(vendorID, productID string) bool {
	return ChipsetForUSB(vendorID, productID) == ChipsetZXIC
}
