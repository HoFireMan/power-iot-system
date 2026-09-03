package migrations

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
)

// D1LInstallerDigestV1 is the SHA-256 of the exact, embedded installer bytes.
const D1LInstallerDigestV1 = "d5a2446fb6082d51f67f2f52c54c21afdecf679ad9288854639d2ba9d4d45a0f"

const d1LInstallerArtifactLengthV1 = 10000

// D1LInstallerDigestNext is the digest of the immutable additive ledger
// transition artifact. It is the only digest accepted for the next current
// control version; the v1 digest above remains unchanged forever.
const D1LInstallerDigestNext = "7036aa689b570a8fa9fb18d111a3f03a9177d420b84bdefadaee155f470e8de1"

const d1LLedgerTransitionArtifactLength = 5319

//go:embed d1l/install_v1.sql
var d1LInstallerBytes []byte

//go:embed d1l/install_v2.sql
var d1LLedgerTransitionBytes []byte

var ErrD1LArtifactDigest = errors.New("D1-L installer artifact digest mismatch")

func D1LInstallerSQL() []byte {
	return append([]byte(nil), d1LInstallerBytes...)
}

// D1LLedgerTransitionSQL returns the additive ledger transition artifact. It
// is intentionally separate from the immutable v1 installer.
func D1LLedgerTransitionSQL() []byte { return append([]byte(nil), d1LLedgerTransitionBytes...) }

func D1LInstallerDigest() [32]byte { return sha256.Sum256(d1LInstallerBytes) }

func D1LLedgerTransitionDigest() [32]byte { return sha256.Sum256(d1LLedgerTransitionBytes) }

func D1LLedgerTransitionDigestHex() string {
	d := D1LLedgerTransitionDigest()
	return hex.EncodeToString(d[:])
}

func d1LLedgerTransitionDigestBytes() []byte {
	d, _ := hex.DecodeString(D1LInstallerDigestNext)
	return append([]byte(nil), d...)
}

func D1LInstallerDigestHex() string { d := D1LInstallerDigest(); return hex.EncodeToString(d[:]) }

func d1LInstallerDigestBytes() []byte {
	d, _ := hex.DecodeString(D1LInstallerDigestV1)
	return append([]byte(nil), d...)
}

func verifyD1LInstallerArtifact() error {
	return verifyD1LInstallerArtifactBytes(d1LInstallerBytes)
}

// verifyD1LInstallerArtifactBytes verifies the exact bytes that a caller is
// about to execute.  Keeping the digest over the supplied slice (rather than
// rereading the embedded variable after this function returns) binds the
// successful check to the execution input.
func verifyD1LInstallerArtifactBytes(installer []byte) error {
	if len(installer) != d1LInstallerArtifactLengthV1 {
		return ErrD1LArtifactDigest
	}
	digest := sha256.Sum256(installer)
	if hex.EncodeToString(digest[:]) != D1LInstallerDigestV1 {
		return ErrD1LArtifactDigest
	}
	return nil
}

func verifyD1LLedgerTransitionArtifactBytes(artifact []byte) error {
	if len(artifact) != d1LLedgerTransitionArtifactLength {
		return ErrD1LArtifactDigest
	}
	digest := sha256.Sum256(artifact)
	if hex.EncodeToString(digest[:]) != D1LInstallerDigestNext {
		return ErrD1LArtifactDigest
	}
	return nil
}
