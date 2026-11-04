package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
)

var (
	testEnv    *envtest.Environment
	cfg        *rest.Config
	testScheme *runtime.Scheme
	k8sClient  client.WithWatch
	clientset  *kubernetes.Clientset
)

func TestMain(m *testing.M) {
	testScheme = runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(testScheme); err != nil {
		fmt.Fprintln(os.Stderr, "add client-go scheme:", err)
		os.Exit(1)
	}
	if err := fleetv1alpha1.AddToScheme(testScheme); err != nil {
		fmt.Fprintln(os.Stderr, "add fleet scheme:", err)
		os.Exit(1)
	}

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest start failed:", err)
		os.Exit(1)
	}
	k8sClient, err = client.NewWithWatch(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, "client failed:", err)
		_ = testEnv.Stop()
		os.Exit(1)
	}
	clientset, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "clientset failed:", err)
		_ = testEnv.Stop()
		os.Exit(1)
	}

	code := m.Run()
	if err := testEnv.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "envtest stop failed:", err)
		code = 1
	}
	os.Exit(code)
}
