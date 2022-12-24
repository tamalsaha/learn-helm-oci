package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fluxcd/pkg/oci"
	"github.com/fluxcd/pkg/oci/auth/login"
	"github.com/fluxcd/source-controller/api/v1beta2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/tamalsaha/learn-helm-oci/internal/helm/getter"
	"github.com/tamalsaha/learn-helm-oci/internal/helm/registry"
	"github.com/tamalsaha/learn-helm-oci/internal/helm/repository"
	"helm.sh/helm/v3/pkg/chart/loader"
	helmgetter "helm.sh/helm/v3/pkg/getter"
	helmreg "helm.sh/helm/v3/pkg/registry"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

func main() {
	if err := useKubebuilderClient(); err != nil {
		panic(err)
	}
}

func NewClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1beta2.AddToScheme(scheme)

	ctrl.SetLogger(klogr.New())
	cfg := ctrl.GetConfigOrDie()
	cfg.QPS = 100
	cfg.Burst = 100

	mapper, err := apiutil.NewDynamicRESTMapper(cfg)
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

var getters = helmgetter.Providers{
	helmgetter.Provider{
		Schemes: []string{"http", "https"},
		New:     helmgetter.NewHTTPGetter,
	},
	helmgetter.Provider{
		Schemes: []string{"oci"},
		New:     helmgetter.NewOCIGetter,
	},
}

