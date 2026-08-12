package vowifi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClassifyErrorEAPAuthenticationRejected(t *testing.T) {
	err := fmt.Errorf("tunnel setup: %w", ErrEAPAuthenticationRejected)
	if got := classifyError(PhaseTunnelReady, err); got != "eap_authentication_rejected" {
		t.Fatalf("classifyError = %q", got)
	}
}

func TestCleanupCallContainsProviderThatIgnoresContext(t *testing.T) {
	orchestrator := &Orchestrator{options: Options{CleanupTimeout: 20 * time.Millisecond}}
	release := make(chan struct{})
	defer close(release)
	started := time.Now()
	err := orchestrator.cleanupCall(func(context.Context) error {
		<-release
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanupCall() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("cleanupCall() took %v", elapsed)
	}
}

type fakeEnvironment struct {
	mu sync.Mutex

	calls      []string
	failCounts map[string]int
	blockAt    string
	blocked    chan struct{}
	blockOnce  sync.Once

	identity       SIMIdentity
	akaEvidence    AKAEvidence
	radioSnapshot  RadioSnapshot
	proxy          ProxyRoute
	tunnelEvidence TunnelEvidence
	imsEvidence    IMSEvidence
	smsEvidence    SMSEvidence
	phoneRecords   []PhoneRecord
	tunnelRequests []TunnelRequest
	tunnelFailures chan error
	imsFailures    chan error
}

func newFakeEnvironment() *fakeEnvironment {
	return &fakeEnvironment{
		failCounts: make(map[string]int),
		blocked:    make(chan struct{}),
		identity: SIMIdentity{
			ICCID:           "8944100000000000000",
			IMSI:            "234150000000000",
			IMEI:            "860000000000000",
			HomeMCC:         "234",
			HomeMNC:         "15",
			HomeCountryCode: "GB",
		},
		akaEvidence: AKAEvidence{
			Ready:       true,
			Application: "usim",
		},
		radioSnapshot: RadioSnapshot{
			CellularDataEnabled: true,
			OperatingMode:       1,
			PureAirplanePolicy:  false,
		},
		proxy: ProxyRoute{Mode: ProxyModeDirect},
		tunnelEvidence: TunnelEvidence{
			Established:   true,
			Name:          "vowifi0",
			ResponderAUTH: ResponderAUTHVerified,
			IKEEncryption: "aes-cbc-128",
			IKEIntegrity:  "hmac-sha2-256",
			IKEDHGroup:    "modp2048",
			ESPEncryption: "aes-cbc-128",
			ESPIntegrity:  "hmac-sha1-96",
		},
		imsEvidence: IMSEvidence{
			Registered:        true,
			RegistrationState: "registered",
			AssociatedMSISDN:  "+447700900123@ims.mnc015.mcc234.3gppnetwork.org",
			PAssociatedURI:    []string{"sip:234150000000000@ims.mnc015.mcc234.3gppnetwork.org"},
			Transport:         "tcp",
			LastSIPCode:       200,
		},
		smsEvidence: SMSEvidence{Ready: true},
	}
}

func (environment *fakeEnvironment) record(ctx context.Context, call string) error {
	environment.mu.Lock()
	environment.calls = append(environment.calls, call)
	block := environment.blockAt == call
	if remaining := environment.failCounts[call]; remaining > 0 {
		environment.failCounts[call] = remaining - 1
		environment.mu.Unlock()
		return errors.New(call + " failed")
	}
	environment.mu.Unlock()

	if block {
		environment.blockOnce.Do(func() { close(environment.blocked) })
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (environment *fakeEnvironment) callsSnapshot() []string {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	return append([]string(nil), environment.calls...)
}

func (environment *fakeEnvironment) callCount(call string) int {
	count := 0
	for _, recorded := range environment.callsSnapshot() {
		if recorded == call {
			count++
		}
	}
	return count
}

func (environment *fakeEnvironment) setFailure(call string, count int) {
	environment.mu.Lock()
	environment.failCounts[call] = count
	environment.mu.Unlock()
}

type fakeSIM struct{ environment *fakeEnvironment }

func (fake fakeSIM) ReadIdentity(ctx context.Context, _ string) (SIMIdentity, error) {
	if err := fake.environment.record(ctx, "sim.identity"); err != nil {
		return SIMIdentity{}, err
	}
	fake.environment.mu.Lock()
	identity := fake.environment.identity
	fake.environment.mu.Unlock()
	return identity, nil
}

func (environment *fakeEnvironment) setIdentity(identity SIMIdentity) {
	environment.mu.Lock()
	environment.identity = identity
	environment.mu.Unlock()
}

type fakeAKA struct{ environment *fakeEnvironment }

func (fake fakeAKA) CheckReady(ctx context.Context, _ SIMIdentity) (AKAEvidence, error) {
	if err := fake.environment.record(ctx, "aka.ready"); err != nil {
		return AKAEvidence{}, err
	}
	return fake.environment.akaEvidence, nil
}

func (fake fakeAKA) Authenticate(ctx context.Context, _ SIMIdentity, _ AKAChallenge) (AKAResult, error) {
	if err := fake.environment.record(ctx, "aka.authenticate"); err != nil {
		return AKAResult{}, err
	}
	return AKAResult{
		RES: []byte{0x01, 0x02, 0x03, 0x04},
		CK:  make([]byte, 16),
		IK:  make([]byte, 16),
	}, nil
}

type fakeRadio struct{ environment *fakeEnvironment }

func (fake fakeRadio) Snapshot(ctx context.Context, _ string) (RadioSnapshot, error) {
	if err := fake.environment.record(ctx, "radio.snapshot"); err != nil {
		return RadioSnapshot{}, err
	}
	return fake.environment.radioSnapshot, nil
}

func (fake fakeRadio) StopCellularData(ctx context.Context, _ string) error {
	return fake.environment.record(ctx, "radio.stop_data")
}

func (fake fakeRadio) EnterVoWiFiRFOff(ctx context.Context, _ string) error {
	return fake.environment.record(ctx, "radio.rf_off")
}

func (fake fakeRadio) Restore(ctx context.Context, _ string, _ RadioSnapshot) error {
	return fake.environment.record(ctx, "radio.restore")
}

type fakeProxy struct{ environment *fakeEnvironment }

func (fake fakeProxy) Resolve(ctx context.Context, _ ProxyRequest) (ProxyRoute, error) {
	if err := fake.environment.record(ctx, "proxy.resolve"); err != nil {
		return ProxyRoute{}, err
	}
	return fake.environment.proxy, nil
}

type fakeTunnelProvider struct{ environment *fakeEnvironment }

func (fake fakeTunnelProvider) Start(ctx context.Context, request TunnelRequest) (TunnelSession, error) {
	if err := fake.environment.record(ctx, "tunnel.start"); err != nil {
		return nil, err
	}
	fake.environment.mu.Lock()
	fake.environment.tunnelRequests = append(fake.environment.tunnelRequests, request)
	fake.environment.mu.Unlock()
	return &fakeTunnelSession{environment: fake.environment}, nil
}

type fakeTunnelSession struct{ environment *fakeEnvironment }

func (fake *fakeTunnelSession) Evidence() TunnelEvidence {
	_ = fake.environment.record(context.Background(), "tunnel.evidence")
	return fake.environment.tunnelEvidence
}

func (fake *fakeTunnelSession) Close(ctx context.Context) error {
	return fake.environment.record(ctx, "tunnel.close")
}

func (fake *fakeTunnelSession) Failures() <-chan error {
	return fake.environment.tunnelFailures
}

type fakeIMSProvider struct{ environment *fakeEnvironment }

func (fake fakeIMSProvider) Start(ctx context.Context, _ IMSRequest) (IMSSession, error) {
	if err := fake.environment.record(ctx, "ims.start"); err != nil {
		return nil, err
	}
	return &fakeIMSSession{environment: fake.environment}, nil
}

type fakeIMSSession struct{ environment *fakeEnvironment }

func (fake *fakeIMSSession) Evidence() IMSEvidence {
	_ = fake.environment.record(context.Background(), "ims.evidence")
	return fake.environment.imsEvidence
}

func (fake *fakeIMSSession) EnableSMS(ctx context.Context) (SMSEvidence, error) {
	if err := fake.environment.record(ctx, "ims.sms"); err != nil {
		return SMSEvidence{}, err
	}
	return fake.environment.smsEvidence, nil
}

func (fake *fakeIMSSession) SendSMS(ctx context.Context, request SMSSubmitRequest) (SMSSubmitResult, error) {
	if err := fake.environment.record(ctx, "ims.send_sms"); err != nil {
		return SMSSubmitResult{}, err
	}
	return SMSSubmitResult{
		To:               request.Recipient,
		PartsTotal:       1,
		PartsAttempted:   1,
		PartsAccepted:    1,
		AllPartsAccepted: true,
		SubmissionStatus: "accepted_by_ims",
	}, nil
}

func (fake *fakeIMSSession) Close(ctx context.Context) error {
	return fake.environment.record(ctx, "ims.close")
}

func (fake *fakeIMSSession) Failures() <-chan error {
	return fake.environment.imsFailures
}

type fakePhones struct{ environment *fakeEnvironment }

func (fake fakePhones) SaveAssociatedNumber(ctx context.Context, record PhoneRecord) error {
	if err := fake.environment.record(ctx, "phone.save"); err != nil {
		return err
	}
	fake.environment.mu.Lock()
	fake.environment.phoneRecords = append(fake.environment.phoneRecords, record)
	fake.environment.mu.Unlock()
	return nil
}

func newTestOrchestrator(t *testing.T, environment *fakeEnvironment, allowMissingAUTH bool) *Orchestrator {
	t.Helper()
	return newTestOrchestratorWithOptions(t, environment, Options{
		DeviceID:                  "EC20",
		AllowMissingResponderAUTH: allowMissingAUTH,
		CleanupTimeout:            time.Second,
	})
}

func newTestOrchestratorWithOptions(
	t *testing.T,
	environment *fakeEnvironment,
	options Options,
) *Orchestrator {
	t.Helper()
	orchestrator, err := New(Dependencies{
		SIM:    fakeSIM{environment},
		AKA:    fakeAKA{environment},
		Radio:  fakeRadio{environment},
		Proxy:  fakeProxy{environment},
		Tunnel: fakeTunnelProvider{environment},
		IMS:    fakeIMSProvider{environment},
		Phones: fakePhones{environment},
	}, options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return orchestrator
}

func TestEnableKeepsIMSAndNumberWhenSMSCapabilityIsOptional(t *testing.T) {
	environment := newFakeEnvironment()
	environment.setFailure("ims.sms", 1)
	orchestrator := newTestOrchestratorWithOptions(t, environment, Options{
		DeviceID:           "EC20",
		AllowIMSWithoutSMS: true,
		CleanupTimeout:     time.Second,
	})

	state, err := orchestrator.Enable(context.Background())
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if state.Phase != PhaseIMSReady || !state.Active || !state.TunnelReady ||
		!state.IMSReady || state.SMSReady {
		t.Fatalf("Enable() state = %+v", state)
	}
	if state.PhoneNumber != "+447700900123" {
		t.Fatalf("phone number = %q", state.PhoneNumber)
	}
	if state.LastReason != "ims_registered_sms_unavailable" ||
		len(state.Warnings) == 0 {
		t.Fatalf("optional SMS evidence = %+v", state)
	}
	if environment.callCount("ims.close") != 0 ||
		environment.callCount("tunnel.close") != 0 {
		t.Fatal("optional SMS failure tore down a valid IMS registration")
	}
}

func TestEnableUsesEvidenceBackedOrderAndDisableRollsBackInReverse(t *testing.T) {
	environment := newFakeEnvironment()
	orchestrator := newTestOrchestrator(t, environment, false)

	state, err := orchestrator.Enable(context.Background())
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if state.Phase != PhaseSMSReady ||
		!state.Enabled ||
		!state.Active ||
		!state.SIMReady ||
		!state.AccessReady ||
		!state.TunnelReady ||
		!state.IMSReady ||
		!state.SMSReady {
		t.Fatalf("Enable() state = %+v", state)
	}
	if state.PhoneNumber != "+447700900123" ||
		state.PhoneNumberSource != PhoneSourceAssociatedMSISDN {
		t.Fatalf("phone projection = %q (%q)", state.PhoneNumber, state.PhoneNumberSource)
	}
	if state.Security.ResponderAUTH != ResponderAUTHVerified || state.Security.HighRisk {
		t.Fatalf("security audit = %+v", state.Security)
	}
	if state.PureAirplanePolicy {
		t.Fatal("VoWiFi RF off must not enable the independent pure-airplane policy")
	}

	wantEnableCalls := []string{
		"radio.snapshot",
		"radio.rf_off",
		"radio.stop_data",
		"sim.identity",
		"aka.ready",
		"proxy.resolve",
		"tunnel.start",
		"tunnel.evidence",
		"ims.start",
		"ims.evidence",
		"phone.save",
		"ims.sms",
	}
	if calls := environment.callsSnapshot(); !reflect.DeepEqual(calls, wantEnableCalls) {
		t.Fatalf("enable calls = %#v, want %#v", calls, wantEnableCalls)
	}
	if len(environment.tunnelRequests) != 1 {
		t.Fatalf("tunnel request count = %d", len(environment.tunnelRequests))
	}
	request := environment.tunnelRequests[0]
	if request.EPDG != "epdg.epc.mnc015.mcc234.pub.3gppnetwork.org" {
		t.Fatalf("EPDG = %q", request.EPDG)
	}
	if request.Proxy.Mode != ProxyModeDirect || request.Security.AllowMissingResponderAUTH {
		t.Fatalf("tunnel request = %+v", request)
	}

	state, err = orchestrator.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if state.Phase != PhaseIdle || state.Enabled || state.Active ||
		state.TunnelReady || state.IMSReady || state.SMSReady {
		t.Fatalf("Disable() state = %+v", state)
	}
	if state.PhoneNumber != "+447700900123" {
		t.Fatal("disabling the runtime must not erase the ICCID-associated number projection")
	}
	calls := environment.callsSnapshot()
	wantCleanup := []string{"ims.close", "tunnel.close", "radio.restore"}
	if !reflect.DeepEqual(calls[len(calls)-len(wantCleanup):], wantCleanup) {
		t.Fatalf("cleanup tail = %#v, want %#v", calls, wantCleanup)
	}
}

func TestEnableFailuresCleanUpEveryAcquiredLayer(t *testing.T) {
	tests := []struct {
		name            string
		failCall        string
		mutate          func(*fakeEnvironment)
		wantError       error
		wantCleanupTail []string
	}{
		{name: "identity", failCall: "sim.identity"},
		{name: "aka", failCall: "aka.ready"},
		{name: "radio snapshot", failCall: "radio.snapshot"},
		{name: "stop data can partially mutate", failCall: "radio.stop_data"},
		{name: "rf off", failCall: "radio.rf_off"},
		{name: "proxy", failCall: "proxy.resolve"},
		{name: "tunnel start", failCall: "tunnel.start"},
		{
			name: "tunnel evidence",
			mutate: func(environment *fakeEnvironment) {
				environment.tunnelEvidence.Established = false
				environment.tunnelEvidence.ResponderAUTH = ResponderAUTHUnknown
			},
			wantError:       ErrTunnelNotEstablished,
			wantCleanupTail: []string{"tunnel.close"},
		},
		{
			name:            "IMS start",
			failCall:        "ims.start",
			wantCleanupTail: []string{"tunnel.close"},
		},
		{
			name: "IMS registration evidence",
			mutate: func(environment *fakeEnvironment) {
				environment.imsEvidence.Registered = false
			},
			wantError:       ErrIMSNotRegistered,
			wantCleanupTail: []string{"ims.close", "tunnel.close"},
		},
		{
			name:            "SMS activation",
			failCall:        "ims.sms",
			wantCleanupTail: []string{"ims.close", "tunnel.close"},
		},
		{
			name: "SMS evidence",
			mutate: func(environment *fakeEnvironment) {
				environment.smsEvidence.Ready = false
			},
			wantError:       ErrSMSNotReady,
			wantCleanupTail: []string{"ims.close", "tunnel.close"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newFakeEnvironment()
			if test.failCall != "" {
				environment.setFailure(test.failCall, 1)
			}
			if test.mutate != nil {
				test.mutate(environment)
			}
			orchestrator := newTestOrchestrator(t, environment, false)

			state, err := orchestrator.Enable(context.Background())
			if err == nil {
				t.Fatal("Enable() unexpectedly succeeded")
			}
			if test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("Enable() error = %v, want errors.Is(%v)", err, test.wantError)
			}
			if state.Phase != PhaseFailed || state.Active ||
				state.TunnelReady || state.IMSReady || state.SMSReady {
				t.Fatalf("failed state = %+v", state)
			}
			if environment.callCount("radio.restore") != 0 {
				t.Fatalf("failed VoWiFi attempt re-enabled cellular RF: %#v", environment.callsSnapshot())
			}
			if len(test.wantCleanupTail) > 0 {
				calls := environment.callsSnapshot()
				if len(calls) < len(test.wantCleanupTail) {
					t.Fatalf("calls = %#v", calls)
				}
				tail := calls[len(calls)-len(test.wantCleanupTail):]
				if !reflect.DeepEqual(tail, test.wantCleanupTail) {
					t.Fatalf("cleanup tail = %#v, want %#v", tail, test.wantCleanupTail)
				}
			}
		})
	}
}

func TestResponderAUTHPolicyIsStrictByDefaultAndAuditsExplicitCompatibility(t *testing.T) {
	t.Run("strict", func(t *testing.T) {
		environment := newFakeEnvironment()
		environment.tunnelEvidence.ResponderAUTH = ResponderAUTHMissing
		orchestrator := newTestOrchestrator(t, environment, false)

		state, err := orchestrator.Enable(context.Background())
		if !errors.Is(err, ErrResponderAUTHRequired) {
			t.Fatalf("Enable() error = %v", err)
		}
		if state.Phase != PhaseFailed || state.Security.HighRisk ||
			state.Security.CompatibilityOverride {
			t.Fatalf("strict security state = %+v", state.Security)
		}
	})

	t.Run("explicit compatibility", func(t *testing.T) {
		environment := newFakeEnvironment()
		environment.tunnelEvidence.ResponderAUTH = ResponderAUTHMissing
		orchestrator := newTestOrchestrator(t, environment, true)

		state, err := orchestrator.Enable(context.Background())
		if err != nil {
			t.Fatalf("Enable() error = %v", err)
		}
		if state.Phase != PhaseSMSReady ||
			!state.Security.HighRisk ||
			!state.Security.CompatibilityOverride ||
			state.Security.Level != AuditLevelHigh ||
			state.Security.Code != AuditCodeMissingResponderAUTH {
			t.Fatalf("compatibility security state = %+v", state.Security)
		}
		if !environment.tunnelRequests[0].Security.AllowMissingResponderAUTH {
			t.Fatal("explicit compatibility policy was not passed to the tunnel provider")
		}
	})

	t.Run("invalid is never compatible", func(t *testing.T) {
		environment := newFakeEnvironment()
		environment.tunnelEvidence.ResponderAUTH = ResponderAUTHInvalid
		orchestrator := newTestOrchestrator(t, environment, true)

		state, err := orchestrator.Enable(context.Background())
		if !errors.Is(err, ErrResponderAUTHRequired) || state.Phase != PhaseFailed {
			t.Fatalf("Enable() = (%+v, %v)", state, err)
		}
	})
}

func TestPhoneNumberIsNeverInferredFromIMSI(t *testing.T) {
	environment := newFakeEnvironment()
	environment.identity.IMSI = "234159999999999"
	environment.imsEvidence.AssociatedMSISDN = ""
	environment.imsEvidence.PAssociatedURI = []string{
		"sip:234159999999999@ims.mnc015.mcc234.3gppnetwork.org",
	}
	orchestrator := newTestOrchestrator(t, environment, false)

	state, err := orchestrator.Enable(context.Background())
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if state.PhoneNumber != "" || environment.callCount("phone.save") != 0 {
		t.Fatalf("number was inferred: state=%+v records=%+v", state, environment.phoneRecords)
	}
	if len(state.Warnings) != 1 || !strings.Contains(state.Warnings[0], "not inferred from IMSI") {
		t.Fatalf("warnings = %#v", state.Warnings)
	}
}

func TestPhoneStoreFailureDoesNotMisreportOrTearDownWorkingIMS(t *testing.T) {
	environment := newFakeEnvironment()
	environment.setFailure("phone.save", 1)
	orchestrator := newTestOrchestrator(t, environment, false)

	state, err := orchestrator.Enable(context.Background())
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if state.Phase != PhaseSMSReady || !state.IMSReady || state.PhoneNumber != "" {
		t.Fatalf("state = %+v", state)
	}
	if len(state.Warnings) != 1 || !strings.Contains(state.Warnings[0], "could not be persisted") {
		t.Fatalf("warnings = %#v", state.Warnings)
	}
}

func TestDisableCancelsAnInFlightEnableAndRestoresRadio(t *testing.T) {
	environment := newFakeEnvironment()
	environment.blockAt = "tunnel.start"
	orchestrator := newTestOrchestrator(t, environment, false)

	enableResult := make(chan error, 1)
	go func() {
		_, err := orchestrator.Enable(context.Background())
		enableResult <- err
	}()

	select {
	case <-environment.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("Enable() did not reach blocking tunnel provider")
	}

	disableContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	state, err := orchestrator.Disable(disableContext)
	if err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if state.Phase != PhaseIdle || state.Enabled || state.Active {
		t.Fatalf("Disable() state = %+v", state)
	}
	select {
	case err := <-enableResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Enable() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Enable() did not exit after Disable() cancellation")
	}
	if environment.callCount("radio.restore") != 1 {
		t.Fatalf("radio.restore count = %d", environment.callCount("radio.restore"))
	}
}

func TestConcurrentEnableStartsOnlyOneRuntime(t *testing.T) {
	environment := newFakeEnvironment()
	orchestrator := newTestOrchestrator(t, environment, false)

	const goroutines = 24
	start := make(chan struct{})
	results := make(chan error, goroutines)
	var group sync.WaitGroup
	for index := 0; index < goroutines; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := orchestrator.Enable(context.Background())
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)

	successes := 0
	alreadyEnabled := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAlreadyEnabled):
			alreadyEnabled++
		default:
			t.Fatalf("unexpected Enable() error = %v", err)
		}
	}
	if successes != 1 || alreadyEnabled != goroutines-1 {
		t.Fatalf("successes=%d alreadyEnabled=%d", successes, alreadyEnabled)
	}
	if environment.callCount("tunnel.start") != 1 {
		t.Fatalf("tunnel.start count = %d", environment.callCount("tunnel.start"))
	}
}

