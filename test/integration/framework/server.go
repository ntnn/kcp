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

package framework

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/component-base/logs"
	"k8s.io/klog/v2"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/embeddedetcd"
	"github.com/spf13/cobra"

	"github.com/kcp-dev/kcp/cmd/kcp/options"
	"github.com/kcp-dev/kcp/pkg/server"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"

	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

func init() {
	kcptestingserver.RunInProcessFunc = RunKCPInProcess
}

func RunKCPInProcess(kcpCtx context.Context, tb kcptestingserver.TestingT, dataDir string, args []string) (<-chan struct{}, error) {
	kcpOptions := options.NewOptions(dataDir)
	// kcpOptions.Server.GenericControlPlane.Logs.Verbosity = logsapiv1.VerbosityLevel(2)
	// kcpOptions.Server.Extra.AdditionalMappingsFile = additionalMappingsFile

	fss := cliflag.NamedFlagSets{}
	kcpOptions.AddFlags(&fss)

	// Setting up a separate context that will be cancelled in the
	// goroutine running kcp.
	// This context cannot come from kcpCtx because kcpCtx gets
	// cancelled to signal the shutdown. When the shutdown happens kcp
	// will need to finish writing to etcd before etcd can shutdown,
	// otherwise there will be goroutines popping up in the leak tests.
	etcdCtx, etcdCancel := context.WithCancel(context.Background())

	startCmd := &cobra.Command{
		PersistentPreRunE: func(*cobra.Command, []string) error {
			// silence client-go warnings.
			// apiserver loopback clients should not log self-issued warnings.
			rest.SetDefaultWarningHandler(rest.NoWarnings{})
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// run as early as possible to avoid races later when some components (e.g. grpc) start early using klog
			// if err := logsapiv1.ValidateAndApply(kcpOptions.Server.GenericControlPlane.Logs, kcpfeatures.DefaultFeatureGate); err != nil {
			// 	return err
			// }

			completedKcpOptions, err := kcpOptions.Complete()
			if err != nil {
				return err
			}

			if errs := completedKcpOptions.Validate(); len(errs) > 0 {
				return utilerrors.NewAggregate(errs)
			}

			logger := klog.FromContext(cmd.Context())
			logger.Info("running with selected batteries", "batteries", strings.Join(completedKcpOptions.Server.Extra.BatteriesIncluded, ","))

			serverConfig, err := server.NewConfig(kcpCtx, completedKcpOptions.Server)
			if err != nil {
				return err
			}

			completedConfig, err := serverConfig.Complete()
			if err != nil {
				return err
			}

			// the etcd server must be up before NewServer because storage decorators access it right away
			if completedConfig.EmbeddedEtcd.Config != nil {
				if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(etcdCtx); err != nil {
					return err
				}
			}

			s, err := server.NewServer(completedConfig)
			if err != nil {
				return err
			}
			return s.Run(kcpCtx)
		},
	}

	globalflag.AddGlobalFlags(fss.FlagSet("global"), startCmd.Name(), logs.SkipLoggingConfigurationFlags())

	if err := startCmd.ValidateArgs(args); err != nil {
		return nil, err
	}

	stopCh := make(chan struct{})
	go func() {
		defer close(stopCh)
		defer etcdCancel()
		logs.InitLogs()
		if err := startCmd.Execute(); err != nil {
			tb.Errorf("error in kcp: %w", err)
		}
	}()

	return stopCh, nil
}

// StartTestServer starts a KCP server for testing purposes.
//
// It returns a clientset for the KCP server.
//
// The returned function can be called to explicitly stop the server,
// the server is implicitly stopped when the test ends.
func StartTestServer(tb testing.TB) (kcpclientset.ClusterInterface, kcpkubernetesclientset.ClusterInterface, func()) {
	tb.Helper()

	server := kcptesting.PrivateKcpServer(
		tb,
		kcptestingserver.WithScratchDirectories(
			filepath.Join(tb.TempDir(), "artifact"),
			filepath.Join(tb.TempDir(), "data"),
		),
		kcptestingserver.WithRunInProcess(),
	)

	kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(tb))
	if err != nil {
		tb.Fatal(err)
	}

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(tb))
	if err != nil {
		tb.Fatal(err)
	}

	return kcpClusterClient, kubeClusterClient, server.Cancel
}
