package nats

import (
	"errors"
	"fmt"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

const subscribeFlushTimeout = 2 * time.Second

// Msg is one message from a Subscribe subscription. MsgID is the
// Nats-Msg-Id it was published with, empty if none.
type Msg struct {
	Subject string
	MsgID   string
	Data    []byte
}

// Subscribe starts a core NATS subscription on subject and calls fn for each
// message, one at a time, until the returned unsubscribe is called. fn must
// not block for long: later messages wait behind it.
//
// A core subscription sees messages published while it exists, including
// ones JetStream stores, and nothing from before. It suits push hints whose
// durable record lives elsewhere, such as a connected runtime's deliveries,
// which are replayed from Postgres after a reconnect. Worker consumers that
// must not miss an event use Consume.
//
// Subscribe returns once the server has registered the interest, so a
// message published after it returns is seen.
func (c *Client) Subscribe(subject string, fn func(Msg)) (unsubscribe func() error, err error) {
	sub, err := c.nc.Subscribe(subject, func(m *natsgo.Msg) {
		fn(Msg{Subject: m.Subject, MsgID: m.Header.Get(natsgo.MsgIdHdr), Data: m.Data})
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe %s: %w", subject, err)
	}
	if err := c.nc.FlushTimeout(subscribeFlushTimeout); err != nil {
		return nil, errors.Join(fmt.Errorf("subscribe %s: %w", subject, err), sub.Unsubscribe())
	}
	return sub.Unsubscribe, nil
}
