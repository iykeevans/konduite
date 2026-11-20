package outbox

import (
	"testing"
	"time"
)

func TestBackoffDoublesFromMinAndCapsAtMax(t *testing.T) {
	lo, hi := 500*time.Millisecond, 30*time.Second
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{1, 500 * time.Millisecond},
		{2, time.Second},
		{3, 2 * time.Second},
		{7, 30 * time.Second},
		{8, 30 * time.Second},
		{200, 30 * time.Second},
	}
	for _, tt := range tests {
		if got := backoff(lo, hi, tt.failures); got != tt.want {
			t.Errorf("backoff(%d failures) = %v, want %v", tt.failures, got, tt.want)
		}
	}
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.BatchSize != 100 || c.PollInterval != 5*time.Second || c.MinBackoff != 500*time.Millisecond ||
		c.MaxBackoff != 30*time.Second || c.PublishTimeout != 5*time.Second || c.Logger == nil {
		t.Errorf("defaults = %+v", c)
	}
	custom := Config{BatchSize: 7, PollInterval: time.Hour}.withDefaults()
	if custom.BatchSize != 7 || custom.PollInterval != time.Hour {
		t.Errorf("explicit values overwritten: %+v", custom)
	}
}
