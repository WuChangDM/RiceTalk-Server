package bots

import "testing"

func TestServiceStopIsIdempotent(t *testing.T) {
	service := &Service{stopCh: make(chan struct{})}

	service.Stop()
	service.Stop()

	select {
	case <-service.stopCh:
	default:
		t.Fatal("expected Stop to close the cleanup signal")
	}
}
