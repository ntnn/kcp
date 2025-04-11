package framework

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kcp-dev/client-go/apiextensions/client"
	"github.com/kcp-dev/kcp/cmd/kcp/options"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
	"github.com/kcp-dev/kcp/pkg/server"
	kcpclusterclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
)

func unusedPort() (int, func() error, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, nil, fmt.Errorf("could not bind to a port: %v", err)
	}
	return l.Addr().(*net.TCPAddr).Port, l.Close, nil
}

func unusedPorts() (int, int, int, error) {
	port1, close1, err := unusedPort()
	if err != nil {
		return 0, 0, 0, err
	}
	defer close1()

	port2, close2, err := unusedPort()
	if err != nil {
		return 0, 0, 0, err
	}
	defer close2()

	port3, close3, err := unusedPort()
	if err != nil {
		return 0, 0, 0, err
	}
	defer close3()

	return port1, port2, port3, nil
}

// StartTestServer starts a KCP server for testing purposes.
//
// It returns a clientset for the KCP server.
//
// The returned function can be called to explicitly stop the server,
// the server is implicitly stopped when the test ends.
func StartTestServer(tb testing.TB) (*kcpclusterclientset.ClusterClientset, func()) {
	// tb.Helper()

	// etcdUrl, etcdCancel, err := RunCustomEtcd(filepath.Join(tb.TempDir(), "etcd"), nil, nil)
	// if err != nil {
	// 	tb.Fatalf("failed to start etcd: %v", err)
	// }
	// tb.Cleanup(etcdCancel)

	ctx, cancel := context.WithCancel(tb.Context())
	// tb.Cleanup(cancel)

	// rootDir, err := os.MkdirTemp("", "test-kcpintegration-"+strings.ReplaceAll(tb.Name(), "/", "_"))
	// if err != nil {
	// 	tb.Fatalf("Couldn't create temp dir: %v", err)
	// }

	kcpOptions := options.NewOptions(tb.TempDir())

	// kcpOptions.Server.EmbeddedEtcd.ClientPort = "2379"
	// kcpOptions.Server.EmbeddedEtcd.PeerPort = "2379"

	// kcpOptions.Server.GenericControlPlane.Etcd.StorageConfig.Transport.ServerList = []string{etcdUrl}
	etcdClientPort, etcdPeerPort, kcpBindPort, err := unusedPorts()
	if err != nil {
		tb.Fatalf("failed to get available port: %v", err)
	}

	kcpOptions.Server.EmbeddedEtcd.ClientPort = strconv.Itoa(etcdClientPort)
	kcpOptions.Server.EmbeddedEtcd.PeerPort = strconv.Itoa(etcdPeerPort)
	kcpOptions.Server.EmbeddedEtcd.Directory = filepath.Join(tb.TempDir(), "etcd")

	kcpOptions.Server.GenericControlPlane.SecureServing.BindAddress = net.ParseIP("127.0.0.1")
	kcpOptions.Server.GenericControlPlane.SecureServing.BindPort = kcpBindPort

	// kcpOptions.Server.GenericServerRunOptions.AdvertiseAddress = net.ParseIP("127.0.0.1")
	// kcpOptions.Server.GenericControlPlane.Etcd.StorageConfig.Prefix = tb.Name()

	// kcpOptions.Server.GenericControlPlane.EtcdClientPort = kcpOptions.Server.EmbeddedEtcd.ClientPort
	// kcpOptions.Server.GenericControlPlane.GenericServerRunOptions

	completedKcpOptions, err := kcpOptions.Complete()
	if err != nil {
		tb.Fatalf("failed to complete kcp options: %v", err)
	}

	if errs := completedKcpOptions.Validate(); len(errs) > 0 {
		tb.Fatalf("failed to validate kcp options: %v", errors.Join(errs...))
	}

	// logger := klog.FromContext(ctx)

	serverConfig, err := server.NewConfig(ctx, completedKcpOptions.Server)
	if err != nil {
		tb.Fatalf("failed to create server config: %v", err)
	}

	completedConfig, err := serverConfig.Complete()
	if err != nil {
		tb.Fatalf("failed to complete server config: %v", err)
	}

	if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(ctx); err != nil {
		tb.Fatalf("failed to run embedded etcd server: %v", err)
	}

	s, err := server.NewServer(completedConfig)
	if err != nil {
		tb.Fatalf("failed to create server: %v", err)
	}

	go func() {
		// defer func() {
		// 	if err := os.RemoveAll(rootDir); err != nil {
		// 		tb.Log(err)
		// 	}
		// }()
		if err := s.Run(ctx); err != nil {
			tb.Fatalf("failed to run server: %v", err)
		}
	}()

	kcpServerClientConfig := rest.CopyConfig(completedConfig.GenericConfig.LoopbackClientConfig)
	// kcpServerClientConfig.CAFile = path.Join(certDir, "apiserver.crt")
	// kcpServerClientConfig.CAData = nil
	// kcpServerClientConfig.ServerName = ""

	if err := wait.PollImmediate(1*time.Second, 60*time.Second, func() (done bool, err error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}

		healthzConfig := rest.CopyConfig(kcpServerClientConfig)
		// healthzConfig.ContentType = ""
		// healthzConfig.AcceptContentTypes = ""
		kcpClient, err := client.NewForConfig(healthzConfig)
		if err != nil {
			// this happens because we race the API server start
			tb.Log(err)
			return false, nil
		}

		healthStatus := 0
		kcpClient.Discovery().RESTClient().Get().AbsPath("/healthz").Do(ctx).StatusCode(&healthStatus)
		if healthStatus != http.StatusOK {
			return false, nil
		}

		return true, nil
	}); err != nil {
		tb.Fatal(err)
	}

	kcpServerClient, err := kcpclusterclientset.NewForConfig(kcpServerClientConfig)
	if err != nil {
		tb.Fatal(err)
	}

	return kcpServerClient, cancel
}
