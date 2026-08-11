package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ConcatMessageIDPrefix marks the stable message id that ingest points assign to
// every segment of one concatenated (long) SMS. Unlike the per-segment modem/IMS
// ids (which embed a storage slot, PDU hash, or RP reference), this id is shared
// by all segments of the message, so SaveSMSMessage folds them into a single row.
const ConcatMessageIDPrefix = "concat:"

const concatReassemblyWindow = 5 * time.Minute

type concatSegmentDescriptor struct {
	reference    int
	total        int
	sequence     int
	fingerprint  string
	occurrenceID string
}

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
// hardware identity, subscriber scope, and peer scope it; total is folded in to
// distinguish groups with different segment counts. saveSMSMessage adds an
// occurrence suffix when the same scoped reference is reused for a later long
// message. Including the subscriber scope in the durable message id prevents
// segments received before and after a SIM switch from sharing one assembly.
// The hardware identity matches the row lookup in saveSMSMessage, so a segment
// always finds the row its siblings started.
func StableConcatMessageID(source, modemIMEI, deviceID, subscriberScope, peer string, reference, total int) string {
	return ConcatMessageIDPrefix + source + ":" + smsHardwareKey(modemIMEI, deviceID) + ":" + subscriberScope + ":" + peer + ":" +
		strconv.Itoa(reference) + ":" + strconv.Itoa(total)
}

