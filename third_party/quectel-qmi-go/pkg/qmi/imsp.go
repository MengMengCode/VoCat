package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
)

// ============================================================================
// IMSP Service Wrapper / IMSP 服务封装
// ============================================================================

type IMSPService struct {
	client   *Client
	clientID uint8
}

func NewIMSPService(client *Client) (*IMSPService, error) {
	clientID, err := client.AllocateClientID(ServiceIMSP)
	if err != nil {
		return nil, err
	}
	return &IMSPService{client: client, clientID: clientID}, nil
}

func (i *IMSPService) Close() error {
	return i.client.ReleaseClientID(ServiceIMSP, i.clientID)
}

// ============================================================================
// IMSP Types / IMSP 类型
// ============================================================================

type IMSPEnablerState uint32

const (
	IMSPEnablerStateUninitialized IMSPEnablerState = 0x01
	IMSPEnablerStateInitialized   IMSPEnablerState = 0x02
	IMSPEnablerStateAirplane      IMSPEnablerState = 0x03
	IMSPEnablerStateRegistered    IMSPEnablerState = 0x04
)

const IMSPGetEnablerState uint16 = 0x0024

func (i *IMSPService) GetEnablerState(ctx context.Context) (IMSPEnablerState, error) {
	resp, err := i.client.SendRequest(ctx, ServiceIMSP, i.clientID, IMSPGetEnablerState, nil)
	if err != nil {
		return 0, err
	}
	return parseIMSPGetEnablerStateResponse(resp)
}

func parseIMSPGetEnablerStateResponse(resp *Packet) (IMSPEnablerState, error) {
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("get imsp enabler state failed: %w", err)
	}
	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 4 {
		return 0, fmt.Errorf("imsp enabler state TLV missing")
	}
	return IMSPEnablerState(binary.LittleEndian.Uint32(tlv.Value[0:4])), nil
}
