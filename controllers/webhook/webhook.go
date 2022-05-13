package webhook

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/armory/spinnaker-operator/pkg/api/util"
	"github.com/operator-framework/operator-sdk/pkg/k8sutil"
	ar_v1 "k8s.io/api/admissionregistration/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	servicePort = 9876
)

var registrations = []registration{}

type registration struct {
	kind schema.GroupVersionKind
	h    admission.Handler
	p    string
	r    string
}

func Register(kind schema.GroupVersionKind, resources string, h admission.Handler) {
	registrations = append(registrations, registration{
		kind: kind,
		h:    h,
		p:    generateValidatePath(kind),
		r:    resources,
	})
}

func Start(m manager.Manager) error {
	if len(registrations) == 0 {
		return errors.New("no kind registered for validation")
	}

	ns, name, err := getOperatorNameAndNamespace()
	if err != nil {
		return err
	}

	// Create Kubernetes service for listening to requests from API server
	rawClient := kubernetes.NewForConfigOrDie(m.GetConfig())
	err = deployWebhookService(ns, name, servicePort, rawClient)
	if err != nil {
		return err
	}

	// Create or get certificates
	c, err := getCertContext(ns, name)
	if err != nil {
		return err
	}

	hookServer := m.GetWebhookServer()
	hookServer.CertDir = c.certDir
	hookServer.Port = servicePort

	for _, r := range registrations {
		hookServer.Register(r.p, &webhook.Admission{Handler: r.h})
	}
	// Create validating webhook configuration for registering our webhook with the API server
	return deployValidatingWebhookConfiguration(name, ns, rawClient, c.signingCert)
}

func getOperatorNameAndNamespace() (string, string, error) {
	name, err := k8sutil.GetOperatorName()
	if err != nil {
		return "", "", err
	}
	ns, err := k8sutil.GetOperatorNamespace()
	if err != nil {
		envNs := os.Getenv("ADMISSION_PROXY_NAMESPACE")
		if envNs == "" {
			return "", "", fmt.Errorf("unable to determine operator namespace. Error: %s and ADMISSION_PROXY_NAMESPACE env var not set", err.Error())
		}
		ns = envNs
	}
	return ns, name, nil
}

func generateValidatePath(gvk schema.GroupVersionKind) string {
	return "/validate-" + strings.Replace(gvk.Group, ".", "-", -1) + "-" +
		gvk.Version + "-" + strings.ToLower(gvk.Kind)
}

func deployWebhookService(ns string, name string, port int, rawClient *kubernetes.Clientset) error {
	selectorLabels := map[string]string{"name": name}
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			Labels:    selectorLabels,
		},
		Spec: v1.ServiceSpec{
			Selector: selectorLabels,
			Ports: []v1.ServicePort{
				{
					Name:       "http",
					Protocol:   "TCP",
					Port:       443,
					TargetPort: intstr.FromInt(port),
				},
			},
		},
	}
	return util.CreateOrUpdateService(service, rawClient)
}

func deployValidatingWebhookConfiguration(svcName, ns string, rawClient *kubernetes.Clientset, cert []byte) error {
	webhookConfig := &ar_v1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spinnakervalidatingwebhook",
			Namespace: ns,
		},
		// Webhooks: []ar_v1.ValidatingWebhook{},
	}

	for i := range registrations {
		r := registrations[i]
		webhookConfig.Webhooks = append(webhookConfig.Webhooks, ar_v1.ValidatingWebhook{
			Name: fmt.Sprintf("webhook-%s-%s.%s", r.r, r.kind.Version, strings.ToLower(r.kind.Group)),
			ClientConfig: ar_v1.WebhookClientConfig{
				Service: &ar_v1.ServiceReference{
					Namespace: ns,
					Name:      svcName,
					Path:      &r.p,
				},
				CABundle: cert,
			},
			Rules: []ar_v1.RuleWithOperations{{
				Operations: []ar_v1.OperationType{
					ar_v1.Create,
					ar_v1.Update,
				},
				Rule: ar_v1.Rule{
					APIGroups:   []string{r.kind.Group},
					APIVersions: []string{r.kind.Version},
					Resources:   []string{r.r}, // should be "spinnakerservices"
				},
			}},
			SideEffects: sideEffect(ar_v1.SideEffectClassNone),
			AdmissionReviewVersions: []string{
				"v1beta1",
				"v1",
			},
		})
	}
	return util.CreateOrUpdateValidatingWebhookConfiguration(webhookConfig, rawClient)
}

func sideEffect(sideEffect ar_v1.SideEffectClass) *ar_v1.SideEffectClass {
	s := new(ar_v1.SideEffectClass)
	*s = sideEffect
	return s
}
