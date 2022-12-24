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


- https://fluxcd.io/flux/faq/#can-i-use-flux-helmreleases-without-gitops

### Install FluxCD CRDs

```
k apply -f https://github.com/fluxcd/source-controller/raw/v0.30.1/config/crd/bases/source.toolkit.fluxcd.io_helmrepositories.yaml
k apply -f https://github.com/fluxcd/helm-operator/raw/v1.4.4/deploy/crds.yaml
```

```
flux install \
--namespace=flux-system \
--network-policy=false \
--components=source-controller,helm-controller

# docker login --username ${USERNAME} --password ${DOCKER_TOKEN}

kubectl create secret docker-registry regcred \
 --docker-server=registry-1.docker.io \
 --docker-username=tigerworks \
 --docker-password=$DOCKERHUB_TOKEN

> k apply -f oci-reg.yaml
> k get helmrepository -A
NAMESPACE   NAME      URL                                     AGE     READY   STATUS
default     podinfo   oci://registry-1.docker.io/tigerworks   8m32s   True    Helm repository is ready

> k apply -f oci-release.yaml
> k get helmreleases -A
NAMESPACE   NAME         AGE     READY   STATUS
default     my-release   3m11s   True    Release reconciliation succeeded
```