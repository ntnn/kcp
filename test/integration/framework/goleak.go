package framework

import (
	"testing"

	"go.uber.org/goleak"
)

var (
	IgnoreEtcdGoroutines = []goleak.Option{
		// ignore klog
		goleak.IgnoreCreatedBy("k8s.io/klog/v2.(*flushDaemon).run"),

		// ignore if etcd leaks stuff
		goleak.IgnoreCreatedBy("github.com/kcp-dev/embeddedetcd.(*Server).Run"),
		goleak.IgnoreCreatedBy("go.etcd.io/etcd/client/v3.(*watchGrpcStream).run"),

		// random grpc leaks
		goleak.IgnoreCreatedBy("google.golang.org/grpc.(*acBalancerWrapper).Connect"),
		goleak.IgnoreCreatedBy("google.golang.org/grpc/internal/grpcsync.NewCallbackSerializer"),

		// expected kcp leaks
		goleak.IgnoreCreatedBy("github.com/kcp-dev/kcp/pkg/informer.NewGenericDiscoveringDynamicSharedInformerFactory[...]"),

		// expected kube leaks
		goleak.IgnoreCreatedBy("k8s.io/apiserver/pkg/registry/generic/registry.(*Store).startObservingCount"),
		goleak.IgnoreCreatedBy("k8s.io/apiserver/pkg/server/healthz.(*log).Check.func1"),
		goleak.IgnoreCreatedBy("k8s.io/apiserver/pkg/storage/cacher.(*watchCache).waitUntilFreshAndBlock"),
		goleak.IgnoreCreatedBy("k8s.io/apiserver/pkg/storage/storagebackend/factory.newETCD3Check"),
		goleak.IgnoreCreatedBy("k8s.io/client-go/util/workqueue.newDelayingQueue[...]"),
		goleak.IgnoreCreatedBy("k8s.io/client-go/util/workqueue.newQueue[...]"),
	}
)

func GoleakWithDefaults(tb testing.TB, in ...goleak.Option) {
	opts := []goleak.Option{}
	opts = append(opts, IgnoreEtcdGoroutines...)
	opts = append(opts, in...)
	goleak.VerifyNone(tb, opts...)
}
