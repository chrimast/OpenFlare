// Copyright 2026 Arctel.net
// SPDX-License-Identifier: Apache-2.0

package upload

import (
	"Wavelet/plugins/domain/upload/models"
	"io"
)

// IngestPolicy controls how ingest handles hash collisions and record creation.
type IngestPolicy int

const (
	// PolicyCreate always stores a new object and creates a new upload record.
	PolicyCreate IngestPolicy = iota

	// PolicyDedupNewRecord reuses an existing object path on hash match but creates a new record and stats delta.
	PolicyDedupNewRecord

	// PolicyResolveExisting returns an existing upload on hash match without creating a record or stats delta.
	PolicyResolveExisting
)

// ObjectKeyFn builds the storage object key for a new upload.
type ObjectKeyFn func(id uint64, ext string) string

// IngestRequest describes a programmatic file ingest operation.
type IngestRequest struct {
	UserID uint64
	Type   string

	AccessMode *int
	Status     models.UploadStatus

	Reader    io.Reader
	Size      int64
	FileName  string
	MimeType  string
	Extension string
	Hash      string

	Metadata models.UploadMetadata
	Policy   Policy

	ObjectKeyFn ObjectKeyFn

	// SkipExtensionCheck bypasses the configured upload extension whitelist.
	SkipExtensionCheck bool
}

// Result reports the outcome of an ingest operation.
type Result struct {
	Upload   models.Upload
	Created  bool
	Stored   bool
	Resolved bool
}