func TestRetryAfterFailureCreatesANewAttempt(t *testing.T) {
	environment := newFakeEnvironment()
	environment.setFailure("tunnel.start", 1)
	orchestrator := newTestOrchestrator(t, environment, false)

	first, err := orchestrator.Enable(context.Background())
	if err == nil || first.Phase != PhaseFailed || first.Attempt != 1 {
		t.Fatalf("first Enable() = (%+v, %v)", first, err)
	}
	second, err := orchestrator.Retry(context.Background())
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if second.Phase != PhaseSMSReady || second.Attempt != 2 {
		t.Fatalf("Retry() state = %+v", second)
	}
	if environment.callCount("tunnel.start") != 2 {
		t.Fatalf("tunnel.start count = %d", environment.callCount("tunnel.start"))
	}
	if environment.callCount("radio.snapshot") != 1 || environment.callCount("radio.restore") != 0 {
		t.Fatalf("retry must retain RF-off checkpoint: %#v", environment.callsSnapshot())
	}
}

func TestFailedEnableRestoresRadioOnlyOnExplicitDisable(t *testing.T) {
	environment := newFakeEnvironment()
	environment.setFailure("tunnel.start", 1)
	orchestrator := newTestOrchestrator(t, environment, false)

	state, err := orchestrator.Enable(context.Background())
	if err == nil || state.Phase != PhaseFailed || !state.Enabled {
		t.Fatalf("Enable() = (%+v, %v)", state, err)
	}
	if environment.callCount("radio.restore") != 0 {
		t.Fatalf("failed enable restored cellular RF: %#v", environment.callsSnapshot())
	}

	state, err = orchestrator.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if state.Phase != PhaseIdle || state.Enabled {
		t.Fatalf("Disable() state = %+v", state)
	}
	if environment.callCount("radio.restore") != 1 {
		t.Fatalf("explicit disable did not restore cellular RF: %#v", environment.callsSnapshot())
	}
}

