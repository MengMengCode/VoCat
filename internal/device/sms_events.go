package device

import "strings"

// SMSArrivals receives a value when any modem announces a new SMS with
// +CMTI. It is only a wake-up signal: the receiver must list modem storage.
// The buffer holds one value, so a burst collapses into a single wake-up and
// an arrival during the receiver's scan wakes it once more afterwards.
func (manager *Manager) SMSArrivals() <-chan struct{} {
	return manager.smsArrivals
}

// signalSMSArrival returns an AT session URC handler that signals arrivals
// for +CMTI. It runs on the session's URC path, so it never blocks.
func signalSMSArrival(arrivals chan<- struct{}) func(string) {
	return func(line string) {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "+CMTI:") {
			return
		}
		select {
		case arrivals <- struct{}{}:
		default:
			// A wake-up is already pending; its scan will see this message.
		}
	}
}
