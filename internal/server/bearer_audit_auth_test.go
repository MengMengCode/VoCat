package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"vocat/internal/store"
	"vocat/internal/vowifi"
)

type auditAuthCallMedia struct{ written chan []int16 }

func (media *auditAuthCallMedia) Codec() string { return "PCMA" }
func (media *auditAuthCallMedia) ReadPCM(ctx context.Context) ([]int16, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (media *auditAuthCallMedia) WritePCM(samples []int16) error {
	media.written <- append([]int16(nil), samples...)
	return nil
}

type auditAuthVoWiFi struct {
	fakeVoWiFiController
	media *auditAuthCallMedia
}

func (controller *auditAuthVoWiFi) CallMedia(context.Context, string, string) (vowifi.CallMedia, error) {
	return controller.media, nil
}

func TestAuditReadOnlyTokenCannotInjectCallAudio(t *testing.T) {
	_, service, database := newBearerTestApplication(t)
	token, err := service.CreateAPIToken(context.Background(), "audio-reader", true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	media := &auditAuthCallMedia{written: make(chan []int16, 1)}
	server := &Server{auth: service, store: database, logger: regionTestLogger(),
		vowifi: &auditAuthVoWiFi{fakeVoWiFiController: fakeVoWiFiController{state: vowifi.State{IMSReady: true}}, media: media}}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authenticated, ok := server.requireBearer(w, r)
		if !ok {
			return
		}
		server.handleCallMedia(w, authenticated, store.Device{ID: "configured"})
	}))
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/devices/configured/calls/media?call_id=active", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token.Token}},
	})
	if err != nil {
		if response != nil && response.StatusCode == http.StatusForbidden {
			return
		}
		t.Fatalf("unexpected dial failure: %v, response=%v", err, response)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x34, 0x12}); err != nil {
		t.Fatal(err)
	}
	select {
	case samples := <-media.written:
		t.Fatalf("read-only token reached WritePCM with audio samples %v", samples)
	case <-ctx.Done():
		t.Fatal("read-only token obtained a call-media websocket")
	}
}
