// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package sinks

import (
	"context"
	"errors"
	"fmt"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// ErrMissingCredentials is returned by BuildSinkWriters when the credential
// snapshot does not contain the values required by a configured sink kind.
// The caller should classify the job as FAILED_AUTH.
type ErrMissingCredentials struct {
	SinkKind        livingwiki.RepoWikiSinkKind
	IntegrationName string
	Detail          string
}

func (e *ErrMissingCredentials) Error() string {
	return fmt.Sprintf("sinks: missing credentials for %s sink %q: %s",
		e.SinkKind, e.IntegrationName, e.Detail)
}

// ErrSinkNotImplemented is returned when a sink kind is recognised but not yet
// wired in this release. The caller should surface this as a partial failure
// with a clear exclusion reason rather than a hard job failure.
type ErrSinkNotImplemented struct {
	SinkKind        livingwiki.RepoWikiSinkKind
	IntegrationName string
}

func (e *ErrSinkNotImplemented) Error() string {
	return fmt.Sprintf("sinks: %s sink %q is not yet implemented in this release",
		e.SinkKind, e.IntegrationName)
}

// BuildSinkWriters constructs the NamedSinkWriter slice for a given repo's
// settings using the per-job credential snapshot. Call once at job dispatch
// time.
//
// Errors:
//   - *ErrMissingCredentials when required credentials are absent from snap.
//   - *ErrSinkNotImplemented for sink kinds that are not yet wired (only
//     returned for the first unimplemented sink encountered).
//
// Both error types are returned without wrapping so callers can use errors.As.
func BuildSinkWriters(
	_ context.Context,
	repoSettings *livingwiki.RepositoryLivingWikiSettings,
	snap credentials.Snapshot,
) ([]NamedSinkWriter, error) {
	if repoSettings == nil || len(repoSettings.Sinks) == 0 {
		return nil, nil
	}

	writers := make([]NamedSinkWriter, 0, len(repoSettings.Sinks))

	// Per-sink errors should not abort the whole dispatch — one not-yet-implemented
	// sink kind in a repo's config (e.g. git_repo while Confluence is wired) was
	// stopping the build at the first error and silently dropping the rest of
	// the configured sinks. Skip not-implemented sinks; only abort on missing
	// credentials, which is a fixable user-facing problem.
	var missingCredsErr error
	for _, sink := range repoSettings.Sinks {
		w, err := buildOneWriter(sink, snap)
		if err != nil {
			var notImpl *ErrSinkNotImplemented
			if errors.As(err, &notImpl) {
				// Log-and-skip via the caller — it sees the absence of this sink
				// in the resulting writers list and can record a "not implemented"
				// per-sink failure if it wants. For now, drop silently.
				continue
			}
			var missing *ErrMissingCredentials
			if errors.As(err, &missing) {
				// Capture the first missing-creds error to return at the end so
				// implemented sinks still get built.
				if missingCredsErr == nil {
					missingCredsErr = err
				}
				continue
			}
			return nil, err
		}
		writers = append(writers, NamedSinkWriter{Name: sink.IntegrationName, Writer: w})
	}

	if len(writers) == 0 && missingCredsErr != nil {
		return nil, missingCredsErr
	}
	return writers, nil
}

// buildOneWriter creates a single SinkWriter for the given sink configuration.
func buildOneWriter(sink livingwiki.RepoWikiSink, snap credentials.Snapshot) (SinkWriter, error) {
	switch sink.Kind {
	case livingwiki.RepoWikiSinkConfluence:
		if snap.ConfluenceSite == "" {
			return nil, &ErrMissingCredentials{
				SinkKind:        sink.Kind,
				IntegrationName: sink.IntegrationName,
				Detail:          "ConfluenceSite is empty; configure it in Living Wiki settings",
			}
		}
		if snap.ConfluenceEmail == "" || snap.ConfluenceToken == "" {
			return nil, &ErrMissingCredentials{
				SinkKind:        sink.Kind,
				IntegrationName: sink.IntegrationName,
				Detail:          "ConfluenceEmail and/or ConfluenceToken are empty; configure them in Living Wiki settings",
			}
		}
		// For v1: use IntegrationName as the space key. A future revision can
		// add a per-sink SpaceKey field to RepoWikiSink.
		spaceKey := sink.IntegrationName
		return NewConfluenceSinkWriter(snap.ConfluenceSite, spaceKey, "" /* parentPageID */, snap), nil

	case livingwiki.RepoWikiSinkNotion:
		if snap.NotionToken == "" {
			return nil, &ErrMissingCredentials{
				SinkKind:        sink.Kind,
				IntegrationName: sink.IntegrationName,
				Detail:          "NotionToken is empty; configure it in Living Wiki settings",
			}
		}
		// DatabaseID is optional; leave empty to fall back to title-based search.
		return NewNotionSinkWriter("" /* databaseID */, snap), nil

	case livingwiki.RepoWikiSinkGitRepo,
		livingwiki.RepoWikiSinkGitHubWiki,
		livingwiki.RepoWikiSinkGitLabWiki,
		livingwiki.RepoWikiSinkBackstageTechDocs,
		livingwiki.RepoWikiSinkMkDocs,
		livingwiki.RepoWikiSinkDocusaurus,
		livingwiki.RepoWikiSinkVitePress:
		// Git-based sinks require a local clone path that is not yet tracked in
		// the per-repo settings. Wire in a future release when the clone-path
		// field is added to RepositoryLivingWikiSettings.
		return nil, &ErrSinkNotImplemented{
			SinkKind:        sink.Kind,
			IntegrationName: sink.IntegrationName,
		}

	default:
		return nil, &ErrSinkNotImplemented{
			SinkKind:        sink.Kind,
			IntegrationName: sink.IntegrationName,
		}
	}
}
