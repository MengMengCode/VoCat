package vowifi

import (
	"context"
	"errors"
	"testing"
)

type ussiCancelSession struct {
	IMSSession
	err   error
	calls int
	id    string
}

func (s *ussiCancelSession) CancelUSSI(_ context.Context, id string) error {
	s.calls++
	s.id = id
	return s.err
}

func TestCancelUSSIReportsUnavailableAndPropagatesNetworkFailure(t *testing.T) {
	networkErr := errors.New("BYE failed")
	for _, tc := range []struct {
		name      string
		ready     bool
		resources *runtimeResources
		want      error
	}{
		{"no resources", true, nil, ErrUSSINotReady},
		{"no IMS session", true, &runtimeResources{}, ErrUSSINotReady},
		{"not registered", false, &runtimeResources{ims: &ussiCancelSession{}}, ErrUSSINotReady},
		{"missing capability", true, &runtimeResources{ims: &fakeIMSSession{}}, ErrUSSINotReady},
		{"network failure", true, &runtimeResources{ims: &ussiCancelSession{err: networkErr}}, networkErr},
		{"success", true, &runtimeResources{ims: &ussiCancelSession{}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &Orchestrator{operation: make(chan struct{}, 1), resources: tc.resources, state: State{IMSReady: tc.ready}}
			o.operation <- struct{}{}
			if err := o.CancelUSSI(context.Background(), "dialog-1"); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.resources != nil {
				if s, ok := tc.resources.ims.(*ussiCancelSession); ok && tc.ready && (s.calls != 1 || s.id != "dialog-1") {
					t.Fatalf("incorrect dialog: %+v", s)
				}
			}
		})
	}
}
