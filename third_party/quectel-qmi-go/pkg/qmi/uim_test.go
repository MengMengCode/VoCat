package qmi

import "testing"

func TestDispatchUIMIndications(t *testing.T) {
	c := &Client{eventCh: make(chan Event, 4)}
	cases := []struct {
		msgID uint16
		want  EventType
	}{
		{msgID: UIMStatusChangeInd, want: EventSimStatusChanged},
		{msgID: UIMSessionClosedInd, want: EventUIMSessionClosed},
		{msgID: UIMRefreshInd, want: EventUIMRefresh},
		{msgID: UIMSlotStatusInd, want: EventUIMSlotStatus},
	}

	for _, tc := range cases {
		c.dispatchIndication(&Packet{ServiceType: ServiceUIM, MessageID: tc.msgID, IsIndication: true})
		evt := <-c.eventCh
		if evt.Type != tc.want {
			t.Fatalf("UIM msg 0x%04X dispatched as %v, want %v", tc.msgID, evt.Type, tc.want)
		}
	}
}
