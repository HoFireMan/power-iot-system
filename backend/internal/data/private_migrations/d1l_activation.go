package migrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"

	"github.com/google/uuid"
)

const d1LActivationNonceSize = 32
const d1LActivationSize = d1LActivationNonceSize + sha256.Size

// sealD1LActivation creates an in-memory one-shot presentation. The database
// stores only SHA-256(nonce); the MAC binds every immutable lease field and
// database-owned expiry, so a presentation cannot cross a lease, generation,
// target, evidence, verifier, or expiry boundary.
func sealD1LActivation(nonce []byte, lease d1LLease) []byte {
	if len(nonce) != d1LActivationNonceSize || len(lease.CapabilityVerifierDigest) != sha256.Size {
		return nil
	}
	mac := d1LActivationMAC(nonce, lease)
	presentation := make([]byte, d1LActivationSize)
	copy(presentation, nonce)
	copy(presentation[d1LActivationNonceSize:], mac[:])
	return presentation
}

func verifyD1LActivation(presentation []byte, lease d1LLease) bool {
	if len(presentation) != d1LActivationSize || len(lease.CapabilityVerifierDigest) != sha256.Size {
		return false
	}
	nonce := presentation[:d1LActivationNonceSize]
	verifier := sha256.Sum256(nonce)
	if !hmac.Equal(verifier[:], lease.CapabilityVerifierDigest) {
		return false
	}
	want := d1LActivationMAC(nonce, lease)
	return hmac.Equal(want[:], presentation[d1LActivationNonceSize:])
}

func d1LActivationMAC(nonce []byte, lease d1LLease) [32]byte {
	mac := hmac.New(sha256.New, nonce)
	writeD1LActivationUUID(mac, lease.LeaseID)
	writeD1LActivationUUID(mac, lease.OperationID)
	writeD1LActivationUUID(mac, lease.AttemptID)
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(lease.Generation))
	_, _ = mac.Write(number[:])
	writeD1LActivationBytes(mac, lease.TargetFingerprint)
	writeD1LActivationBytes(mac, lease.EvidenceDigest)
	writeD1LActivationBytes(mac, lease.CapabilityVerifierDigest)
	binary.BigEndian.PutUint64(number[:], uint64(lease.ExpiresAt.UTC().UnixNano()))
	_, _ = mac.Write(number[:])
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

func writeD1LActivationUUID(dst interface{ Write([]byte) (int, error) }, id uuid.UUID) {
	_, _ = dst.Write(id[:])
}

func writeD1LActivationBytes(dst interface{ Write([]byte) (int, error) }, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = dst.Write(length[:])
	_, _ = dst.Write(value)
}
