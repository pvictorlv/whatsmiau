package whatsmiau

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

// rawFallbackSkip lists protobuf fields that are never useful to a webhook
// consumer: they are pure Signal/crypto plumbing, they are large, and whatsmeow
// has already acted on them before the event reaches us.
var rawFallbackSkip = map[string]struct{}{
	"senderKeyDistributionMessage":               {},
	"fastRatchetKeySenderKeyDistributionMessage": {},
}

// wookRawFieldOrder is the JSON key order of WookMessageRaw. Consumers detect
// the message type by taking the first key that looks like one, so explicitly
// mapped fields must always be emitted before fallback-only ones.
var wookRawFieldOrder = buildWookRawFieldOrder()

func buildWookRawFieldOrder() []string {
	t := reflect.TypeOf(WookMessageRaw{})
	order := make([]string, 0, t.NumField())

	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		order = append(order, strings.Split(tag, ",")[0])
	}

	return order
}

// normalizeProtoKey converts a protobuf JSON name to the lowerCamelCase spelling
// Baileys and Evolution use, which is also what the hand-mapped structs above
// emit. whatsmeow's generated names keep Go-style initialisms, so without this
// the payload would mix "remoteJID" with "remoteJid" and consumers reading
// key.id would find nothing.
func normalizeProtoKey(key string) string {
	if key == "" {
		return key
	}

	runes := []rune(key)
	var out []rune

	for i := 0; i < len(runes); i++ {
		if !unicode.IsUpper(runes[i]) {
			out = append(out, runes[i])
			continue
		}

		// Consume a full run of uppercase letters and trailing digits, e.g.
		// "JID", "URL", "SHA256".
		run := i
		for run < len(runes) && (unicode.IsUpper(runes[run]) || unicode.IsDigit(runes[run])) {
			run++
		}

		end := run
		// A run followed by lowercase letters means its last capital starts the
		// next word: "JPEGThumbnail" is JPEG + Thumbnail.
		if run < len(runes) && run-i > 1 {
			end = run - 1
		}

		out = append(out, runes[i])
		for j := i + 1; j < end; j++ {
			out = append(out, unicode.ToLower(runes[j]))
		}
		i = end - 1
	}

	out[0] = unicode.ToLower(out[0])

	return string(out)
}

// normalizeProtoKeys walks a decoded protojson value and rewrites every object
// key with normalizeProtoKey.
func normalizeProtoKeys(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, nested := range typed {
			normalized[normalizeProtoKey(key)] = normalizeProtoKeys(nested)
		}
		return normalized
	case []any:
		for i, nested := range typed {
			typed[i] = normalizeProtoKeys(nested)
		}
		return typed
	default:
		return value
	}
}

// buildRawFallback marshals the whole waE2E message with protojson so that any
// message type parseWAMessage does not map explicitly still reaches the
// consumer instead of being silently dropped as "unknown".
func buildRawFallback(m *waE2E.Message) map[string]json.RawMessage {
	if m == nil {
		return nil
	}

	encoded, err := protojson.Marshal(m)
	if err != nil {
		zap.L().Error("failed to marshal raw message fallback", zap.Error(err))
		return nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		zap.L().Error("failed to decode raw message fallback", zap.Error(err))
		return nil
	}

	fallback := make(map[string]json.RawMessage, len(decoded))
	for key, value := range decoded {
		normalizedKey := normalizeProtoKey(key)
		if _, skip := rawFallbackSkip[normalizedKey]; skip {
			continue
		}

		reEncoded, err := json.Marshal(normalizeProtoKeys(value))
		if err != nil {
			zap.L().Error("failed to re-encode raw message field",
				zap.String("field", normalizedKey),
				zap.Error(err))
			continue
		}
		fallback[normalizedKey] = reEncoded
	}

	return fallback
}

// firstContentKey returns the protobuf field name that identifies the message
// type, mirroring how Baileys consumers derive it (first key that is either
// "conversation" or contains "Message"). Used to give unmapped types a real name
// instead of "unknown".
func firstContentKey(fallback map[string]json.RawMessage) string {
	keys := make([]string, 0, len(fallback))
	for key := range fallback {
		if key == "conversation" || strings.Contains(key, "Message") {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}

	sort.Strings(keys)
	return keys[0]
}

// deepMerge overlays override on top of base. Both are merged recursively when
// they are objects; anywhere else override wins. Used so a partial hand-mapping
// (which may carry enriched values) keeps its fields while still gaining the
// ones it never modelled.
func deepMerge(base, override json.RawMessage) json.RawMessage {
	var baseObj, overrideObj map[string]json.RawMessage

	if err := json.Unmarshal(base, &baseObj); err != nil {
		return override
	}
	if err := json.Unmarshal(override, &overrideObj); err != nil {
		return override
	}

	for key, value := range overrideObj {
		if existing, present := baseObj[key]; present {
			baseObj[key] = deepMerge(existing, value)
			continue
		}
		baseObj[key] = value
	}

	merged, err := json.Marshal(baseObj)
	if err != nil {
		return override
	}
	return merged
}

// MarshalJSON emits the explicitly mapped fields first, in struct order, each
// deep-merged with its protobuf counterpart, then every remaining protobuf
// field. Nothing the device sent is dropped, and the mapped spelling always
// wins on conflicts.
func (r WookMessageRaw) MarshalJSON() ([]byte, error) {
	// plain avoids recursing into this method while marshalling the struct.
	type plain WookMessageRaw

	encoded, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}

	if len(r.Fallback) == 0 {
		return encoded, nil
	}

	mapped := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &mapped); err != nil {
		return nil, err
	}

	extra := make([]string, 0, len(r.Fallback))
	for key := range r.Fallback {
		if _, alreadyMapped := mapped[key]; !alreadyMapped {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)

	var buf bytes.Buffer
	buf.WriteByte('{')

	write := func(key string, value json.RawMessage) error {
		if buf.Len() > 1 {
			buf.WriteByte(',')
		}

		encodedKey, err := json.Marshal(key)
		if err != nil {
			return err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		buf.Write(value)
		return nil
	}

	for _, key := range wookRawFieldOrder {
		value, present := mapped[key]
		if !present {
			continue
		}

		if counterpart, ok := r.Fallback[key]; ok {
			value = deepMerge(counterpart, value)
		}
		if err := write(key, value); err != nil {
			return nil, err
		}
	}

	for _, key := range extra {
		if err := write(key, r.Fallback[key]); err != nil {
			return nil, err
		}
	}

	buf.WriteByte('}')

	return buf.Bytes(), nil
}
