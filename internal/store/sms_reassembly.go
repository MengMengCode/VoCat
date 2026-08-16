package store

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
)

// ConcatMessageIDPrefix marks the stable message id that ingest points assign to
// every segment of one concatenated (long) SMS. Unlike the per-segment modem/IMS
// ids (which embed a storage slot, PDU hash, or RP reference), this id is shared
// by all segments of the message, so SaveSMSMessage folds them into a single row.
const ConcatMessageIDPrefix = "concat:"

// isConcatSMSMessageID reports whether a message id addresses a whole
// concatenated SMS rather than one physical segment.
func isConcatSMSMessageID(messageID string) bool {
	return strings.HasPrefix(messageID, ConcatMessageIDPrefix)
}

// ConcatSMSReadyToNotify reports whether an inbound SMS row is ready to surface
// to a notification consumer. A plain message is always ready; a concatenated
// (long) SMS row is ready only once every segment has merged (concat_complete).
// Until then consumers should hold the notification but still advance their
// cursor — the completed message re-enters as a fresh durable id.
func ConcatSMSReadyToNotify(messageID string, extra json.RawMessage) bool {
	if !isConcatSMSMessageID(messageID) {
		return true
	}
	document, err := decodeJSONObject(extra)
	if err != nil {
		return false
	}
	complete, _ := document["concat_complete"].(bool)
	return complete
}

// StableConcatMessageID builds the message id shared by every segment of one
// concatenated SMS. The UDH concat reference is only unique per sender, so the
// hardware identity and peer scope it; total is folded in to further separate the
// rare reference reuse between two different long messages from the same peer.
// The hardware identity matches the row lookup in saveSMSMessage, so a segment
// always finds the row its siblings started.
func StableConcatMessageID(source, modemIMEI, deviceID, peer string, reference, total int) string {
	return ConcatMessageIDPrefix + source + ":" + smsHardwareKey(modemIMEI, deviceID) + ":" + peer + ":" +
		strconv.Itoa(reference) + ":" + strconv.Itoa(total)
}

// mergeConcatSegment folds one incoming segment into the progressively merged
// body of a concatenated SMS. existingExtra is the stored row's Extra (empty for
// the first segment); segmentBody/segmentExtra are the incoming segment's text
// and Extra, the latter carrying "concat" ({reference,total,sequence}).
//
// Each segment's text is kept under "concat_parts" keyed by its UDH sequence and
// the body is rebuilt by joining the parts in ascending sequence order with no
// separator — exactly how a phone reassembles a long message, and correct for any
// arrival order. The merge is idempotent: redelivering an already-folded sequence
// reports changed=false so callers can skip the write and avoid id churn.
// "concat_complete" flips true once Total segments are present.
func mergeConcatSegment(
	existingExtra json.RawMessage,
	segmentBody string,
	segmentExtra json.RawMessage,
) (body string, extra json.RawMessage, changed bool, err error) {
	segment, err := decodeJSONObject(segmentExtra)
	if err != nil {
		return "", nil, false, fmt.Errorf("decode segment extra: %w", err)
	}
	concat, _ := segment["concat"].(map[string]any)
	sequence := numberAsInt(concat["sequence"])
	total := numberAsInt(concat["total"])
	if sequence < 1 {
		// No usable UDH sequence: keep the incoming segment as the whole body.
		return segmentBody, json.RawMessage(segmentExtra), true, nil
	}

	// Seed the per-segment texts from the previously stored parts so an
	// out-of-order arrival always rebuilds in sequence order.
	parts := map[int]string{}
	if len(existingExtra) > 0 {
		if existing, derr := decodeJSONObject(existingExtra); derr == nil {
			if stored, ok := existing["concat_parts"].(map[string]any); ok {
				for key, value := range stored {
					n, aerr := strconv.Atoi(key)
					if aerr != nil || n < 1 {
						continue
					}
					if text, ok := value.(string); ok {
						parts[n] = text
					}
				}
			}
		}
	}
	// Some IMS stacks hand us a cumulative segment: sequence 2 contains the
	// already-decoded text of sequence 1 followed by its own payload. Keep a
	// snapshot so normalizing that representation remains idempotent on a later
	// redelivery of the same segment.
	previousParts := maps.Clone(parts)
	normalizeCumulativeConcatParts(previousParts)
	parts[sequence] = segmentBody
	normalizeCumulativeConcatParts(parts)
	changed = !maps.Equal(previousParts, parts)

	sequences := make([]int, 0, len(parts))
	for n := range parts {
		sequences = append(sequences, n)
	}
	sort.Ints(sequences)

	var joined strings.Builder
	stored := make(map[string]string, len(parts))
	for _, n := range sequences {
		joined.WriteString(parts[n])
		stored[strconv.Itoa(n)] = parts[n]
	}
	complete := total > 0 && len(parts) >= total

	merged := map[string]any{
		"concat":          concat,
		"concat_parts":    stored,
		"concat_received": len(parts),
		"concat_complete": complete,
	}
	// Preserve non-concat metadata from the latest segment for context.
	for _, key := range []string{"encoding", "storage", "transport", "source"} {
		if value, ok := segment[key]; ok {
			merged[key] = value
		}
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return "", nil, false, fmt.Errorf("encode merged concat extra: %w", err)
	}
	return joined.String(), json.RawMessage(encoded), changed, nil
}

// normalizeCumulativeConcatParts converts cumulative IMS segment bodies back
// into ordinary per-segment bodies. It only removes an exact, non-empty prefix
// assembled from every preceding sequence starting at 1, and only when the
// current value also contains additional text. That deliberately leaves equal
// repeated segments and incomplete/out-of-order prefixes untouched.
func normalizeCumulativeConcatParts(parts map[int]string) {
	var prefix strings.Builder
	for sequence := 1; ; sequence++ {
		text, ok := parts[sequence]
		if !ok {
			return
		}
		assembled := prefix.String()
		if assembled != "" && len(text) > len(assembled) && strings.HasPrefix(text, assembled) {
			text = strings.TrimPrefix(text, assembled)
			parts[sequence] = text
		}
		prefix.WriteString(text)
	}
}
