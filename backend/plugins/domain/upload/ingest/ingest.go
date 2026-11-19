// Copyright 2026 Arctel.net
// SPDX-License-Identifier: Apache-2.0

package upload

import (
	"Wavelet/plugins/domain/upload/models"
	"Wavelet/plugins/domain/upload/repository"
	"context"
	"errors"

	"gorm.io/gorm"
)

// Ingest stores or resolves an upload using the configured policy and side effects.
func Ingest(ctx context.Context, req IngestRequest) (IngestResult, error) {
	normalizeRequest(&req)
	if req.Hash == "" {
		return IngestResult{}, errors.New("ingest hash is required")
	}
	if req.Reader == nil {
		return IngestResult{}, errors.New("ingest reader is required")
	}
	if req.Size < 0 {
		return IngestResult{}, errors.New("ingest size must be non-negative")
	}

	switch req.Policy {
	case PolicyDedupNewRecord, PolicyResolveExisting:
		return ingestWithHashPolicy(ctx, req)
	case PolicyCreate:
		return createNewUpload(ctx, req)
	default:
		return IngestResult{}, errors.New("unsupported ingest policy")
	}
}

// FindByHash returns a reusable active upload with the same hash and size.
func FindByHash(ctx context.Context, hash string, size int64) (models.Upload, error) {
	return repository.FindReusableUploadByHash(ctx, hash, size)
}

func ingestWithHashPolicy(ctx context.Context, req IngestRequest) (IngestResult, error) {
	existing, err := repository.FindReusableUploadByHash(ctx, req.Hash, req.Size)
	if err == nil {
		switch req.Policy {
		case PolicyResolveExisting:
			return IngestResult{
				Upload:   existing,
				Resolved: true,
			}, nil
		case PolicyDedupNewRecord:
			if uploadstorageReadOnly(ctx) {
				return IngestResult{}, ErrIngestStorageReadOnly
			}
			return createDedupRecord(ctx, existing, req)
		}
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return IngestResult{}, err
	}

	return createNewUpload(ctx, req)
}