func useKubebuilderClient() error {
	ctx := context.TODO()

	fmt.Println("Using kubebuilder client")
	kc, err := NewClient()
	if err != nil {
		return err
	}

	var repo v1beta2.HelmRepository
	err = kc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "podinfo"}, &repo)
	if err != nil {
		return err
	}

	var (
		tlsConfig     *tls.Config
		authenticator authn.Authenticator
		keychain      authn.Keychain
	)
	// Used to login with the repository declared provider
	ctxTimeout, cancel := context.WithTimeout(ctx, repo.Spec.Timeout.Duration)
	defer cancel()

	normalizedURL := repository.NormalizeURL(repo.Spec.URL)
	err = repository.ValidateDepURL(normalizedURL)
	if err != nil {
		return err
	}
	// Construct the Getter options from the HelmRepository data
	clientOpts := []helmgetter.Option{
		helmgetter.WithURL(normalizedURL),
		helmgetter.WithTimeout(repo.Spec.Timeout.Duration),
		helmgetter.WithPassCredentialsAll(repo.Spec.PassCredentials),
	}
	if secret, err := getHelmRepositorySecret(ctx, kc, &repo); secret != nil || err != nil {
		if err != nil {
			return fmt.Errorf("failed to get secret '%s': %w", repo.Spec.SecretRef.Name, err)
		}

		// Build client options from secret
		opts, tls, err := clientOptionsFromSecret(secret, normalizedURL)
		if err != nil {
			return err
		}
		clientOpts = append(clientOpts, opts...)
		tlsConfig = tls

		// Build registryClient options from secret
		keychain, err = registry.LoginOptionFromSecret(normalizedURL, *secret)
		if err != nil {
			return fmt.Errorf("failed to configure Helm client with secret data: %w", err)
		}
	} else if repo.Spec.Provider != sourcev1.GenericOCIProvider && repo.Spec.Type == sourcev1.HelmRepositoryTypeOCI {
		auth, authErr := oidcAuth(ctxTimeout, repo.Spec.URL, repo.Spec.Provider)
		if authErr != nil && !errors.Is(authErr, oci.ErrUnconfiguredProvider) {
			return fmt.Errorf("failed to get credential from %s: %w", repo.Spec.Provider, authErr)
		}
		if auth != nil {
			authenticator = auth
		}
	}

	loginOpt, err := makeLoginOption(authenticator, keychain, normalizedURL)
	if err != nil {
		return err
	}

	// Initialize the chart repository
	var chartRepo repository.Downloader
	switch repo.Spec.Type {
	case sourcev1.HelmRepositoryTypeOCI:
		if !helmreg.IsOCI(normalizedURL) {
			return fmt.Errorf("invalid OCI registry URL: %s", normalizedURL)
		}

		// with this function call, we create a temporary file to store the credentials if needed.
		// this is needed because otherwise the credentials are stored in ~/.docker/config.json.
		// TODO@souleb: remove this once the registry move to Oras v2
		// or rework to enable reusing credentials to avoid the unneccessary handshake operations
		registryClient, credentialsFile, err := registry.ClientGenerator(loginOpt != nil)
		if err != nil {
			return fmt.Errorf("failed to construct Helm client: %w", err)
		}

		if credentialsFile != "" {
			defer func() {
				if err := os.Remove(credentialsFile); err != nil {
					klog.Warningf("failed to delete temporary credentials file: %s", err)
				}
			}()
		}

		/*
			// TODO(tamal): SKIP verifier

			var verifiers []soci.Verifier
			if obj.Spec.Verify != nil {
				provider := obj.Spec.Verify.Provider
				verifiers, err = r.makeVerifiers(ctx, obj, authenticator, keychain)
				if err != nil {
					if obj.Spec.Verify.SecretRef == nil {
						provider = fmt.Sprintf("%s keyless", provider)
					}
					e := &serror.Event{
						Err:    fmt.Errorf("failed to verify the signature using provider '%s': %w", provider, err),
						Reason: sourcev1.VerificationError,
					}
					conditions.MarkFalse(obj, sourcev1.SourceVerifiedCondition, e.Reason, e.Err.Error())
					return sreconcile.ResultEmpty, e
				}
			}
		*/

		// Tell the chart repository to use the OCI client with the configured getter
		clientOpts = append(clientOpts, helmgetter.WithRegistryClient(registryClient))
		ociChartRepo, err := repository.NewOCIChartRepository(normalizedURL,
			repository.WithOCIGetter(getters),
			repository.WithOCIGetterOptions(clientOpts),
			repository.WithOCIRegistryClient(registryClient),
			// repository.WithVerifiers(verifiers),
		)
		if err != nil {
			return err
		}
		chartRepo = ociChartRepo

		// If login options are configured, use them to login to the registry
		// The OCIGetter will later retrieve the stored credentials to pull the chart
		if loginOpt != nil {
			err = ociChartRepo.Login(loginOpt)
			if err != nil {
				return fmt.Errorf("failed to login to OCI registry: %w", err)
			}
			defer ociChartRepo.Logout()
		}
	default:
		fmt.Println(tlsConfig) // keep go compiler happy
		return fmt.Errorf("UNHANDLED_CASE_____ old repo format")
		/*
			httpChartRepo, err := repository.NewChartRepository(normalizedURL, r.Storage.LocalPath(*repo.GetArtifact()), r.Getters, tlsConfig, clientOpts,
				repository.WithMemoryCache(r.Storage.LocalPath(*repo.GetArtifact()), r.Cache, r.TTL, func(event string) {
					r.IncCacheEvents(event, obj.Name, obj.Namespace)
				}))
			if err != nil {
				return chartRepoConfigErrorReturn(err, obj)
			}
			chartRepo = httpChartRepo
			defer func() {
				if httpChartRepo == nil {
					return
				}
				// Cache the index if it was successfully retrieved
				// and the chart was successfully built
				if r.Cache != nil && httpChartRepo.Index != nil {
					// The cache key have to be safe in multi-tenancy environments,
					// as otherwise it could be used as a vector to bypass the helm repository's authentication.
					// Using r.Storage.LocalPath(*repo.GetArtifact() is safe as the path is in the format /<helm-repository-name>/<chart-name>/<filename>.
					err := httpChartRepo.CacheIndexInMemory()
					if err != nil {
						r.eventLogf(ctx, obj, eventv1.EventTypeTrace, sourcev1.CacheOperationFailedReason, "failed to cache index: %s", err)
					}
				}

				// Delete the index reference
				if httpChartRepo.Index != nil {
					httpChartRepo.Unload()
				}
			}()
		*/
	}

	// conditions.Delete(obj, meta.StalledCondition)

	// https://github.com/fluxcd/source-controller/blob/04d87b61ca76e8081869cf3f9937bc178195f876/controllers/helmchart_controller.go#L467

	// /Users/tamal/go/src/github.com/fluxcd/source-controller/internal/helm/chart/builder_remote.go

	remote := chartRepo
	remoteRef := RemoteReference{
		// Name:    "oci://registry-1.docker.io/tigerworks/hello-oci",
		Name:    "hello-oci",
		Version: "0.1.0",
	}

	// Get the current version for the RemoteReference
	cv, err := remote.GetChartVersion(remoteRef.Name, remoteRef.Version)
	if err != nil {
		err = fmt.Errorf("failed to get chart version for remote reference: %w", err)
		return err
	}

	//// Verify the chart if necessary
	//if opts.Verify {
	//	if err := remote.VerifyChart(ctx, cv); err != nil {
	//		return nil, nil, &BuildError{Reason: ErrChartVerification, Err: err}
	//	}
	//}
	//
	//result, shouldReturn, err := generateBuildResult(cv, opts)
	//if err != nil {
	//	return nil, nil, err
	//}
	//
	//if shouldReturn {
	//	return nil, result, nil
	//}

	// Download the package for the resolved version
	res, err := remote.DownloadChart(cv)
	if err != nil {
		err = fmt.Errorf("failed to download chart for remote reference: %w", err)
		return err
	}

	chrt, err := loader.LoadArchive(res)
	if err != nil {
		return err
	}
	fmt.Println(chrt.Metadata.Name)

	return nil
}

