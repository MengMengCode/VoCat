package device

import "testing"

func TestSignalSMSArrivalOnlyForCMTI(t *testing.T) {
	arrivals := make(chan struct{}, 1)
	handle := signalSMSArrival(arrivals)
	handle(`+CMT: "+447700900123",,"26/10/18,10:00:00+00"`)
	if len(arrivals) != 0 {
		t.Fatal("non-+CMTI URC signalled an SMS arrival")
	}
	handle(`+CMTI: "SM",5`)
	if len(arrivals) != 1 {
		t.Fatal("+CMTI did not signal an SMS arrival")
	}
}
