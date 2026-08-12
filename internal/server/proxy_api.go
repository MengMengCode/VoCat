package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"vocat/internal/i18n"
	localproxy "vocat/internal/proxy"
	"vocat/internal/store"
)

func (s *Server) routeProxyAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	switch cleanPath {
	case "upstream-proxies":
		s.handleUpstreamProxies(w, r)
	case "upstream-proxy-probe":
		s.handleUpstreamProbeConfig(w, r)
	case "upstream-proxy-countries":
		if !requireMethod(w, r, http.MethodGet) {
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": proxyCountries})
	case "upstream-proxy-country-rules":
		s.handleCountryRules(w, r)
	case "upstream-proxy-profile-bindings":
		s.handleProfileProxyBindings(w, r)
	default:
		segments := splitAPIPath(cleanPath)
		switch {
		case len(segments) == 2 && segments[0] == "upstream-proxies":
			s.handleUpstreamProxy(w, r, segments[1])
		case len(segments) == 4 &&
			segments[0] == "upstream-proxies" &&
			segments[2] == "actions" &&
			segments[3] == "probe":
			s.handleUpstreamProbe(w, r, segments[1])
		case len(segments) == 2 && segments[0] == "upstream-proxy-country-rules":
			s.handleCountryRule(w, r, segments[1])
		default:
			return false
		}
	}
	return true
}

type upstreamProxyPayload struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Addr     string `json:"addr"`
	Username string `json:"username"`
	Password string `json:"password"`
	Enabled  bool   `json:"enabled"`
}