func TestReconnectClosesThenRebuildsTheRuntime(t *testing.T) {
	environment := newFakeEnvironment()
	orchestrator := newTestOrchestrator(t, environment, false)
	if _, err := orchestrator.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}

	state, err := orchestrator.Reconnect(context.Background())
	if err != nil {
		t.Fatalf("Reconnect() error = %v", err)
	}
	if state.Phase != PhaseSMSReady || state.Attempt != 2 {
		t.Fatalf("Reconnect() state = %+v", state)
	}
	if environment.callCount("tunnel.start") != 2 ||
		environment.callCount("tunnel.close") != 1 ||
		environment.callCount("radio.restore") != 0 ||
		environment.callCount("radio.snapshot") != 1 {
		t.Fatalf("calls = %#v", environment.callsSnapshot())
	}
}

// A non-fatal teardown error (for example the network rejecting SIP
// deregistration during IMS close) must not stop a reconnect from rebuilding
// the runtime; Disable still releases the local IMS, tunnel, and radio layers.
func TestReconnectToleratesCleanupFailureAndRebuilds(t *testing.T) {
	environment := newFakeEnvironment()
	orchestrator := newTestOrchestrator(t, environment, false)
	if _, err := orchestrator.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}

	environment.setFailure("ims.close", 1)

	state, err := orchestrator.Reconnect(context.Background())
	if err != nil {
		t.Fatalf("Reconnect() error = %v", err)
	}
	if state.Phase != PhaseSMSReady || state.Attempt != 2 {
		t.Fatalf("Reconnect() state = %+v", state)
	}
	if environment.callCount("ims.close") != 1 ||
		environment.callCount("tunnel.close") != 1 ||
		environment.callCount("radio.restore") != 0 ||
		environment.callCount("radio.snapshot") != 1 ||
		environment.callCount("tunnel.start") != 2 {
		t.Fatalf("calls = %#v", environment.callsSnapshot())
	}
}