func getHelmRepositorySecret(ctx context.Context, client client.Client, repository *sourcev1.HelmRepository) (*corev1.Secret, error) {
	if repository.Spec.SecretRef == nil {
		return nil, nil
	}
	name := types.NamespacedName{
		Namespace: repository.GetNamespace(),
		Name:      repository.Spec.SecretRef.Name,
	}
	var secret corev1.Secret
	err := client.Get(ctx, name, &secret)
	if err != nil {
		return nil, err
	}
	return &secret, nil
}

func clientOptionsFromSecret(secret *corev1.Secret, normalizedURL string) ([]helmgetter.Option, *tls.Config, error) {
	opts, err := getter.ClientOptionsFromSecret(*secret)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to configure Helm client with secret data: %w", err)
	}

	tlsConfig, err := getter.TLSClientConfigFromSecret(*secret, normalizedURL)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create TLS client config with secret data: %w", err)
	}

	return opts, tlsConfig, nil
}

// RemoteReference contains sufficient information to look up a chart in
// a ChartRepository.
type RemoteReference struct {
	// Name of the chart.
	Name string
	// Version of the chart.
	// Can be a Semver range, or empty for latest.
	Version string
}

// makeLoginOption returns a registry login option for the given HelmRepository.
// If the HelmRepository does not specify a secretRef, a nil login option is returned.
func makeLoginOption(auth authn.Authenticator, keychain authn.Keychain, registryURL string) (helmreg.LoginOption, error) {
	if auth != nil {
		return registry.AuthAdaptHelper(auth)
	}

	if keychain != nil {
		return registry.KeychainAdaptHelper(keychain)(registryURL)
	}

	return nil, nil
}

// oidcAuth generates the OIDC credential authenticator based on the specified cloud provider.
func oidcAuth(ctx context.Context, url, provider string) (authn.Authenticator, error) {
	u := strings.TrimPrefix(url, sourcev1.OCIRepositoryPrefix)
	ref, err := name.ParseReference(u)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL '%s': %w", u, err)
	}

	opts := login.ProviderOptions{}
	switch provider {
	case sourcev1.AmazonOCIProvider:
		opts.AwsAutoLogin = true
	case sourcev1.AzureOCIProvider:
		opts.AzureAutoLogin = true
	case sourcev1.GoogleOCIProvider:
		opts.GcpAutoLogin = true
	}

	return login.NewManager().Login(ctx, u, ref, opts)
}
