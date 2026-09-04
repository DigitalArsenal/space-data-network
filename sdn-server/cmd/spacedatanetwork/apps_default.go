package main

// The node's DEFAULT-$APP wiring.
//
// Owner ruling 2026-08-04, verbatim: "the dashboard is an $APP itself, should
// be, just like orbital-console, and what gets copied over to spaceaware.io
// repo under the beta path is the client SDN with the $APP that is published
// to sdn.spaceaware.io, and the browser client pulls it in and uses it as the
// default app. That's something else we need to change, there needs to be a
// default $APP for the SDN node software (server or browser), it's the
// Dashboard for the server and the orbital-console for the browser, with both
// loaded, and just like in the design there's a link to each one in the other."
//
// So this file does exactly two things:
//
//  1. Mints the node's own dashboard as a REAL $APP record from the artifact
//     bytes already go:embed'ed for "/" — same bytes, two envelopes, so the
//     record and the served page can never disagree.
//  2. Applies the operator's declarations (apps.declared / apps.default_*) so
//     the node can also advertise the browser-class default it does not serve
//     itself.
//
// Everything else — decoding, defaults resolution, the anonymous read — is
// generic plumbing in internal/apps and internal/api. No app logic in Go.

import (
	"fmt"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/apps"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// dashboardAppID is the stable $APP identity of the node's own homepage.
// Stable across builds on purpose: a client that bookmarked the default app of
// a node must find the same app there after an upgrade.
const dashboardAppID = "sdn-dashboard"

// dashboardAppName / dashboardAppDescription are the record's display fields.
const (
	dashboardAppName        = "SDN Node Dashboard"
	dashboardAppDescription = "The SDN node's own status and administration face: peers, identity, apps and data on this node. Server-class default $APP, served at / by the node that owns it."
)

// dashboardPageID keys the single inline UI page of the dashboard $APP.
const dashboardPageID = "dashboard"

// buildAppRegistry assembles the node's $APP registry.
//
// dashboard is the built self-contained homepage (may be empty when the
// artifact was not built into the binary — the node then serves the wordmark
// placeholder at "/" and advertises NO server-class app, which is the honest
// answer rather than a record describing a page that is not there).
//
// Errors from operator declarations are RETURNED, not swallowed: a config that
// names a default the node does not have is a mistake the operator must see.
func buildAppRegistry(cfg config.AppsConfig, dashboard []byte) (*apps.Registry, error) {
	registry := apps.New(api.AppsRecordPrefix)

	if len(dashboard) > 0 {
		record, err := apps.BuildInlinePageRecord(
			apps.AppIdentity{
				ID:          dashboardAppID,
				Name:        dashboardAppName,
				Version:     versioninfo.Version(),
				Description: dashboardAppDescription,
			},
			[]apps.InlinePage{{
				ID:          dashboardPageID,
				Title:       dashboardAppName,
				Description: dashboardAppDescription,
				HTML:        dashboard,
				Entry:       true,
			}},
		)
		if err != nil {
			return nil, fmt.Errorf("apps: build dashboard $APP record: %w", err)
		}
		if _, err := registry.InstallRecord(apps.RuntimeServer, "/", record); err != nil {
			return nil, fmt.Errorf("apps: install dashboard $APP record: %w", err)
		}
	}

	for i, installed := range cfg.Installed {
		class, ok := apps.ParseRuntimeClass(installed.RuntimeClass)
		if !ok {
			return nil, fmt.Errorf(
				"apps.installed[%d] (%q): runtime_class %q is not one of server/browser",
				i, installed.ID, installed.RuntimeClass)
		}
		record, err := os.ReadFile(installed.RecordPath)
		if err != nil {
			return nil, fmt.Errorf("apps.installed[%d] (%q): read record: %w", i, installed.ID, err)
		}
		entry, err := registry.InstallRecord(class, installed.URL, record)
		if err != nil {
			return nil, fmt.Errorf("apps.installed[%d] (%q): %w", i, installed.ID, err)
		}
		if entry.ID != installed.ID {
			return nil, fmt.Errorf(
				"apps.installed[%d]: config names %q but the record at %s is %q — refusing the mismatch",
				i, installed.ID, installed.RecordPath, entry.ID)
		}
	}

	for i, declared := range cfg.Declared {
		class, ok := apps.ParseRuntimeClass(declared.RuntimeClass)
		if !ok {
			return nil, fmt.Errorf(
				"apps.declared[%d] (%q): runtime_class %q is not one of server/browser",
				i, declared.ID, declared.RuntimeClass)
		}
		if _, err := registry.Declare(apps.Declaration{
			ID:           declared.ID,
			Name:         declared.Name,
			Version:      declared.Version,
			Description:  declared.Description,
			RuntimeClass: class,
			URL:          declared.URL,
		}); err != nil {
			return nil, fmt.Errorf("apps.declared[%d]: %w", i, err)
		}
	}

	for class, configured := range map[apps.RuntimeClass]string{
		apps.RuntimeServer:  cfg.DefaultServerApp,
		apps.RuntimeBrowser: cfg.DefaultBrowserApp,
	} {
		if strings.TrimSpace(configured) == "" {
			continue
		}
		if err := registry.SetDefault(class, configured); err != nil {
			return nil, fmt.Errorf("apps.default_%s_app: %w", class, err)
		}
	}
	return registry, nil
}