func TestRuntimeTunnelFailureRevokesReadinessAndCleansEveryLayer(t *testing.T) {
	environment := newFakeEnvironment()
	environment.tunnelFailures = make(chan error, 1)
	orchestrator := newTestOrchestrator(t, environment, false)
	if _, err := orchestrator.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}

	environment.tunnelFailures <- errors.New("ESP relay stopped")
	deadline := time.Now().Add(2 * time.Second)
	for {
		state := orchestrator.State()
		if state.Phase == PhaseFailed {
			if state.Active || state.TunnelReady || state.IMSReady || state.SMSReady {
				t.Fatalf("stale runtime readiness survived failure: %+v", state)
			}
			if !state.Enabled || state.LastErrorClass != "tunnel_runtime" ||
				state.LastReason != "runtime_tunnel_failed" ||
				!strings.Contains(state.LastError, "ESP relay stopped") {
				t.Fatalf("runtime failure evidence = %+v", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for runtime failure; state = %+v", state)
		}
		time.Sleep(10 * time.Millisecond)
	}

	calls := environment.callsSnapshot()
	wantTail := []string{"ims.close", "tunnel.close"}
	if len(calls) < len(wantTail) ||
		!reflect.DeepEqual(calls[len(calls)-len(wantTail):], wantTail) {
		t.Fatalf("runtime failure cleanup tail = %#v", calls)
	}
}

func TestRuntimeIMSFailureRevokesRegistrationEvidence(t *testing.T) {
	environment := newFakeEnvironment()
	environment.imsFailures = make(chan error, 1)
	orchestrator := newTestOrchestrator(t, environment, false)
	if _, err := orchestrator.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}

	environment.imsFailures <- errors.New("registration refresh failed")
	deadline := time.Now().Add(2 * time.Second)
	for {
		state := orchestrator.State()
		if state.Phase == PhaseFailed {
			if state.TunnelReady || state.IMSReady || state.SMSReady ||
				state.LastErrorClass != "ims_runtime" ||
				state.LastReason != "runtime_ims_failed" {
				t.Fatalf("IMS runtime failure evidence = %+v", state)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for IMS runtime failure; state = %+v", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRuntimeSIMChangeRevokesOldSubscriberSMSWithoutRetry(t *testing.T) {
	environment := newFakeEnvironment()
	orchestrator := newTestOrchestratorWithOptions(t, environment, Options{
		DeviceID:              "EC20",
		CleanupTimeout:        time.Second,
		IdentityCheckInterval: 5 * time.Millisecond,
	})
	if _, err := orchestrator.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	changed := environment.identity
	changed.ICCID = "8999900000000000000"
	changed.IMSI = "310260999999999"
	changed.HomeMCC = "310"
	changed.HomeMNC = "260"
	environment.setIdentity(changed)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := orchestrator.State()
		if state.LastReason == "runtime_sim_identity_changed" {
			if state.Phase != PhaseIdle || state.Enabled || state.Active ||
				state.SIMReady || state.TunnelReady || state.IMSReady || state.SMSReady ||
				state.LastErrorClass != "sim_identity_changed" ||
				state.LastError != ErrSIMIdentityChanged.Error() {
				t.Fatalf("identity-change state = %+v", state)
			}
			calls := environment.callsSnapshot()
			wantTail := []string{"ims.close", "tunnel.close", "radio.restore"}
			if len(calls) < len(wantTail) || !reflect.DeepEqual(calls[len(calls)-len(wantTail):], wantTail) {
				t.Fatalf("identity-change cleanup tail = %#v", calls)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for SIM identity revocation; state=%+v", orchestrator.State())
}

func TestSendSMSRechecksLiveSIMIdentityAndRevokesStaleSession(t *testing.T) {
	environment := newFakeEnvironment()
	orchestrator := newTestOrchestrator(t, environment, false)
	if _, err := orchestrator.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	changed := environment.identity
	changed.ICCID = "8999900000000000000"
	changed.IMSI = "310260999999999"
	environment.setIdentity(changed)

	result, err := orchestrator.SendSMS(context.Background(), SMSSubmitRequest{
		Recipient: "+12025550123",
		Text:      "hello",
	})
	if !errors.Is(err, ErrSMSNotReady) || !errors.Is(err, ErrSIMIdentityChanged) {
		t.Fatalf("SendSMS error = %v, want SMS-not-ready identity change", err)
	}
	if result.PartsAttempted != 0 || environment.callCount("ims.send_sms") != 0 {
		t.Fatalf("stale IMS submission reached sender: result=%+v calls=%#v", result, environment.callsSnapshot())
	}
	state := orchestrator.State()
	if state.Phase != PhaseIdle || state.Enabled || state.Active || state.IMSReady || state.SMSReady ||
		state.LastReason != "runtime_sim_identity_changed" {
		t.Fatalf("identity-change state = %+v", state)
	}
}

func TestSendSMSFailsClosedWhenLiveSIMCannotBeRead(t *testing.T) {
	environment := newFakeEnvironment()
	orchestrator := newTestOrchestrator(t, environment, false)
	if _, err := orchestrator.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	environment.setFailure("sim.identity", 1)

	result, err := orchestrator.SendSMS(context.Background(), SMSSubmitRequest{
		Recipient: "+12025550123",
		Text:      "hello",
	})
	if !errors.Is(err, ErrSMSNotReady) {
		t.Fatalf("SendSMS error = %v, want ErrSMSNotReady", err)
	}
	if result.PartsAttempted != 0 || environment.callCount("ims.send_sms") != 0 {
		t.Fatalf("unverified IMS submission reached sender: result=%+v calls=%#v", result, environment.callsSnapshot())
	}
	if state := orchestrator.State(); state.Phase != PhaseSMSReady || !state.SMSReady {
		t.Fatalf("transient identity read failure tore down session: %+v", state)
	}
}

func TestSubscriptionPublishesOrderedEvidencePhases(t *testing.T) {
	environment := newFakeEnvironment()
	orchestrator := newTestOrchestrator(t, environment, false)
	updates, unsubscribe := orchestrator.Subscribe(32)
	defer unsubscribe()

	if _, err := orchestrator.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}

	var phases []Phase
	deadline := time.After(2 * time.Second)
	for {
		select {
		case state := <-updates:
			if len(phases) == 0 || phases[len(phases)-1] != state.Phase {
				phases = append(phases, state.Phase)
			}
			if state.Phase == PhaseSMSReady {
				want := []Phase{
					PhaseIdle,
					PhaseSIMReady,
					PhaseAccessReady,
					PhaseTunnelReady,
					PhaseIMSReady,
					PhaseSMSReady,
				}
				if !reflect.DeepEqual(phases, want) {
					t.Fatalf("phases = %#v, want %#v", phases, want)
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for phases; got %#v", phases)
		}
	}
}

func TestFailedAttemptKeepsRadioOffUntilExplicitDisable(t *testing.T) {
	environment := newFakeEnvironment()
	environment.setFailure("ims.sms", 1)
	environment.setFailure("ims.close", 1)
	environment.setFailure("tunnel.close", 1)
	environment.setFailure("radio.restore", 1)
	orchestrator := newTestOrchestrator(t, environment, false)

	state, err := orchestrator.Enable(context.Background())
	if err == nil {
		t.Fatal("Enable() unexpectedly succeeded")
	}
	if len(state.CleanupErrors) != 2 {
		t.Fatalf("cleanup errors = %#v", state.CleanupErrors)
	}
	calls := environment.callsSnapshot()
	wantTail := []string{"ims.close", "tunnel.close"}
	if !reflect.DeepEqual(calls[len(calls)-2:], wantTail) {
		t.Fatalf("cleanup tail = %#v", calls[len(calls)-2:])
	}
	for _, text := range []string{"close IMS", "close tunnel"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("error %q does not contain %q", err, text)
		}
	}
	if environment.callCount("radio.restore") != 0 {
		t.Fatalf("failed attempt restored RF unexpectedly: %#v", calls)
	}
	if _, disableErr := orchestrator.Disable(context.Background()); disableErr == nil ||
		!strings.Contains(disableErr.Error(), "restore radio") {
		t.Fatalf("Disable() error = %v, want retained radio restore failure", disableErr)
	}
}

func TestDisableCleanupWarningStillSettlesIdle(t *testing.T) {
	environment := newFakeEnvironment()
	orchestrator := newTestOrchestrator(t, environment, false)
	if _, err := orchestrator.Enable(context.Background()); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	environment.setFailure("ims.close", 1)

	state, err := orchestrator.Disable(context.Background())
	if !errors.Is(err, ErrCleanupIncomplete) {
		t.Fatalf("Disable() error = %v, want ErrCleanupIncomplete", err)
	}
	if state.Phase != PhaseIdle || state.Enabled || state.Active ||
		state.SIMReady || state.AccessReady || state.TunnelReady ||
		state.IMSReady || state.SMSReady {
		t.Fatalf("Disable() warning state = %+v", state)
	}
	if state.LastErrorClass != "cleanup_warning" ||
		state.LastReason != "disabled_with_cleanup_errors" ||
		len(state.CleanupErrors) != 1 {
		t.Fatalf("Disable() warning evidence = %+v", state)
	}
}

func TestNewRejectsMissingProvidersAndInvalidOptions(t *testing.T) {
	environment := newFakeEnvironment()
	dependencies := Dependencies{
		SIM:    fakeSIM{environment},
		AKA:    fakeAKA{environment},
		Radio:  fakeRadio{environment},
		Proxy:  fakeProxy{environment},
		Tunnel: fakeTunnelProvider{environment},
		IMS:    fakeIMSProvider{environment},
		Phones: fakePhones{environment},
	}
	if _, err := New(Dependencies{}, Options{DeviceID: "EC20"}); err == nil {
		t.Fatal("New() accepted missing providers")
	}
	if _, err := New(dependencies, Options{}); err == nil {
		t.Fatal("New() accepted empty device ID")
	}
}

// smscSIM is a SIM reader that also answers the service-centre probe, the
// shape EC20/EC25 firmware has through AT+CSCA?.
type smscSIM struct {
	environment *fakeEnvironment
	value       string
	err         error
	calls       int
}

func (fake *smscSIM) ReadIdentity(ctx context.Context, deviceID string) (SIMIdentity, error) {
	return fakeSIM{fake.environment}.ReadIdentity(ctx, deviceID)
}

func (fake *smscSIM) ReadSMSCenter(context.Context, string) (string, error) {
	fake.calls++
	return fake.value, fake.err
}

// smscAKA is an AKA provider that also answers the probe, the shape a native
// Qualcomm 410 has through QMI while its AT port stays silent.
type smscAKA struct {
	environment *fakeEnvironment
	value       string
	err         error
	calls       int
}

func (fake *smscAKA) CheckReady(ctx context.Context, identity SIMIdentity) (AKAEvidence, error) {
	return fakeAKA{fake.environment}.CheckReady(ctx, identity)
}

func (fake *smscAKA) Authenticate(
	ctx context.Context,
	identity SIMIdentity,
	challenge AKAChallenge,
) (AKAResult, error) {
	return fakeAKA{fake.environment}.Authenticate(ctx, identity, challenge)
}

func (fake *smscAKA) ReadSMSCenter(context.Context, string) (string, error) {
	fake.calls++
	return fake.value, fake.err
}

// smscDualRole fills both dependency slots with one object, as the EC20
// adapter does in production.
type smscDualRole struct {
	smscAKA
}

func (fake *smscDualRole) ReadIdentity(ctx context.Context, deviceID string) (SIMIdentity, error) {
	return fakeSIM{fake.environment}.ReadIdentity(ctx, deviceID)
}

func newSMSCenterOrchestrator(
	t *testing.T,
	environment *fakeEnvironment,
	sim SIMIdentityReader,
	aka AKAProvider,
) *Orchestrator {
	t.Helper()
	orchestrator, err := New(Dependencies{
		SIM:    sim,
		AKA:    aka,
		Radio:  fakeRadio{environment},
		Proxy:  fakeProxy{environment},
		Tunnel: fakeTunnelProvider{environment},
		IMS:    fakeIMSProvider{environment},
		Phones: fakePhones{environment},
	}, Options{DeviceID: "wwan0", CleanupTimeout: time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return orchestrator
}

func TestReadSMSCenterFallsBackToTheAKAProvider(t *testing.T) {
	environment := newFakeEnvironment()
	sim := &smscSIM{environment: environment, err: errors.New("AT+CSCA? is not supported")}
	aka := &smscAKA{environment: environment, value: "  +447785016005  "}
	orchestrator := newSMSCenterOrchestrator(t, environment, sim, aka)

	value, failures := orchestrator.readSMSCenter(context.Background())
	if value != "+447785016005" {
		t.Fatalf("service centre = %q, want %q", value, "+447785016005")
	}
	if len(failures) != 0 {
		t.Fatalf("failures = %v, want none once a source succeeded", failures)
	}
	if sim.calls != 1 || aka.calls != 1 {
		t.Fatalf("probe counts sim=%d aka=%d, want 1 and 1", sim.calls, aka.calls)
	}
}

func TestReadSMSCenterSkipsAnEmptySIMAnswer(t *testing.T) {
	environment := newFakeEnvironment()
	sim := &smscSIM{environment: environment, value: "   "}
	aka := &smscAKA{environment: environment, value: "+447785016005"}
	orchestrator := newSMSCenterOrchestrator(t, environment, sim, aka)

	value, failures := orchestrator.readSMSCenter(context.Background())
	if value != "+447785016005" {
		t.Fatalf("service centre = %q, want the AKA provider's value", value)
	}
	if len(failures) != 0 {
		t.Fatalf("failures = %v, want none; a blank answer is not an error", failures)
	}
}

func TestReadSMSCenterProbesADualRoleProviderOnce(t *testing.T) {
	environment := newFakeEnvironment()
	dual := &smscDualRole{smscAKA{environment: environment, err: errors.New("AT+CSCA? is not supported")}}
	orchestrator := newSMSCenterOrchestrator(t, environment, dual, dual)

	value, failures := orchestrator.readSMSCenter(context.Background())
	if value != "" {
		t.Fatalf("service centre = %q, want it empty", value)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one; the same provider must not be probed twice", failures)
	}
	if dual.calls != 1 {
		t.Fatalf("probe count = %d, want 1", dual.calls)
	}
}
