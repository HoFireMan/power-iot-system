package reconciliation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/google/uuid"
)

// MappingArtifact is the only supported explicit authority input. It is bound
// to one source snapshot and is never allowed to repair malformed relational
// facts. The three categories are deliberately closed: shop, device, and
// admin operation/provenance.
type MappingArtifact struct {
	SchemaVersion     string         `json:"schema"`
	Version           int            `json:"version"`
	SourceFactsDigest string         `json:"source_facts_digest"`
	Mappings          []MappingEntry `json:"mappings"`
}

type MappingCategory string

const (
	MappingShop            MappingCategory = "shop_id->client_id"
	MappingDevice          MappingCategory = "device_id->client_id"
	MappingAdminProvenance MappingCategory = "admin_operation/provenance->client_id"
)

type MappingEntry struct {
	// DeviceID is retained as the v5 device shorthand. Category is optional
	// only for this legacy spelling; canonical output always writes category.
	Category    MappingCategory `json:"category,omitempty"`
	ShopID      uint            `json:"shop_id,omitempty"`
	DeviceID    uint            `json:"device_id,omitempty"`
	OperationID uuid.UUID       `json:"operation_id,omitempty"`
	ClientID    uint            `json:"client_id"`
	// ExpectedCurrentClientID is a compare-and-swap expectation, not authority.
	ExpectedCurrentClientID *uint `json:"expected_current_client_id,omitempty"`
}

func (a MappingArtifact) validate() error {
	if a.SchemaVersion != MappingSchema {
		return fmt.Errorf("mapping schema must be %q", MappingSchema)
	}
	if a.Version != 5 {
		return errors.New("mapping version must be 5")
	}
	if a.SourceFactsDigest == "" {
		return errors.New("mapping source_facts_digest is required")
	}
	if !isDigestHex(a.SourceFactsDigest) {
		return errors.New("mapping source_facts_digest must be a SHA-256 hex digest")
	}
	seen := map[string]bool{}
	for i, m := range a.Mappings {
		category, key, err := mappingKey(m)
		if err != nil {
			return fmt.Errorf("mapping %d: %w", i, err)
		}
		if m.ClientID == 0 {
			return fmt.Errorf("mapping %s client_id is required", key)
		}
		if m.ExpectedCurrentClientID != nil && *m.ExpectedCurrentClientID == 0 {
			return fmt.Errorf("mapping %s expected_current_client_id is invalid", key)
		}
		compound := string(category) + "\x00" + key
		if seen[compound] {
			return fmt.Errorf("duplicate mapping for %s %s", category, key)
		}
		seen[compound] = true
	}
	return nil
}

func isDigestHex(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func mappingKey(m MappingEntry) (MappingCategory, string, error) {
	category := m.Category
	if category == "" {
		if m.DeviceID != 0 && m.ShopID == 0 && m.OperationID == uuid.Nil {
			category = MappingDevice
		} else {
			return "", "", errors.New("category is required")
		}
	}
	switch category {
	case MappingShop:
		if m.ShopID == 0 || m.DeviceID != 0 || m.OperationID != uuid.Nil {
			return "", "", errors.New("shop mapping requires only shop_id")
		}
		return category, fmt.Sprint(m.ShopID), nil
	case MappingDevice:
		if m.DeviceID == 0 || m.ShopID != 0 || m.OperationID != uuid.Nil {
			return "", "", errors.New("device mapping requires only device_id")
		}
		return category, fmt.Sprint(m.DeviceID), nil
	case MappingAdminProvenance:
		if m.OperationID == uuid.Nil || m.ShopID != 0 || m.DeviceID != 0 {
			return "", "", errors.New("admin provenance mapping requires only operation_id")
		}
		return category, m.OperationID.String(), nil
	default:
		return "", "", fmt.Errorf("unknown mapping category %q", category)
	}
}

// ParseMappingArtifact strictly rejects unknown fields and duplicate JSON
// object keys. Duplicate rejection is performed before encoding/json decoding
// because encoding/json otherwise silently accepts the last key.
func ParseMappingArtifact(data []byte) (MappingArtifact, error) {
	var a MappingArtifact
	if err := rejectDuplicateKeys(data); err != nil {
		return a, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return a, fmt.Errorf("mapping artifact: %w", err)
	}
	var extra interface{}
	if err := dec.Decode(&extra); err != io.EOF {
		return a, errors.New("mapping artifact must contain one JSON value")
	}
	if err := a.validate(); err != nil {
		return a, err
	}
	return a, nil
}

// Canonical returns sorted compact JSON with a fixed field order and digest.
func (a MappingArtifact) Canonical() ([]byte, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	entries := append([]MappingEntry(nil), a.Mappings...)
	for i := range entries {
		if entries[i].Category == "" {
			entries[i].Category = MappingDevice
		}
	}
	sortMappings(entries)
	body, err := json.Marshal(struct {
		Schema            string         `json:"schema"`
		Version           int            `json:"version"`
		SourceFactsDigest string         `json:"source_facts_digest"`
		Mappings          []MappingEntry `json:"mappings"`
	}{a.SchemaVersion, a.Version, a.SourceFactsDigest, entries})
	if err != nil {
		return nil, err
	}
	return body, nil
}
func (a MappingArtifact) Digest() ([]byte, error) {
	b, err := a.Canonical()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}
func (a MappingArtifact) DigestHex() (string, error) {
	d, err := a.Digest()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(d), nil
}
func (a MappingArtifact) CanonicalDigest() ([]byte, []byte, error) {
	b, err := a.Canonical()
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(b)
	return b, sum[:], nil
}
func sortMappings(v []MappingEntry) {
	sort.Slice(v, func(i, j int) bool {
		ci, ki, _ := mappingKey(v[i])
		cj, kj, _ := mappingKey(v[j])
		if ci != cj {
			return ci < cj
		}
		return ki < kj
	})
}

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var v interface{}
	if err := decodeUnique(dec, &v, "$"); err != nil {
		return err
	}
	return nil
}
func decodeUnique(dec *json.Decoder, out *interface{}, path string) error {
	t, err := dec.Token()
	if err != nil {
		return fmt.Errorf("invalid mapping artifact: %w", err)
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyTok, e := dec.Token()
				if e != nil {
					return e
				}
				key, ok := keyTok.(string)
				if !ok {
					return errors.New("mapping object key is not a string")
				}
				if seen[key] {
					return fmt.Errorf("duplicate mapping field %q", key)
				}
				seen[key] = true
				var child interface{}
				if e = decodeUnique(dec, &child, path+"."+key); e != nil {
					return e
				}
			}
			end, e := dec.Token()
			if e != nil || end != json.Delim('}') {
				return errors.New("unterminated mapping object")
			}
		case '[':
			i := 0
			for dec.More() {
				var child interface{}
				if e := decodeUnique(dec, &child, path+"["+strconv.Itoa(i)+"]"); e != nil {
					return e
				}
				i++
			}
			end, e := dec.Token()
			if e != nil || end != json.Delim(']') {
				return errors.New("unterminated mapping array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	return nil
}

// MappingArtifactJSON is a convenience for callers that already hold an
// artifact and still want strict canonical bytes.
func MappingArtifactJSON(data []byte) ([]byte, []byte, error) {
	a, err := ParseMappingArtifact(data)
	if err != nil {
		return nil, nil, err
	}
	return a.CanonicalDigest()
}
