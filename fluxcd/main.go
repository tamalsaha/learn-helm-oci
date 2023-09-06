package main

import (
	"bytes"
	"fmt"
	"io"
	"kubepack.dev/lib-helm/pkg/repo"
	"kubepack.dev/lib-helm/pkg/values"
	"net/url"
	"path"
	"strings"

	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/s3blob"
	"helm.sh/helm/v3/pkg/registry"
	releasesapi "x-helm.dev/apimachinery/apis/releases/v1alpha1"
)

type FluxCDCommandPrinter struct {
	Registry      repo.IRegistry
	ChartRef      releasesapi.ChartRef
	Version       string
	ReleaseName   string
	Namespace     string
	Values        values.Options
	UseValuesFile bool

	W          io.Writer
	valuesFile []byte
}

const indent = "  "

func (x *FluxCDCommandPrinter) Do() error {
	chrt, err := x.Registry.GetChart(releasesapi.ChartSourceRef{
		Name:      x.ChartRef.Name,
		Version:   x.Version,
		SourceRef: x.ChartRef.SourceRef,
	})
	if err != nil {
		return err
	}

	repoURL := x.ChartRef.SourceRef.Name
	switch x.ChartRef.SourceRef.Kind {
	case releasesapi.SourceKindHelmRepository:
		helmRepo, err := x.Registry.GetHelmRepository(releasesapi.ChartSourceRef{
			Name:      x.ChartRef.Name,
			Version:   x.Version,
			SourceRef: x.ChartRef.SourceRef,
		})
		if err != nil {
			return err
		}
		repoURL = helmRepo.Spec.URL
	}

	/*
		$ helm repo add appscode https://charts.appscode.com/stable/
		$ helm repo update
		$ helm search repo appscode/voyager --version v12.0.0-rc.1
	*/

	var buf bytes.Buffer
	if !registry.IsOCI(repoURL) {
		/*
			$ helm upgrade --install voyager-operator appscode/voyager --version v12.0.0-rc.1 \
			  --namespace kube-system \
			  --set cloudProvider=$provider
		*/
		_, err = fmt.Fprintf(&buf, "helm upgrade --install %s %s \\\n", x.ReleaseName, x.ChartRef.Name)
		if err != nil {
			return err
		}

		if x.Version != "" {
			_, err = fmt.Fprintf(&buf, "%s--repo %s --version %s \\\n", indent, repoURL, x.Version)
			if err != nil {
				return err
			}
		} else {
			_, err = fmt.Fprintf(&buf, "%s--repo %s \\\n", indent, repoURL)
			if err != nil {
				return err
			}
		}
	} else {
		u, err := url.Parse(repoURL)
		if err != nil {
			return err
		}
		u.Path = path.Join(u.Path, x.ChartRef.Name)
		u.User = nil
		repoURL = u.String()

		if x.Version != "" {
			_, err = fmt.Fprintf(&buf, "helm upgrade --install %s %s --version %s \\\n", x.ReleaseName, repoURL, x.Version)
			if err != nil {
				return err
			}
		} else {
			_, err = fmt.Fprintf(&buf, "helm upgrade --install %s %s \\\n", x.ReleaseName, repoURL)
			if err != nil {
				return err
			}
		}
	}

	if x.Namespace != "" {
		_, err = fmt.Fprintf(&buf, "%s--namespace %s --create-namespace \\\n", indent, x.Namespace)
		if err != nil {
			return err
		}
	}

	_, err = fmt.Fprintf(&buf, "%s--wait --debug --burst-limit=1000 \\\n", indent)
	if err != nil {
		return err
	}

	modified, err := x.Values.MergeValues(chrt.Chart)
	if err != nil {
		return err
	}
	if x.UseValuesFile {
		x.valuesFile, err = values.GetValuesDiffYAML(chrt.Values, modified)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(&buf, "%s--values=values.yaml", indent)
		if err != nil {
			return err
		}
	} else {
		setValues, err := values.GetChangedValues(chrt.Values, modified)
		if err != nil {
			return err
		}
		for _, v := range setValues {
			// xref: https://github.com/kubepack/lib-helm/issues/72
			if strings.ContainsRune(v, '\n') {
				idx := strings.IndexRune(v, '=')
				return fmt.Errorf(`found \n is values for %s`, v[:idx])
			}
			_, err = fmt.Fprintf(&buf, "%s--set %s \\\n", indent, v)
			if err != nil {
				return err
			}
		}
		buf.Truncate(buf.Len() - 3)
	}

	_, err = buf.WriteRune('\n')
	if err != nil {
		return err
	}

	_, err = buf.WriteTo(x.W)
	return err
}

func (x *FluxCDCommandPrinter) ValuesFile() []byte {
	return x.valuesFile
}

func main() {

}
