/*
Copyright AppsCode Inc. and Contributors

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

package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	_ "go.wandrs.dev/http"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/Masterminds/semver/v3"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/unrolled/render"
	"go.wandrs.dev/binding"
	httpw "go.wandrs.dev/http"
	"gomodules.xyz/logs"
	"k8s.io/klog/v2"
	meta_util "kmodules.xyz/client-go/meta"
	"kmodules.xyz/client-go/tools/converter"
	"kubepack.dev/kubepack/apis/kubepack/v1alpha1"
	"kubepack.dev/kubepack/pkg/lib"
	actionx "kubepack.dev/lib-helm/pkg/action"
	"kubepack.dev/lib-helm/pkg/getter"
	"kubepack.dev/lib-helm/pkg/repo"
	"kubepack.dev/lib-helm/pkg/values"
	chartsapi "kubepack.dev/preset/apis/charts/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/yaml"
)

var HelmRegistry = repo.NewDiskCacheRegistry()

func main__() {
	logs.Init(nil, true)
	defer logs.FlushLogs()

	m := chi.NewRouter()
	m.Use(middleware.RequestID)
	m.Use(middleware.RealIP)
	m.Use(middleware.Logger) // middlewares.NewLogger()
	m.Use(middleware.Recoverer)
	m.Use(binding.Injector(render.New()))

	// PUBLIC
	m.Route("/packageview", func(m chi.Router) {
		m.With(binding.JSON(v1alpha1.ChartRepoRef{})).Get("/", binding.HandlerFunc(GetPackageViewForChart))

		// PUBLIC
		m.With(binding.JSON(v1alpha1.ChartRepoRef{})).Get("/files", binding.HandlerFunc(ListPackageFiles))

		// PUBLIC
		m.With(binding.JSON(v1alpha1.ChartRepoRef{})).Get("/files/*", binding.HandlerFunc(GetPackageFile))

		// PUBLIC
		m.With(binding.JSON(chartsapi.ChartPresetRef{})).Get("/values", binding.HandlerFunc(GetValuesFile))
	})

	m.Get("/chartrepositories", binding.HandlerFunc(func(ctx httpw.ResponseWriter) {
		repos, err := repo.DefaultNamer.ListHelmHubRepositories()
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
		ctx.JSON(http.StatusOK, repos)
	}))
	m.Get("/chartrepositories/charts", binding.HandlerFunc(func(ctx httpw.ResponseWriter) {
		url := ctx.R().Query("url")
		if url == "" {
			ctx.Error(http.StatusBadRequest, "missing url")
			return
		}

		cfg, _, err := HelmRegistry.Get(url)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
		cr, err := repo.NewChartRepository(cfg, getter.All())
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
		err = cr.Load()
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
		ctx.JSON(http.StatusOK, cr.ListCharts())
	}))
	m.Get("/chartrepositories/charts/{name}/versions", binding.HandlerFunc(func(ctx httpw.ResponseWriter) {
		url := ctx.R().Query("url")
		name := ctx.R().Params("name")

		if url == "" {
			ctx.Error(http.StatusBadRequest, "missing url")
			return
		}
		if name == "" {
			ctx.Error(http.StatusBadRequest, "missing chart name")
			return
		}

		cfg, _, err := HelmRegistry.Get(url)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
		cr, err := repo.NewChartRepository(cfg, getter.All())
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
		err = cr.Load()
		if err != nil {
			ctx.Error(http.StatusInternalServerError, err.Error())
			return
		}
		ctx.JSON(http.StatusOK, cr.ListVersions(name))
	}))

	m.Get("/", binding.HandlerFunc(func() string {
		return "Hello world!"
	}))
	klog.Infoln()
	klog.Infoln("Listening on :4000")
	if err := http.ListenAndServe(":4000", m); err != nil {
		klog.Fatalln(err)
	}
}

func GetPackageFile(ctx httpw.ResponseWriter, params v1alpha1.ChartRepoRef) {
	out, ct, err := LoadFile(params.URL, params.Name, params.Version, ctx.R().Params("*"), ctx.R().QueryTrim("format"))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			ctx.WriteHeader(http.StatusNotFound)
			return
		}

		ctx.APIError(err)
		// ctx.Error(http.StatusInternalServerError, "ConvertFormat", err.Error())
		return
	}

	e := fmt.Sprintf("%x", md5.Sum(out))

	var age int
	if _, err := semver.NewVersion(params.Version); err == nil {
		age = 24 * 60 * 60 // 24 hours
	} else {
		age = 10 * 365 * 24 * 60 * 60 // 10 yrs
	}

	ctx.Header().Set("Content-Type", ct)
	ctx.Header().Set("Etag", e)
	ctx.Header().Set("Cache-Control", fmt.Sprintf("private, must-revalidate, max-age=%d", age))

	if match := ctx.R().Request().Header.Get("If-None-Match"); match != "" {
		if match == e {
			ctx.WriteHeader(http.StatusNotModified)
			return
		}
	}
	_, _ = ctx.Write(out)
}

func LoadFile(chartURL, chartName, chartVersion, filename, format string) ([]byte, string, error) {
	chrt, err := HelmRegistry.GetChart(chartURL, chartName, chartVersion)
	if err != nil {
		return nil, "", err
	}

	for _, f := range chrt.Raw {
		if f.Name == filename {
			return converter.Convert(f.Name, f.Data, meta_util.NewDataFormat(format, meta_util.KeepFormat))
		}
	}
	return nil, "", apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "Chart"}, filename)
}

func GetValuesFile(ctx httpw.ResponseWriter, params chartsapi.ChartPresetRef) {
	cfg, err := config.GetConfig()
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return
	}
	kc, err := actionx.NewUncachedClientForConfig(cfg)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, err.Error())
		return
	}

	chrt, err := HelmRegistry.GetChart(params.URL, params.Name, params.Version)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "GetChart", err.Error())
		return
	}

	vals, err := values.MergePresetValues(kc, chrt.Chart, params)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "MergePresetValues", err.Error())
		return
	}

	var data []byte
	var ct string
	format := meta_util.NewDataFormat(ctx.R().QueryTrim("format"), meta_util.KeepFormat)
	if format == meta_util.JsonFormat {
		ct = "application/json"
		data, err = json.Marshal(vals)
	} else if format == meta_util.YAMLFormat {
		ct = "text/yaml"
		data, err = yaml.Marshal(vals)
	}
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "ConvertFormat", err.Error())
		return
	}

	ctx.Header().Set("Content-Type", ct)
	_, _ = ctx.Write(data)
}

func ListPackageFiles(ctx httpw.ResponseWriter, params v1alpha1.ChartRepoRef) {
	// TODO: verify params

	chrt, err := HelmRegistry.GetChart(params.URL, params.Name, params.Version)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "GetChart", err.Error())
		return
	}

	files := make([]string, 0, len(chrt.Raw))
	for _, f := range chrt.Raw {
		files = append(files, f.Name)
	}
	sort.Strings(files)

	ctx.JSON(http.StatusOK, files)
}

func GetPackageViewForChart(ctx httpw.ResponseWriter, params v1alpha1.ChartRepoRef) {
	// TODO: verify params

	chrt, err := HelmRegistry.GetChart(params.URL, params.Name, params.Version)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "GetChart", err.Error())
		return
	}

	pv, err := lib.CreatePackageView(params.URL, chrt.Chart)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "CreatePackageView", err.Error())
		return
	}

	ctx.JSON(http.StatusOK, pv)
}
