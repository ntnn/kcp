/*
Copyright 2025 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import "context"

// Config qualify a kcp server to start
//
// Deprecated for use outside this package. Prefer PrivateKcpServer().
type Config struct {
	Name        string
	Args        []string
	ArtifactDir string
	DataDir     string
	ClientCADir string

	LogToConsole    bool
	RunInProcess    bool
	RunInProcessCtx context.Context //nolint:containedctx
}

// Option a function that wish to modify a given kcp configuration.
type Option func(*Config)

// WithScratchDirectories adds custom scratch directories to a kcp configuration.
func WithScratchDirectories(artifactDir, dataDir string) Option {
	return func(cfg *Config) {
		cfg.ArtifactDir = artifactDir
		cfg.DataDir = dataDir
	}
}

// WithCustomArguments applies provided arguments to a given kcp configuration.
func WithCustomArguments(args ...string) Option {
	return func(cfg *Config) {
		cfg.Args = args
	}
}

// WithClientCA sets the client CA directory for a given kcp configuration.
// A client CA will automatically created and the --client-ca configured.
func WithClientCA(clientCADir string) Option {
	return func(cfg *Config) {
		cfg.ClientCADir = clientCADir
	}
}

// WithRunInProcess sets the kcp server to run in process. This requires extra
// setup of the RunInProcessFunc variable and will only work inside of the kcp
// repository.
func WithRunInProcess() Option {
	return func(cfg *Config) {
		cfg.RunInProcess = true
	}
}

func WithRunInProcessWithContext(ctx context.Context) Option {
	return func(cfg *Config) {
		cfg.RunInProcess = true
		cfg.RunInProcessCtx = ctx
	}
}

// WithLogToConsole sets the kcp server to log to console.
func WithLogToConsole() Option {
	return func(cfg *Config) {
		cfg.LogToConsole = true
	}
}
