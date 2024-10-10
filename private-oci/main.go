package main

import (
	"fmt"

	fluxsrc "github.com/fluxcd/source-controller/api/v1"
	"github.com/gregjones/httpcache"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	kmapi "kmodules.xyz/client-go/api/v1"
	"kubepack.dev/lib-helm/pkg/repo"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	releasesapi "x-helm.dev/apimachinery/apis/releases/v1alpha1"
)

func NewClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = releasesapi.AddToScheme(scheme)
	_ = fluxsrc.AddToScheme(scheme)

	ctrl.SetLogger(klog.NewKlogr())
	cfg := ctrl.GetConfigOrDie()
	cfg.QPS = 100
	cfg.Burst = 100

	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}
	mapper, err := apiutil.NewDynamicRESTMapper(cfg, hc)
	if err != nil {
		return nil, err
	}

	return client.New(cfg, client.Options{
		Scheme: scheme,
		Mapper: mapper,
		//Opts: client.WarningHandlerOptions{
		//	SuppressWarnings:   false,
		//	AllowDuplicateLogs: false,
		//},
	})
}

func main() {
	err := doStuff()
	if err != nil {
		panic(err)
	}
}

func doStuff() error {
	kc, err := NewClient()
	if err != nil {
		return err
	}
	reg := repo.NewRegistry(kc, httpcache.NewMemoryCache())

	chartRef := releasesapi.ChartSourceRef{
		Name:    "opscenter-features",
		Version: "v2024.10.7",
		SourceRef: kmapi.TypedObjectReference{
			APIGroup:  releasesapi.SourceGroupHelmRepository,
			Kind:      releasesapi.SourceKindHelmRepository,
			Namespace: "kubeops",
			Name:      "bootstrap",
		},
	}

	chrt, err := reg.GetChart(chartRef)
	if err != nil {
		return err
	}
	fmt.Println(chrt.Metadata.Name)
	return nil
}
