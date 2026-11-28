// Copyright 2026 Arctel.net
// SPDX-License-Identifier: Apache-2.0

// Package cloudflare reconciles OpenFlare DNS pointing groups.
package cloudflare

import "github.com/Rain-kl/Wavelet/internal/openflare/dnsprovider"

	"Wavelet/pkg/httppool"
)

var (
	// NewHTTPClient creates the Cloudflare adapter.
	NewHTTPClient = dnsprovider.NewHTTPClient
	// WithBaseURL overrides the Cloudflare API endpoint.
	WithBaseURL = dnsprovider.WithBaseURL
	// WithHTTPClient overrides the Cloudflare HTTP client.
	WithHTTPClient = dnsprovider.WithHTTPClient
)
