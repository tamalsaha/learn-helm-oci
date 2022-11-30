package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/oci"
	"github.com/fluxcd/pkg/oci/auth/login"
	"github.com/fluxcd/source-controller/api/v1beta2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/tamalsaha/learn-helm-oci/internal/helm/registry"
	"github.com/tamalsaha/learn-helm-oci/internal/helm/repository"
	helmreg "helm.sh/helm/v3/pkg/registry"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
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

func useKubebuilderClient() error {
	var (
		authenticator authn.Authenticator
		keychain      authn.Keychain
		err           error
	)
	ctx := context.TODO()

	fmt.Println("Using kubebuilder client")
	kc, err := NewClient()
	if err != nil {
		return err
	}

	var obj v1beta2.HelmRepository
	err = kc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "podinfo"}, &obj)
	if err != nil {
		return err
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, obj.Spec.Timeout.Duration)
	defer cancel()

	// Configure any authentication related options.
	if obj.Spec.SecretRef != nil {
		keychain, err = authFromSecret(ctx, kc, &obj)
		if err != nil {
			// conditions.MarkFalse(obj, meta.ReadyCondition, sourcev1.AuthenticationFailedReason, err.Error())
			// result, retErr = ctrl.Result{}, err
			return err
		}
	} else if obj.Spec.Provider != sourcev1.GenericOCIProvider && obj.Spec.Type == sourcev1.HelmRepositoryTypeOCI {
		auth, authErr := oidcAuth(ctxTimeout, obj.Spec.URL, obj.Spec.Provider)
		if authErr != nil && !errors.Is(authErr, oci.ErrUnconfiguredProvider) {
			e := fmt.Errorf("failed to get credential from %s: %w", obj.Spec.Provider, authErr)
			// conditions.MarkFalse(obj, meta.ReadyCondition, sourcev1.AuthenticationFailedReason, e.Error())
			// result, retErr = ctrl.Result{}, e
			return e
		}
		if auth != nil {
			authenticator = auth
		}
	}

	loginOpt, err := makeLoginOption(authenticator, keychain, obj.Spec.URL)
	if err != nil {
		// conditions.MarkFalse(obj, meta.ReadyCondition, sourcev1.AuthenticationFailedReason, err.Error())
		// result, retErr = ctrl.Result{}, err
		return err
	}

	// Create registry client and login if needed.
	registryClient, file, err := registry.ClientGenerator(loginOpt != nil)
	if err != nil {
		e := fmt.Errorf("failed to create registry client: %w", err)
		// conditions.MarkFalse(obj, meta.ReadyCondition, meta.FailedReason, e.Error())
		// result, retErr = ctrl.Result{}, e
		return e
	}
	if file != "" {
		defer func() {
			if err := os.Remove(file); err != nil {
				eventLogf(ctx, &obj, corev1.EventTypeWarning, meta.FailedReason,
					"failed to delete temporary credentials file: %s", err)
			}
		}()
	}

	chartRepo, err := repository.NewOCIChartRepository(obj.Spec.URL, repository.WithOCIRegistryClient(registryClient))
	if err != nil {
		e := fmt.Errorf("failed to parse URL '%s': %w", obj.Spec.URL, err)
		// conditions.MarkStalled(obj, sourcev1.URLInvalidReason, e.Error())
		// conditions.MarkFalse(obj, meta.ReadyCondition, sourcev1.URLInvalidReason, e.Error())
		// result, retErr = ctrl.Result{}, nil
		return e
	}
	// conditions.Delete(obj, meta.StalledCondition)

	// Attempt to login to the registry if credentials are provided.
	if loginOpt != nil {
		err = chartRepo.Login(loginOpt)
		if err != nil {
			e := fmt.Errorf("failed to login to registry '%s': %w", obj.Spec.URL, err)
			// conditions.MarkFalse(obj, meta.ReadyCondition, sourcev1.AuthenticationFailedReason, e.Error())
			// result, retErr = ctrl.Result{}, e
			return e
		}
	}

	return nil
}

// authFromSecret returns an authn.Keychain for the given HelmRepository.
// If the HelmRepository does not specify a secretRef, an anonymous keychain is returned.
func authFromSecret(ctx context.Context, client client.Client, obj *sourcev1.HelmRepository) (authn.Keychain, error) {
	// Attempt to retrieve secret.
	name := types.NamespacedName{
		Namespace: obj.GetNamespace(),
		Name:      obj.Spec.SecretRef.Name,
	}
	var secret corev1.Secret
	if err := client.Get(ctx, name, &secret); err != nil {
		return nil, fmt.Errorf("failed to get secret '%s': %w", name.String(), err)
	}

	// Construct login options.
	keychain, err := registry.LoginOptionFromSecret(obj.Spec.URL, secret)
	if err != nil {
		return nil, fmt.Errorf("failed to configure Helm client with secret data: %w", err)
	}
	return keychain, nil
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

func eventLogf(ctx context.Context, obj runtime.Object, eventType string, reason string, messageFmt string, args ...interface{}) {
	msg := fmt.Sprintf(messageFmt, args...)
	// Log and emit event.
	if eventType == corev1.EventTypeWarning {
		ctrl.LoggerFrom(ctx).Error(errors.New(reason), msg)
	} else {
		ctrl.LoggerFrom(ctx).Info(msg)
	}
	// r.Eventf(obj, eventType, reason, msg)
}
