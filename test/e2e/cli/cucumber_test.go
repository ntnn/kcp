package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/pkg/registry/core/rest"
)

func TestFeatures(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "cli")

	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"."},
			TestingT: t, // Testing instance that will run subtests.
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

type scenarioCtx struct {
	server         kcptestingserver.RunningServer
	cfg            *rest.Config
	kubeconfigPath string

	stdout, stderr *bytes.Buffer
}

func (sc *scenarioCtx) iHaveSharedServer(ctx context.Context) error {
	sc.server = kcptesting.SharedKcpServer(godog.T(ctx))
	sc.kubeconfigPath = writeKubeconfig(godog.T(ctx), sc.server)
	return nil
}

func (sc *scenarioCtx) iRunPlugin(ctx context.Context, plugin string, args ...string) error {
	stdout, stderr, err := framework.RunKcpCliPlugin(godog.T(ctx), plugin, sc.kubeconfigPath, args)
	if err != nil {
		return err
	}
	sc.stdout = stdout
	sc.stderr = stderr
	return nil
}

func (sc *scenarioCtx) workspaceShouldExist(ctx context.Context, wsName string) error {
	clientset, err := kcpclientset.NewForConfig(sc.server.BaseConfig(godog.T(ctx)))
	if err != nil {
		return err
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := clientset.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(ctx, wsName, v1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "workspace %q not found", wsName)
	return nil
}

func InitializeScenario(godogCtx *godog.ScenarioContext) {
	sc := &scenarioCtx{}
	godogCtx.Step(`^I have a shared server$`, sc.iHaveSharedServer)
	godogCtx.Step(`^I run the "([^"]*)" plugin with args "([^"]*)"$`, sc.iRunPlugin)
	godogCtx.Step(`^the workspace "([^"]*)" should exist$`, sc.workspaceShouldExist)
}
