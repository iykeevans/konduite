//go:build integration

package nats_test

import (
	"testing"
	"time"

	"github.com/iykeevans/konduite/backend/internal/platform/nats"
)

func TestSubscribeSeesJetStreamPublishesUntilUnsubscribed(t *testing.T) {
	srv := ensured(t)
	got := make(chan nats.Msg, 4)
	unsubscribe, err := srv.Client.Subscribe("agenthub.rt.r1.>", func(m nats.Msg) { got <- m })
	if err != nil {
		t.Fatal(err)
	}

	if err := srv.Client.Publish(t.Context(), "agenthub.rt.r1.deliver", "evt-1", []byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := srv.Client.Publish(t.Context(), "agenthub.rt.r2.deliver", "evt-2", []byte(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.Subject != "agenthub.rt.r1.deliver" || m.MsgID != "evt-1" || string(m.Data) != `{"n":1}` {
			t.Errorf("msg = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message on the subscribed subject")
	}

	if err := unsubscribe(); err != nil {
		t.Fatal(err)
	}
	later := make(chan nats.Msg, 1)
	stop, err := srv.Client.Subscribe("agenthub.rt.r1.>", func(m nats.Msg) { later <- m })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop() })
	if err := srv.Client.Publish(t.Context(), "agenthub.rt.r1.deliver", "evt-3", []byte(`{"n":3}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-later:
	case <-time.After(5 * time.Second):
		t.Fatal("the second subscription saw nothing")
	}
	select {
	case m := <-got:
		t.Errorf("got %+v after unsubscribing", m)
	default:
	}
}
