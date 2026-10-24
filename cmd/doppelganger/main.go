// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// doppelganger answers questions on Jody's behalf from the jody context repo,
// as a fleet service: client-facing through the gateway, the model through
// llmproxy. Configuration (repo_dir, state_dir, model_alias) arrives from the
// fleet's config state; nothing is read from flags or the environment.
//
// The local client that authenticates as a user and calls this service through
// the gateway is a separate program (not written yet).
package main

import (
	"fmt"
	"os"

	"go.uber.org/zap"

	"github.com/tacplus-dev/doppelganger/internal/daemon"
	loggerchassis "github.com/tacplus-dev/doppelganger/tacplus/chassis/logger"
	sm "github.com/tacplus-dev/doppelganger/tacplus/s2sm"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	logger, err := loggerchassis.InstallFromConfig(nil)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	zap.ReplaceGlobals(logger)
	defer logger.Sync() //nolint:errcheck

	s, err := sm.New()
	if err != nil {
		return fmt.Errorf("servicemanager: %w", err)
	}
	defer s.Close()

	// Run blocks: it serves the generated gRPC surface, delivers the config
	// (repo_dir, state_dir, model_alias) to the handler at start and on change,
	// reports health, and returns on shutdown.
	return s.Run(&daemon.Server{
		NewProvider: func(modelAlias string) daemon.Provider {
			return &daemon.LlmProxy{Client: s.Grpc().Clients().Services().LlmProxyClient(), ModelAlias: modelAlias}
		},
	})
}
