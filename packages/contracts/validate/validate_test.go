package validate_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/iykeevans/konduite/packages/contracts/validate"
)

const (
	frameID = "6f1c2b9e-3a4d-4e8f-9b1a-2c3d4e5f6a7b"
	eventID = "0b8e7c6d-5a4f-4e3d-8c2b-1a0f9e8d7c6b"
	wsID    = "1d2c3b4a-5e6f-4a8b-9c0d-1e2f3a4b5c6d"
	rtID    = "9a8b7c6d-5e4f-4a2b-8c0d-9e8f7a6b5c4d"
)

func frame(typ string, version int, data string) string {
	return `{"id":"` + frameID + `","type":"` + typ + `","version":` + strconv.Itoa(version) +
		`,"sent_at":"2026-11-19T10:00:00Z","data":` + data + `}`
}

func event(typ string, version int, data string) string {
	return `{"id":"` + eventID + `","type":"` + typ + `","version":` + strconv.Itoa(version) +
		`,"occurred_at":"2026-11-19T10:00:00Z","workspace_id":"` + wsID +
		`","correlation_id":"` + eventID + `","data":` + data + `}`
}

func TestFrame(t *testing.T) {
	hello := `{"runtime_version":"0.1.0","agents":[{"provider":"claude_code","name":"claude","capabilities":{"supports_resume":true}}]}`

	tests := []struct {
		name        string
		raw         string
		wantErr     bool
		wantUnknown bool
	}{
		{name: "hello", raw: frame("runtime.hello", 1, hello)},
		{name: "hello with no agents", raw: frame("runtime.hello", 1, `{"runtime_version":"0.1.0","agents":[]}`)},
		{name: "heartbeat", raw: frame("runtime.heartbeat", 1, `{}`)},
		{name: "event ack", raw: frame("runtime.event_ack", 1, `{"event_id":"`+eventID+`"}`)},
		{
			name: "optional correlation id",
			raw: `{"id":"` + frameID + `","type":"runtime.heartbeat","version":1,"sent_at":"2026-11-19T10:00:00Z",` +
				`"correlation_id":"` + eventID + `","data":{}}`,
		},

		{name: "not json", raw: `{"id":`, wantErr: true},
		{name: "trailing data", raw: frame("runtime.heartbeat", 1, `{}`) + `{}`, wantErr: true},
		{name: "array", raw: `[]`, wantErr: true},
		{name: "missing id", raw: `{"type":"runtime.heartbeat","version":1,"sent_at":"2026-11-19T10:00:00Z","data":{}}`, wantErr: true},
		{name: "id not a uuid", raw: strings.Replace(frame("runtime.heartbeat", 1, `{}`), frameID, "abc", 1), wantErr: true},
		{name: "bad sent_at", raw: strings.Replace(frame("runtime.heartbeat", 1, `{}`), "2026-11-19T10:00:00Z", "yesterday", 1), wantErr: true},
		{name: "version zero", raw: frame("runtime.heartbeat", 0, `{}`), wantErr: true},
		{name: "type not dotted", raw: frame("hello", 1, `{}`), wantErr: true},
		{name: "data not an object", raw: frame("runtime.heartbeat", 1, `"x"`), wantErr: true},
		{
			name:    "tenant field smuggled into frame",
			raw:     strings.Replace(frame("runtime.heartbeat", 1, `{}`), `"data"`, `"workspace_id":"`+wsID+`","data"`, 1),
			wantErr: true,
		},
		{name: "unknown type", raw: frame("runtime.teleport", 1, `{}`), wantErr: true, wantUnknown: true},
		{name: "unknown version", raw: frame("runtime.hello", 2, hello), wantErr: true, wantUnknown: true},
		{name: "event type sent as frame", raw: frame("runtime.connected", 1, `{"runtime_id":"`+rtID+`"}`), wantErr: true, wantUnknown: true},
		{name: "hello missing agents", raw: frame("runtime.hello", 1, `{"runtime_version":"0.1.0"}`), wantErr: true},
		{name: "hello bad provider", raw: frame("runtime.hello", 1, `{"runtime_version":"0.1.0","agents":[{"provider":"Claude Code","name":"c"}]}`), wantErr: true},
		{name: "hello unknown capability", raw: frame("runtime.hello", 1, `{"runtime_version":"0.1.0","agents":[{"provider":"echo","name":"e","capabilities":{"root":true}}]}`), wantErr: true},
		{name: "heartbeat extra field", raw: frame("runtime.heartbeat", 1, `{"x":1}`), wantErr: true},
		{name: "ack missing event id", raw: frame("runtime.event_ack", 1, `{}`), wantErr: true},
		{name: "ack event id not a uuid", raw: frame("runtime.event_ack", 1, `{"event_id":"42"}`), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := validate.Frame([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if got := errors.Is(err, validate.ErrUnknownType); got != tt.wantUnknown {
					t.Fatalf("errors.Is(err, ErrUnknownType) = %v, want %v (err: %v)", got, tt.wantUnknown, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if f.ID != frameID || f.Version < 1 || f.Type == "" || len(f.Data) == 0 {
				t.Fatalf("frame not decoded: %+v", f)
			}
		})
	}
}

func TestEvent(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantErr     bool
		wantUnknown bool
	}{
		{name: "connected", raw: event("runtime.connected", 1, `{"runtime_id":"`+rtID+`"}`)},
		{name: "disconnected", raw: event("runtime.disconnected", 1, `{"runtime_id":"`+rtID+`","reason":"heartbeat_timeout"}`)},
		{
			name: "with project and causation",
			raw: strings.Replace(event("runtime.connected", 1, `{"runtime_id":"`+rtID+`"}`), `"data"`,
				`"project_id":"`+wsID+`","causation_id":"`+frameID+`","data"`, 1),
		},

		{name: "missing workspace", raw: strings.Replace(event("runtime.connected", 1, `{"runtime_id":"`+rtID+`"}`), `"workspace_id":"`+wsID+`",`, "", 1), wantErr: true},
		{name: "missing correlation", raw: strings.Replace(event("runtime.connected", 1, `{"runtime_id":"`+rtID+`"}`), `"correlation_id":"`+eventID+`",`, "", 1), wantErr: true},
		{name: "frame posing as event", raw: frame("runtime.connected", 1, `{"runtime_id":"`+rtID+`"}`), wantErr: true},
		{name: "unknown type", raw: event("message.teleported", 1, `{}`), wantErr: true, wantUnknown: true},
		{name: "unknown version", raw: event("runtime.connected", 3, `{"runtime_id":"`+rtID+`"}`), wantErr: true, wantUnknown: true},
		{name: "frame type published as event", raw: event("runtime.heartbeat", 1, `{}`), wantErr: true, wantUnknown: true},
		{name: "bad reason", raw: event("runtime.disconnected", 1, `{"runtime_id":"`+rtID+`","reason":"bored"}`), wantErr: true},
		{name: "runtime id not a uuid", raw: event("runtime.connected", 1, `{"runtime_id":"rt-1"}`), wantErr: true},
		{name: "credential smuggled into payload", raw: event("runtime.connected", 1, `{"runtime_id":"`+rtID+`","token":"x"}`), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := validate.Event([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if got := errors.Is(err, validate.ErrUnknownType); got != tt.wantUnknown {
					t.Fatalf("errors.Is(err, ErrUnknownType) = %v, want %v (err: %v)", got, tt.wantUnknown, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if e.ID != eventID || e.WorkspaceID != wsID || len(e.Data) == 0 {
				t.Fatalf("event not decoded: %+v", e)
			}
		})
	}
}
