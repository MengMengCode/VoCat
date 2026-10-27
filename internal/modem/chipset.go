package modem

import "strings"

// Chipset identifies the modem chipset family behind a USB identity. A
// family groups products that share one platform AT dialect, so behavior
// adapts per chipset family instead of per retail module: every ZXIC
// platform product answers the same command set (no ATI, no +CCID family,
// no +CLCC) while each exposes its own USB product ID.
type Chipset string

const (
	// ChipsetGeneric covers USB modems without a known chipset family.
	ChipsetGeneric Chipset = "generic"
	// ChipsetZXIC covers the ZXIC 4G chipset family (USB vendor 19d2).
	ChipsetZXIC Chipset = "zxic"
)

// zxicVendorID is the USB vendor ID shared by the ZXIC chipset family.
const zxicVendorID = "19d2"

// ChipsetForUSB reports the chipset family for a USB vendor/product pair.
// Family matching keys on the vendor ID only; the manufacturer and product
// strings reported by firmware are frequently placeholders and must not
// influence chipset detection.
func ChipsetForUSB(vendorID, productID string) Chipset {
	if strings.EqualFold(strings.TrimSpace(vendorID), zxicVendorID) {
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
