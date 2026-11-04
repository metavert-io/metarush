package models

import "time"

// Content Credentials (D415–D424): a C2PA manifest MetaRush puts into the
// videos it delivers, signed with this computer's signing certificate,
// saying how each video was made — with MetaRush, from which footage (by
// hash), and which parts AI made or changed. Names of people, clients and
// projects never go in; an organisation's name goes in only as the publisher
// a person chose.

// What Content Credentials do (ContentCredentialsSettings.Mode,
// CredentialsSetting.Mode).
const (
	// CredentialsOff: delivered videos carry none (the default).
	CredentialsOff = "off"
	// CredentialsOn: every final delivery is signed; one that can't be goes
	// out without them, and its record and the person are told why.
	CredentialsOn = "on"
	// CredentialsRequired: as on, and a version that can't be signed on this
	// computer is held back by the ship gate (a person may still send it out
	// without them, with a note), and a signing that fails stops the delivery.
	CredentialsRequired = "required"
)

// ContentCredentialsSettings is the workspace's choice (owners, D418).
type ContentCredentialsSettings struct {
	Mode string `bson:"mode" json:"mode" jsonschema:"enum=off,enum=on,enum=required"`
	// TimestampURL is a timestamp service a person chose (RFC 3161): it
	// proves when each video was signed, so its credentials stay valid after
	// the certificate runs out. It receives a fingerprint of the manifest,
	// never the video. Empty: no timestamp (nothing leaves this computer).
	TimestampURL string    `bson:"timestampUrl,omitempty" json:"timestampUrl,omitempty" jsonschema:"maxLength=200"`
	UpdatedAt    time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy    Actor     `bson:"updatedBy" json:"updatedBy"`
}

// CredentialsSetting is a Content Settings part (D418): a client's own word
// on Content Credentials over the workspace's (nil: the workspace's).
type CredentialsSetting struct {
	Mode      string    `bson:"mode" json:"mode" jsonschema:"enum=off,enum=on,enum=required"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy Actor     `bson:"updatedBy" json:"updatedBy"`
}

// SigningCertificate is what anyone may see about a signing credential:
// whom it names, who issued it, when it is valid. Never the key.
type SigningCertificate struct {
	// Signer is how verifiers name who signed: the certificate's
	// organisation, else its name.
	Signer       string `json:"signer"`
	Name         string `json:"name"`
	Organisation string `json:"organisation,omitempty"`
	// Issuer is the certificate authority that issued it.
	Issuer string `json:"issuer"`
	// Fingerprint is the SHA-256 of the signing certificate.
	Fingerprint string    `json:"fingerprint"`
	Serial      string    `json:"serial"`
	ValidFrom   time.Time `json:"validFrom"`
	ValidUntil  time.Time `json:"validUntil"`
	Alg         string    `json:"alg" jsonschema:"enum=es256,enum=es384,enum=es512,enum=ps256,enum=ed25519"`
	// Test: a test certificate MetaRush made; verifiers show its signer as unknown.
	Test         bool `json:"test"`
	Certificates int  `json:"certificates"`
}

// SignedDelivery is a delivered video that carries Content Credentials
// (Revision.ContentCredentials, append-only): the receipt lists them.
type SignedDelivery struct {
	At time.Time `bson:"at" json:"at"`
	By Actor     `bson:"by" json:"by"`
	// File and Folder are the delivered file's and its folder's names (never paths).
	File   string `bson:"file" json:"file"`
	Folder string `bson:"folder,omitempty" json:"folder,omitempty"`
	Preset string `bson:"preset,omitempty" json:"preset,omitempty"`
	// Signer names who signed (SigningCertificate.Signer); Test: with a test certificate.
	Signer string `bson:"signer" json:"signer"`
	Test   bool   `bson:"test,omitempty" json:"test,omitempty"`
	// Verified: MetaRush read the delivered copy back and its credentials checked out.
	Verified bool `bson:"verified" json:"verified"`
	// ManifestHash is the SHA-256 of the manifest store in the file; SHA256 the file's.
	ManifestHash string `bson:"manifestHash" json:"manifestHash"`
	SHA256       string `bson:"sha256" json:"sha256"`
	// Fingerprint is the signing certificate's SHA-256.
	Fingerprint string `bson:"fingerprint,omitempty" json:"fingerprint,omitempty"`
	// Timestamped: a timestamp service vouched for when it was signed.
	Timestamped bool `bson:"timestamped,omitempty" json:"timestamped,omitempty"`
}
