// Package validate checks WebSocket frames and NATS events against the JSON
// Schemas in packages/contracts/schemas before anything acts on them.
//
// A payload schema is registered by its file name: frames/<type>.v<version>.json
// for frames and events/<type>.v<version>.json for events. Adding a file adds
// the type; nothing else needs to change.
package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/iykeevans/konduite/packages/contracts"
)

// ErrUnknownType means the envelope was well formed but no payload schema
// exists for its type and version on that channel.
var ErrUnknownType = errors.New("unknown message type or version")

// Frame validates a WebSocket frame and its payload and returns the decoded
// frame. Data is left raw for the caller to decode into the payload type.
func Frame(raw []byte) (contracts.Frame, error) {
	var f contracts.Frame
	err := check(raw, func(r *registry) (*jsonschema.Schema, map[key]*jsonschema.Schema) {
		return r.frame, r.frames
	}, &f)
	return f, err
}

// Event validates a NATS event envelope and its payload and returns the
// decoded envelope. Data is left raw for the caller to decode into the payload
// type.
func Event(raw []byte) (contracts.Envelope, error) {
	var e contracts.Envelope
	err := check(raw, func(r *registry) (*jsonschema.Schema, map[key]*jsonschema.Schema) {
		return r.envelope, r.events
	}, &e)
	return e, err
}

type key struct {
	typ     string
	version int
}

type registry struct {
	frame, envelope *jsonschema.Schema
	frames, events  map[key]*jsonschema.Schema
}

var load = sync.OnceValues(compile)

func check(raw []byte, pick func(*registry) (*jsonschema.Schema, map[key]*jsonschema.Schema), out any) error {
	reg, err := load()
	if err != nil {
		return err
	}
	outer, payloads := pick(reg)

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("malformed json: %w", err)
	}
	if err := outer.Validate(inst); err != nil {
		return fmt.Errorf("invalid envelope: %w", err)
	}

	m, _ := inst.(map[string]any)
	typ, _ := m["type"].(string)
	num, _ := m["version"].(json.Number)
	version, err := strconv.Atoi(num.String())
	if err != nil {
		return fmt.Errorf("invalid envelope: version %q: %w", num, err)
	}

	payload, ok := payloads[key{typ, version}]
	if !ok {
		return fmt.Errorf("%w: %s v%d", ErrUnknownType, typ, version)
	}
	if err := payload.Validate(m["data"]); err != nil {
		return fmt.Errorf("invalid %s v%d payload: %w", typ, version, err)
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

var payloadName = regexp.MustCompile(`^(.+)\.v([1-9][0-9]*)\.json$`)

func compile() (*registry, error) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.DefaultDraft(jsonschema.Draft2020)

	var files []string
	err := fs.WalkDir(contracts.Schemas, "schemas", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".json" {
			return err
		}
		b, err := fs.ReadFile(contracts.Schemas, p)
		if err != nil {
			return err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if err := c.AddResource(url(p), doc); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load contract schemas: %w", err)
	}

	reg := &registry{frames: map[key]*jsonschema.Schema{}, events: map[key]*jsonschema.Schema{}}
	for _, p := range files {
		s, err := c.Compile(url(p))
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", p, err)
		}
		dir, name := path.Split(p)
		switch dir {
		case "schemas/":
			switch name {
			case "frame.json":
				reg.frame = s
			case "envelope.json":
				reg.envelope = s
			}
			continue
		case "schemas/frames/", "schemas/events/":
		default:
			return nil, fmt.Errorf("compile %s: unexpected schema directory", p)
		}
		parts := payloadName.FindStringSubmatch(name)
		if parts == nil {
			return nil, fmt.Errorf("compile %s: payload schemas are named <type>.v<version>.json", p)
		}
		version, _ := strconv.Atoi(parts[2])
		target := reg.events
		if dir == "schemas/frames/" {
			target = reg.frames
		}
		target[key{parts[1], version}] = s
	}
	if reg.frame == nil || reg.envelope == nil {
		return nil, errors.New("load contract schemas: frame.json or envelope.json missing")
	}
	return reg, nil
}

func url(p string) string {
	return "embed:///" + p
}
