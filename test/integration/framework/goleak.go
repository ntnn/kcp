package framework

import (
	"testing"

	"go.uber.org/goleak"
)

var (
	IgnoreEtcdGoroutines = []goleak.Option{
		goleak.IgnoreTopFunction("go.etcd.io/etcd/server/v3/etcdserver/api/v3rpc.(*watchServer).Watch"),
		goleak.IgnoreTopFunction("go.etcd.io/etcd/client/v3.(*watchGrpcStream).run"),

		// expected kube leaks
		goleak.IgnoreTopFunction("k8s.io/apiserver/pkg/storage/cacher.(*watchCache).waitUntilFreshAndBlock.func2"),
		goleak.IgnoreTopFunction("k8s.io/apimachinery/pkg/util/wait.BackoffUntil"),
		goleak.IgnoreTopFunction("k8s.io/client-go/util/workqueue.(*delayingType[...]).waitingLoop"),
		goleak.IgnoreTopFunction("k8s.io/client-go/util/workqueue.(*Typed[...]).updateUnfinishedWorkLoop"),

		// new kube leaks? o.O
		goleak.IgnoreTopFunction("k8s.io/apiserver/pkg/storage/storagebackend/factory.newETCD3Check.func2"),

		// random grpc leaks that need to be investigated
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.NewCallbackSerializer"),
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*addrConn).resetTransport"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),

		// Leaks in KCP that are just a todo:
		goleak.IgnoreTopFunction("github.com/kcp-dev/kcp/pkg/informer.NewGenericDiscoveringDynamicSharedInformerFactory[...].func3"),
	}
)

func GoleakWithDefaults(tb testing.TB, in ...goleak.Option) {
	opts := []goleak.Option{}
	opts = append(opts, IgnoreEtcdGoroutines...)
	opts = append(opts, in...)
	goleak.VerifyNone(tb, opts...)
}
