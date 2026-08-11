package server

import (
	"net/http"

	"vocat/internal/store"
)

// websheetManager remains as a server-owned capability placeholder. VoCat has
// no carrier provisioning/websheet integration, so it intentionally creates no
// local E911 sessions and stores no address that could be mistaken for a
// carrier-provisioned emergency location.
type websheetManager struct{}

func newWebsheetManager() *websheetManager {
	return &websheetManager{}
}

// handleE911Websheet fails closed until a carrier-backed provisioning service
// and emergency IMS registration path are available. A self-hosted address form
// cannot provision an emergency address with the carrier.
func (s *Server) handleE911Websheet(
	w http.ResponseWriter,
	r *http.Request,
	_ store.Device,
) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	writeE911ProvisioningUnavailable(w)
	return true
}

// handleWebsheet also rejects legacy self-hosted websheet URLs. This prevents
// old clients or bookmarks from treating locally collected data as completed
// carrier provisioning.
func (s *Server) handleWebsheet(w http.ResponseWriter, _ *http.Request) {
	writeE911ProvisioningUnavailable(w)
}

func writeE911ProvisioningUnavailable(w http.ResponseWriter) {
	writeError(
		w,
		http.StatusNotImplemented,
		"e911_provisioning_unavailable",
		"carrier-backed emergency address provisioning is not supported by this VoCat build",
	)
}