func (s *Server) handleUpstreamProxies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		values, err := s.store.ListUpstreamProxies(r.Context())
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		result := make([]map[string]any, 0, len(values))
		for _, value := range values {
			result = append(result, upstreamProxyResponse(value.Redacted()))
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	case http.MethodPost:
		var payload upstreamProxyPayload
		if err := s.decodeJSON(w, r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if !validObjectID(payload.ID) {
			writeError(w, http.StatusBadRequest, "invalid_proxy_id", "proxy ID must use 1-64 safe characters")
			return
		}
		s.saveAndProbeUpstream(w, r, payload)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) handleUpstreamProxy(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPut:
		var payload upstreamProxyPayload
		if err := s.decodeJSON(w, r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if payload.ID != "" && payload.ID != id {
			writeError(w, http.StatusConflict, "immutable_proxy_id", "upstream proxy ID cannot be changed")
			return
		}
		payload.ID = id
		s.saveAndProbeUpstream(w, r, payload)
	case http.MethodDelete:
		bindings, listErr := s.store.ListDeviceProxyBindings(r.Context())
		if listErr != nil {
			s.writeStoreError(w, listErr)
			return
		}
		if err := s.store.DeleteUpstreamProxy(r.Context(), id); err != nil {
			s.writeStoreError(w, err)
			return
		}
		for _, binding := range bindings {
			if binding.UpstreamProxyID == id {
				s.requestProfileProxyRouteReconnect(binding.DeviceID, binding.ICCID)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"deleted": true}})
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

type profileProxyBindingPayload struct {
	DeviceID    string `json:"device_id"`
	ICCID       string `json:"iccid"`
	ProfileName string `json:"profile_name"`
	// Accepted for compatibility with the first profile-picker bundle, which
	// sent the read-only display state together with the writable identity.
	StateText string `json:"state_text,omitempty"`
}

func (s *Server) handleProfileProxyBindings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		values, err := s.store.ListDeviceProxyBindings(r.Context())
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		result := make([]map[string]any, 0, len(values))
		for _, value := range values {
			result = append(result, deviceProxyBindingResponse(value))
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	case http.MethodPost:
		var request struct {
			UpstreamProxyID string                       `json:"upstream_proxy_id"`
			Bindings        []profileProxyBindingPayload `json:"bindings"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		request.UpstreamProxyID = strings.TrimSpace(request.UpstreamProxyID)
		upstream, err := s.store.UpstreamProxy(r.Context(), request.UpstreamProxyID)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !upstream.Enabled {
			writeError(w, http.StatusConflict, "upstream_proxy_disabled", "enable the upstream proxy before binding a profile")
			return
		}
		if len(request.Bindings) == 0 || len(request.Bindings) > 200 {
			writeError(w, http.StatusBadRequest, "invalid_bindings", "select between 1 and 200 profiles")
			return
		}
		values := make([]store.DeviceProxyBinding, 0, len(request.Bindings))
		seen := make(map[string]struct{}, len(request.Bindings))
		for _, item := range request.Bindings {
			deviceID := strings.TrimSpace(item.DeviceID)
			iccid := strings.TrimSpace(item.ICCID)
			if !validDeviceID(deviceID) {
				writeError(w, http.StatusBadRequest, "invalid_device_id", "device ID must use 1-64 safe characters")
				return
			}
			if !validProfileICCID(iccid) {
				writeError(w, http.StatusBadRequest, "invalid_iccid", "profile ICCID must contain 18 to 22 digits")
				return
			}
			if _, duplicate := seen[iccid]; duplicate {
				writeError(w, http.StatusBadRequest, "duplicate_iccid", "the same ICCID was selected more than once")
				return
			}
			seen[iccid] = struct{}{}
			if _, err := s.store.Device(r.Context(), deviceID); err != nil {
				s.writeStoreError(w, err)
				return
			}
			if existing, err := s.store.DeviceProxyBinding(r.Context(), iccid); err == nil && existing.UpstreamProxyID != upstream.ID {
				writeError(w, http.StatusConflict, "profile_already_bound", "this ICCID is already bound to another upstream proxy; delete that binding first")
				return
			} else if err != nil && !errors.Is(err, store.ErrNotFound) {
				s.writeStoreError(w, err)
				return
			}
			name := strings.TrimSpace(item.ProfileName)
			if name == "" {
				name = iccid
			}
			values = append(values, store.DeviceProxyBinding{DeviceID: deviceID, ICCID: iccid, ProfileName: name, UpstreamProxyID: upstream.ID})
		}
		requested := false
		var reconnectErrors []string
		for _, value := range values {
			if err := s.store.UpsertDeviceProxyBinding(r.Context(), value); err != nil {
				s.writeStoreError(w, err)
				return
			}
			reconnected, reconnectErr := s.requestProfileProxyRouteReconnect(value.DeviceID, value.ICCID)
			requested = requested || reconnected
			if reconnectErr != nil {
				reconnectErrors = append(reconnectErrors, reconnectErr.Error())
			}
		}
		response := map[string]any{"created": len(values), "reconnect_requested": requested}
		if len(reconnectErrors) > 0 {
			response["reconnect_error"] = strings.Join(reconnectErrors, "; ")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": response})
	case http.MethodDelete:
		var request struct {
			UpstreamProxyID string   `json:"upstream_proxy_id"`
			ICCIDs          []string `json:"iccids"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if len(request.ICCIDs) == 0 || len(request.ICCIDs) > 200 {
			writeError(w, http.StatusBadRequest, "invalid_bindings", "select between 1 and 200 profiles")
			return
		}
		requested := false
		deleted := 0
		var reconnectErrors []string
		for _, rawICCID := range request.ICCIDs {
			iccid := strings.TrimSpace(rawICCID)
			binding, err := s.store.DeviceProxyBinding(r.Context(), iccid)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			if strings.TrimSpace(request.UpstreamProxyID) != "" && binding.UpstreamProxyID != strings.TrimSpace(request.UpstreamProxyID) {
				writeError(w, http.StatusConflict, "binding_proxy_mismatch", "selected ICCID is not bound to this upstream proxy")
				return
			}
			if err := s.store.DeleteDeviceProxyBinding(r.Context(), iccid); err != nil {
				s.writeStoreError(w, err)
				return
			}
			deleted++
			reconnected, reconnectErr := s.requestProfileProxyRouteReconnect(binding.DeviceID, binding.ICCID)
			requested = requested || reconnected
			if reconnectErr != nil {
				reconnectErrors = append(reconnectErrors, reconnectErr.Error())
			}
		}
		response := map[string]any{"deleted": deleted, "reconnect_requested": requested}
		if len(reconnectErrors) > 0 {
			response["reconnect_error"] = strings.Join(reconnectErrors, "; ")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": response})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// A binding is already durable before this is called. Reconnect failures are
// returned as advisory information: the chosen route will still be used on
// the next VoWiFi start/reconnect.
func (s *Server) requestProfileProxyRouteReconnect(deviceID, iccid string) (bool, error) {
	if s.vowifi == nil {
		return false, nil
	}
	config, err := s.store.Device(context.Background(), deviceID)
	if err != nil {
		return false, err
	}
	if !config.VoWiFiEnabled {
		return false, nil
	}
	state, stateErr := s.vowifi.State(deviceID)
	if stateErr != nil || strings.TrimSpace(state.ICCID) == "" || strings.TrimSpace(state.ICCID) != strings.TrimSpace(iccid) {
		return false, nil
	}
	if _, err := s.vowifi.RequestReconnect(deviceID); err != nil {
		s.logger.Warn("VoWiFi proxy route saved but immediate reconnect was not started", "device_id", deviceID, "error", err)
		return false, err
	}
	return true, nil
}

func validProfileICCID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 18 || len(value) > 22 {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (s *Server) saveAndProbeUpstream(
	w http.ResponseWriter,
	r *http.Request,
	payload upstreamProxyPayload,
) {
	value := store.UpstreamProxy{
		ID:       payload.ID,
		Name:     payload.Name,
		Addr:     payload.Addr,
		Username: payload.Username,
		Password: payload.Password,
		Enabled:  payload.Enabled,
	}
	if err := s.store.UpsertUpstreamProxy(r.Context(), value); err != nil {
		s.writeStoreError(w, err)
		return
	}
	saved, err := s.store.UpstreamProxy(r.Context(), value.ID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	bindings, err := s.store.ListDeviceProxyBindings(r.Context())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	for _, binding := range bindings {
		if binding.UpstreamProxyID == saved.ID {
			s.requestProfileProxyRouteReconnect(binding.DeviceID, binding.ICCID)
		}
	}
	probe, probeErr := localproxy.ProbeSOCKS5(
		r.Context(),
		saved.Addr,
		saved.Username,
		saved.Password,
		8*time.Second,
	)
	probeResponse := probeMap(probe, probeErr)
	message := i18n.T("代理已保存；UDP ASSOCIATE 尚未通过。")
	if probeErr == nil && probe.UDPAssociateOK {
		message = i18n.T("代理已保存，SOCKS5 认证与 UDP ASSOCIATE 均通过。")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"status":  "saved",
			"proxy":   upstreamProxyResponse(saved.Redacted()),
			"probe":   probeResponse,
			"message": message,
		},
	})
}

func (s *Server) handleUpstreamProbe(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	value, err := s.store.UpstreamProxy(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	result, probeErr := localproxy.ProbeSOCKS5(
		r.Context(),
		value.Addr,
		value.Username,
		value.Password,
		8*time.Second,
	)
	message := i18n.T("代理不能承载 VoWiFi 所需的 UDP。")
	if probeErr == nil && result.UDPAssociateOK {
		message = i18n.T("SOCKS5 认证与 UDP ASSOCIATE 探测通过。")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"status":  "probed",
			"probe":   probeMap(result, probeErr),
			"message": message,
		},
	})
}

// handleUpstreamProbeConfig probes a front proxy straight from the editor's
// form values, so connectivity (above all UDP ASSOCIATE, which VoWiFi depends
// on) can be verified before the proxy is ever saved. When the form edits an
// existing proxy and leaves the password blank (meaning "keep the stored
// secret"), the stored record supplies the missing credentials.
func (s *Server) handleUpstreamProbeConfig(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var payload upstreamProxyPayload
	if err := s.decodeJSON(w, r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	addr := strings.TrimSpace(payload.Addr)
	username := strings.TrimSpace(payload.Username)
	password := payload.Password
	if id := strings.TrimSpace(payload.ID); id != "" {
		if stored, err := s.store.UpstreamProxy(r.Context(), id); err == nil {
			if addr == "" {
				addr = stored.Addr
			}
			if username == "" {
				username = stored.Username
			}
			if password == "" || password == store.SecretMask {
				password = stored.Password
			}
		}
	}
	if addr == "" {
		writeError(w, http.StatusBadRequest, "invalid_proxy_addr", "Socks5 address is required")
		return
	}
	result, probeErr := localproxy.ProbeSOCKS5(
		r.Context(),
		addr,
		username,
		password,
		8*time.Second,
	)
	message := i18n.T("代理不能承载 VoWiFi 所需的 UDP。")
	if probeErr == nil && result.UDPAssociateOK {
		message = i18n.T("SOCKS5 认证与 UDP ASSOCIATE 探测通过。")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"status":  "probed",
			"probe":   probeMap(result, probeErr),
			"message": message,
		},
	})
}

func (s *Server) handleCountryRules(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	values, err := s.store.ListCountryRules(r.Context())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		result = append(result, countryRuleResponse(value))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) handleCountryRule(w http.ResponseWriter, r *http.Request, countryCode string) {
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	switch r.Method {
	case http.MethodPut:
		var request struct {
			UpstreamProxyID string `json:"upstream_proxy_id"`
			Enabled         bool   `json:"enabled"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		country := countryByCode(countryCode)
		if country == nil {
			writeError(w, http.StatusBadRequest, "invalid_country", "country code is not in the supported MCC table")
			return
		}
		if _, err := s.store.UpstreamProxy(r.Context(), request.UpstreamProxyID); err != nil {
			s.writeStoreError(w, err)
			return
		}
		value := store.CountryRule{
			CountryCode:     countryCode,
			CountryName:     country.Name,
			UpstreamProxyID: request.UpstreamProxyID,
			Enabled:         request.Enabled,
		}
		if err := s.store.UpsertCountryRule(r.Context(), value); err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": countryRuleResponse(value)})
	case http.MethodDelete:
		if err := s.store.DeleteCountryRule(r.Context(), countryCode); err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"deleted": true}})
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func upstreamProxyResponse(value store.UpstreamProxy) map[string]any {
	return map[string]any{
		"id":       value.ID,
		"name":     value.Name,
		"addr":     value.Addr,
		"username": value.Username,
		"password": value.Password,
		"enabled":  value.Enabled,
	}
}

func countryRuleResponse(value store.CountryRule) map[string]any {
	return map[string]any{
		"country_code":      value.CountryCode,
		"country_name":      value.CountryName,
		"upstream_proxy_id": value.UpstreamProxyID,
		"enabled":           value.Enabled,
	}
}

func deviceProxyBindingResponse(value store.DeviceProxyBinding) map[string]any {
	return map[string]any{
		"device_id":         value.DeviceID,
		"iccid":             value.ICCID,
		"profile_name":      value.ProfileName,
		"upstream_proxy_id": value.UpstreamProxyID,
	}
}

func probeMap(result localproxy.ProbeResult, err error) map[string]any {
	encoded, _ := json.Marshal(result)
	var response map[string]any
	_ = json.Unmarshal(encoded, &response)
	if err != nil {
		response["error"] = err.Error()
	}
	return response
}

func validObjectID(value string) bool {
	return validDeviceID(value)
}

type proxyCountry struct {
	Code string
	Name string
	MCCs []string
}

func (country proxyCountry) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"country_code": country.Code,
		"country_name": i18n.T(country.Name),
		"mccs":         country.MCCs,
	})
}

func countryByCode(code string) *proxyCountry {
	for index := range proxyCountries {
		if proxyCountries[index].Code == code {
			return &proxyCountries[index]
		}
	}
	return nil
}

// countryNameForMCC resolves a mobile country code to a display name using the
// shared MCC table. It returns an empty string for an unknown or empty MCC.
func countryNameForMCC(mcc string) string {
	if mcc == "" {
		return ""
	}
	for index := range proxyCountries {
		for _, candidate := range proxyCountries[index].MCCs {
			if candidate == mcc {
				return i18n.T(proxyCountries[index].Name)
			}
		}
	}
	return ""
}

var proxyCountries = []proxyCountry{
	{Code: "CN", Name: "中国", MCCs: []string{"460", "461"}},
	{Code: "HK", Name: "中国香港", MCCs: []string{"454"}},
	{Code: "MO", Name: "中国澳门", MCCs: []string{"455"}},
	{Code: "TW", Name: "中国台湾", MCCs: []string{"466"}},
	{Code: "US", Name: "美国", MCCs: []string{"310", "311", "312", "313", "314", "315", "316"}},
	{Code: "CA", Name: "加拿大", MCCs: []string{"302"}},
	{Code: "GB", Name: "英国", MCCs: []string{"234", "235"}},
	{Code: "DE", Name: "德国", MCCs: []string{"262"}},
	{Code: "FR", Name: "法国", MCCs: []string{"208"}},
	{Code: "IT", Name: "意大利", MCCs: []string{"222"}},
	{Code: "ES", Name: "西班牙", MCCs: []string{"214"}},
	{Code: "PT", Name: "葡萄牙", MCCs: []string{"268"}},
	{Code: "NL", Name: "荷兰", MCCs: []string{"204"}},
	{Code: "BE", Name: "比利时", MCCs: []string{"206"}},
	{Code: "CH", Name: "瑞士", MCCs: []string{"228"}},
	{Code: "AT", Name: "奥地利", MCCs: []string{"232"}},
	{Code: "IE", Name: "爱尔兰", MCCs: []string{"272"}},
	{Code: "DK", Name: "丹麦", MCCs: []string{"238"}},
	{Code: "SE", Name: "瑞典", MCCs: []string{"240"}},
	{Code: "NO", Name: "挪威", MCCs: []string{"242"}},
	{Code: "FI", Name: "芬兰", MCCs: []string{"244"}},
	{Code: "PL", Name: "波兰", MCCs: []string{"260"}},
	{Code: "CZ", Name: "捷克", MCCs: []string{"230"}},
	{Code: "GR", Name: "希腊", MCCs: []string{"202"}},
	{Code: "RO", Name: "罗马尼亚", MCCs: []string{"226"}},
	{Code: "HU", Name: "匈牙利", MCCs: []string{"216"}},
	{Code: "UA", Name: "乌克兰", MCCs: []string{"255"}},
	{Code: "RU", Name: "俄罗斯", MCCs: []string{"250"}},
	{Code: "TR", Name: "土耳其", MCCs: []string{"286"}},
	{Code: "JP", Name: "日本", MCCs: []string{"440", "441"}},
	{Code: "KR", Name: "韩国", MCCs: []string{"450"}},
	{Code: "SG", Name: "新加坡", MCCs: []string{"525"}},
	{Code: "MY", Name: "马来西亚", MCCs: []string{"502"}},
	{Code: "TH", Name: "泰国", MCCs: []string{"520"}},
	{Code: "VN", Name: "越南", MCCs: []string{"452"}},
	{Code: "PH", Name: "菲律宾", MCCs: []string{"515"}},
	{Code: "ID", Name: "印度尼西亚", MCCs: []string{"510"}},
	{Code: "IN", Name: "印度", MCCs: []string{"404", "405", "406"}},
	{Code: "PK", Name: "巴基斯坦", MCCs: []string{"410"}},
	{Code: "AE", Name: "阿联酋", MCCs: []string{"424", "430", "431"}},
	{Code: "SA", Name: "沙特阿拉伯", MCCs: []string{"420"}},
	{Code: "IL", Name: "以色列", MCCs: []string{"425"}},
	{Code: "AU", Name: "澳大利亚", MCCs: []string{"505"}},
	{Code: "NZ", Name: "新西兰", MCCs: []string{"530"}},
	{Code: "BR", Name: "巴西", MCCs: []string{"724"}},
	{Code: "MX", Name: "墨西哥", MCCs: []string{"334"}},
	{Code: "AR", Name: "阿根廷", MCCs: []string{"722"}},
	{Code: "CL", Name: "智利", MCCs: []string{"730"}},
	{Code: "CO", Name: "哥伦比亚", MCCs: []string{"732"}},
	{Code: "ZA", Name: "南非", MCCs: []string{"655"}},
	{Code: "EG", Name: "埃及", MCCs: []string{"602"}},
	{Code: "NG", Name: "尼日利亚", MCCs: []string{"621"}},
	{Code: "KE", Name: "肯尼亚", MCCs: []string{"639"}},
}
