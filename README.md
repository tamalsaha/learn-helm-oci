# learn-helm-oci

```
> helm create charts/hello-oci
> helm package charts/hello-oci
> helm push hello-oci-0.1.0.tgz oci://index.docker.io/tigerworks
Pushed: index.docker.io/tigerworks/hello-oci:0.1.0
Digest: sha256:3ecff51681ff961501afa3c554a5d7a22fe12097008af75adbdf48568309a441
```

```
helm template my-release oci://registry-1.docker.io/tigerworks/hello-oci --version 0.1.0
```

```
helm template my charts/hello-oci \
  --values=./charts/hello-oci/one.yaml \
  --values=./charts/hello-oci/two.yaml
```

```
helm template my charts/hello-oci \
  --values=./charts/hello-oci/two.yaml \
  --values=./charts/hello-oci/one.yaml
```

## FluxCD

- https://fluxcd.io/flux/components/source/helmrepositories/#helm-oci-repository
