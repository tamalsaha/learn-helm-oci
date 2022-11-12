# learn-helm-oci

```
> helm create charts/hello-oci
> helm package charts/hello-oci
> helm push hello-oci-0.1.0.tgz oci://index.docker.io/tigerworks
Pushed: index.docker.io/tigerworks/hello-oci:0.1.0
Digest: sha256:3ecff51681ff961501afa3c554a5d7a22fe12097008af75adbdf48568309a441
```