// legacyConcatMessageID reproduces the durable concat id used before subscriber
// scoping was introduced. It is retained only for locating and upgrading rows
// already written with that historical format.
func legacyConcatMessageID(source, modemIMEI, deviceID, peer string, reference, total int) string {
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
	concatValue, hasConcat := segment["concat"]
	if !hasConcat {
		return segmentBody, json.RawMessage(segmentExtra), true, nil
	}
	concat, ok := concatValue.(map[string]any)
	if !ok {
		return "", nil, false, fmt.Errorf("concat metadata must be an object")
	}
	descriptor, err := describeConcatSegment(segment, segmentBody)
	if err != nil {
		return "", nil, false, err
	}

	// Seed the per-segment texts from the previously stored parts so an
	// out-of-order arrival always rebuilds in sequence order.
	parts := map[int]string{}
	fingerprints := map[int]string{}
	occurrenceIDs := map[int]string{}
	if len(existingExtra) > 0 {
		existing, derr := decodeJSONObject(existingExtra)
		if derr != nil {
			return "", nil, false, fmt.Errorf("decode existing concat extra: %w", derr)
		}
		if existingConcat, ok := existing["concat"].(map[string]any); ok {
			if existingReference, existingTotal := numberAsInt(existingConcat["reference"]), numberAsInt(existingConcat["total"]); existingReference != descriptor.reference || existingTotal != descriptor.total {
				return "", nil, false, fmt.Errorf("concat metadata changed: reference/total %d/%d to %d/%d", existingReference, existingTotal, descriptor.reference, descriptor.total)
			}
		}
		if stored, ok := existing["concat_parts"].(map[string]any); ok {
			for key, value := range stored {
				n, aerr := strconv.Atoi(key)
				if aerr != nil || n < 1 || n > descriptor.total {
					return "", nil, false, fmt.Errorf("invalid stored concat sequence %q for total %d", key, descriptor.total)
				}
				if text, ok := value.(string); ok {
					parts[n] = text
				}
			}
		}
		if stored, ok := existing["concat_fingerprints"].(map[string]any); ok {
			for key, value := range stored {
				n, aerr := strconv.Atoi(key)
				fingerprint, valid := value.(string)
				if aerr != nil || n < 1 || n > descriptor.total || !valid || strings.TrimSpace(fingerprint) == "" {
					return "", nil, false, fmt.Errorf("invalid stored concat fingerprint sequence %q", key)
				}
				fingerprints[n] = fingerprint
			}
		}
		if stored, ok := existing["concat_occurrence_ids"].(map[string]any); ok {
			for key, value := range stored {
				n, aerr := strconv.Atoi(key)
				occurrenceID, valid := value.(string)
				occurrenceID = strings.TrimSpace(occurrenceID)
				if aerr != nil || n < 1 || n > descriptor.total || !valid || occurrenceID == "" {
					return "", nil, false, fmt.Errorf("invalid stored concat occurrence id sequence %q", key)
				}
				occurrenceIDs[n] = occurrenceID
			}
		}
	}
	prior, alreadyHad := parts[descriptor.sequence]
	priorOccurrenceID := occurrenceIDs[descriptor.sequence]
	if alreadyHad {
		if priorOccurrenceID != "" && descriptor.occurrenceID != "" {
			if priorOccurrenceID != descriptor.occurrenceID {
				return "", nil, false, fmt.Errorf("concat sequence %d belongs to a different occurrence", descriptor.sequence)
			}
			if priorFingerprint := strings.TrimSpace(fingerprints[descriptor.sequence]); priorFingerprint != "" && priorFingerprint != descriptor.fingerprint {
				return "", nil, false, fmt.Errorf("concat sequence %d occurrence fingerprint changed", descriptor.sequence)
			}
		} else {
			// Rows written before exact physical occurrence IDs rely on their
			// raw-PDU fingerprint (or, for older rows, body text) as a fallback.
			priorFingerprint := fingerprints[descriptor.sequence]
			if (priorFingerprint != "" && priorFingerprint != descriptor.fingerprint) ||
				(priorFingerprint == "" && prior != segmentBody) {
				return "", nil, false, fmt.Errorf("concat sequence %d belongs to a different occurrence", descriptor.sequence)
			}
		}
	}
	changed = !alreadyHad || prior != segmentBody ||
		(priorOccurrenceID == "" && descriptor.occurrenceID != "")
	parts[descriptor.sequence] = segmentBody
	fingerprints[descriptor.sequence] = descriptor.fingerprint
	if descriptor.occurrenceID != "" {
		occurrenceIDs[descriptor.sequence] = descriptor.occurrenceID
	}

	sequences := make([]int, 0, len(parts))
	for n := range parts {
		sequences = append(sequences, n)
	}
	sort.Ints(sequences)

	var joined strings.Builder
	stored := make(map[string]string, len(parts))
	storedFingerprints := make(map[string]string, len(fingerprints))
	storedOccurrenceIDs := make(map[string]string, len(occurrenceIDs))
	for _, n := range sequences {
		joined.WriteString(parts[n])
		stored[strconv.Itoa(n)] = parts[n]
		storedFingerprints[strconv.Itoa(n)] = fingerprints[n]
		if occurrenceID := occurrenceIDs[n]; occurrenceID != "" {
			storedOccurrenceIDs[strconv.Itoa(n)] = occurrenceID
		}
	}
	complete := descriptor.total > 0 && len(parts) >= descriptor.total

	merged := map[string]any{
		"concat":                concat,
		"concat_parts":          stored,
		"concat_fingerprints":   storedFingerprints,
		"concat_occurrence_ids": storedOccurrenceIDs,
		"concat_received":       len(parts),
		"concat_complete":       complete,
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

func describeConcatSegment(segment map[string]any, body string) (concatSegmentDescriptor, error) {
	concat, ok := segment["concat"].(map[string]any)
	if !ok {
		return concatSegmentDescriptor{}, errors.New("concat metadata must be an object")
	}
	descriptor := concatSegmentDescriptor{
		reference: numberAsInt(concat["reference"]),
		total:     numberAsInt(concat["total"]),
		sequence:  numberAsInt(concat["sequence"]),
	}
	if descriptor.reference < 0 || descriptor.total <= 1 ||
		descriptor.sequence < 1 || descriptor.sequence > descriptor.total {
		return concatSegmentDescriptor{}, fmt.Errorf(
			"invalid concat metadata: reference=%d total=%d sequence=%d",
			descriptor.reference,
			descriptor.total,
			descriptor.sequence,
		)
	}
	descriptor.fingerprint, _ = segment["segment_fingerprint"].(string)
	descriptor.fingerprint = strings.TrimSpace(descriptor.fingerprint)
	descriptor.occurrenceID, _ = segment["segment_occurrence_id"].(string)
	descriptor.occurrenceID = strings.TrimSpace(descriptor.occurrenceID)
	if descriptor.fingerprint == "" {
		// Legacy ingest did not persist raw-PDU fingerprints. Text is sufficient
		// for idempotent rescans of those rows; current ingest always supplies a
		// transport fingerprint, which separates equal text in later occurrences.
		descriptor.fingerprint = "legacy:" + body
	}
	return descriptor, nil
}

func inspectConcatOccurrence(
	extra json.RawMessage,
	descriptor concatSegmentDescriptor,
	body string,
) (complete, hasSequence, sameSegment bool, err error) {
	document, err := decodeJSONObject(extra)
	if err != nil {
		return false, false, false, err
	}
	concat, ok := document["concat"].(map[string]any)
	if !ok || numberAsInt(concat["reference"]) != descriptor.reference ||
		numberAsInt(concat["total"]) != descriptor.total {
		return false, false, false, errors.New("stored concat occurrence metadata does not match its id")
	}
	complete, _ = document["concat_complete"].(bool)
	parts, _ := document["concat_parts"].(map[string]any)
	sequenceKey := strconv.Itoa(descriptor.sequence)
	storedBody, hasSequence := parts[sequenceKey].(string)
	if !hasSequence {
		return complete, false, false, nil
	}
	fingerprints, _ := document["concat_fingerprints"].(map[string]any)
	storedFingerprint, _ := fingerprints[sequenceKey].(string)
	occurrenceIDs, _ := document["concat_occurrence_ids"].(map[string]any)
	storedOccurrenceID, _ := occurrenceIDs[sequenceKey].(string)
	storedOccurrenceID = strings.TrimSpace(storedOccurrenceID)
	if storedOccurrenceID != "" && descriptor.occurrenceID != "" {
		if storedOccurrenceID != descriptor.occurrenceID {
			return complete, true, false, nil
		}
		if storedFingerprint = strings.TrimSpace(storedFingerprint); storedFingerprint != "" && storedFingerprint != descriptor.fingerprint {
			return false, false, false, fmt.Errorf("concat sequence %d occurrence fingerprint changed", descriptor.sequence)
		}
		return complete, true, true, nil
	}
	if strings.TrimSpace(storedFingerprint) != "" {
		// Legacy rows have no exact occurrence id. A matching fingerprint keeps
		// their historical idempotence behavior but deliberately does not force a
		// metadata-only row replacement just to backfill the new identifier.
		return complete, true, storedFingerprint == descriptor.fingerprint, nil
	}
	return complete, true, storedBody == body, nil
}

func concatTimesNear(first, second time.Time) bool {
	if first.IsZero() || second.IsZero() {
		return false
	}
	delta := first.Sub(second)
	if delta < 0 {
		delta = -delta
	}
	return delta <= concatReassemblyWindow
}
