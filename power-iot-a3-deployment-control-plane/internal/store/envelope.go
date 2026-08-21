package store

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

var envelopeEncoding = base64.RawURLEncoding

// EncodeEnvelope is the only wire representation of the one-time secret.
func EncodeEnvelope(authID uuid.UUID, epoch int64, nonce, secret []byte) string {
	enc := func(v []byte) string { return envelopeEncoding.EncodeToString(v) }
	return "d1lba.v1." + enc([]byte(authID.String())) + "." + enc([]byte(strconv.FormatInt(epoch, 10))) + "." + enc(nonce) + "." + enc(secret)
}

type parsedEnvelope struct {
	authID uuid.UUID
	epoch  int64
	nonce  []byte
	secret []byte
}

func parseEnvelope(raw string) (parsedEnvelope, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 6 || parts[0] != "d1lba" || parts[1] != "v1" {
		return parsedEnvelope{}, errors.New("invalid envelope")
	}
	decode := func(s string) ([]byte, error) {
		if s == "" || strings.ContainsAny(s, "=+/ ") {
			return nil, errors.New("invalid envelope encoding")
		}
		b, err := envelopeEncoding.DecodeString(s)
		if err != nil || len(b) == 0 || envelopeEncoding.EncodeToString(b) != s {
			return nil, errors.New("invalid envelope encoding")
		}
		return b, nil
	}
	a, err := decode(parts[2])
	if err != nil {
		return parsedEnvelope{}, err
	}
	e, err := decode(parts[3])
	if err != nil {
		return parsedEnvelope{}, err
	}
	n, err := decode(parts[4])
	if err != nil || len(n) != 16 {
		return parsedEnvelope{}, errors.New("invalid envelope nonce")
	}
	secret, err := decode(parts[5])
	if err != nil || len(secret) != 32 {
		return parsedEnvelope{}, errors.New("invalid envelope secret")
	}
	id, err := uuid.Parse(string(a))
	if err != nil {
		return parsedEnvelope{}, errors.New("invalid envelope authorization")
	}
	epoch, err := strconv.ParseInt(string(e), 10, 64)
	if err != nil || epoch <= 0 || strconv.FormatInt(epoch, 10) != string(e) {
		return parsedEnvelope{}, errors.New("invalid envelope epoch")
	}
	return parsedEnvelope{authID: id, epoch: epoch, nonce: n, secret: secret}, nil
}
