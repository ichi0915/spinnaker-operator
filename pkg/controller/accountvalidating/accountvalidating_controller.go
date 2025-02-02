package accountvalidating

import (
	"context"
	"fmt"
	"net/http"

	"github.com/armory/spinnaker-operator/pkg/accounts"
	"github.com/armory/spinnaker-operator/pkg/apis/spinnaker/interfaces"
	"github.com/armory/spinnaker-operator/pkg/controller/webhook"
	"github.com/armory/spinnaker-operator/pkg/secrets"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/inject"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/validate-v1-spinnakerservice,mutating=false,failurePolicy=fail,groups="",resources=pods,verbs=create;update,versions=v1,name=vpod.kb.io,admissionReviewVersions=v1,sideEffects=none

var TypesFactory interfaces.TypesFactory

// spinnakerValidatingController performs preflight checks
type accountValidatingController struct {
	client     client.Client
	restConfig *rest.Config
	decoder    *admission.Decoder
}

// Implement all intended interfaces.
var _ admission.Handler = &accountValidatingController{}
var _ inject.Config = &accountValidatingController{}
var _ inject.Client = &accountValidatingController{}
var _ admission.DecoderInjector = &accountValidatingController{}
var log = logf.Log.WithName("accountvalidate")

// Add adds the validating admission controller
func Add(m manager.Manager) error {
	gvk, err := apiutil.GVKForObject(TypesFactory.NewAccount(), m.GetScheme())
	if err != nil {
		return err
	}
	webhook.Register(gvk, "spinnakeraccounts", &accountValidatingController{})
	return nil
}

// Handle is the entry point for spinnaker preflight validations
func (v *accountValidatingController) Handle(ctx context.Context, req admission.Request) admission.Response {
	log.Info(fmt.Sprintf("Handling admission request for: %s", req.AdmissionRequest.Kind.Kind))
	gv := TypesFactory.GetGroupVersion()
	acc := TypesFactory.NewAccount()

	if "SpinnakerAccount" == req.AdmissionRequest.Kind.Kind &&
		gv.Group == req.AdmissionRequest.Kind.Group &&
		gv.Version == req.AdmissionRequest.Kind.Version {

		if err := v.decoder.Decode(req, acc); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

		accType, err := accounts.GetType(acc.GetSpec().Type)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

		spinAccount, err := accType.FromCRD(acc)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

		av := spinAccount.NewValidator()
		ctx = secrets.NewContext(ctx, v.restConfig, acc.GetNamespace())
		defer secrets.Cleanup(ctx)

		if err := av.Validate(nil, v.client, ctx, log); err != nil {
			return admission.Errored(http.StatusUnprocessableEntity, err)
		}
	}
	return admission.ValidationResponse(true, "")
}

// InjectClient injects the client.
func (v *accountValidatingController) InjectClient(c client.Client) error {
	v.client = c
	return nil
}

// InjectDecoder injects the decoder.
func (v *accountValidatingController) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}

// InjectConfig injects the rest config for creating raw kubernetes clients.
func (v *accountValidatingController) InjectConfig(c *rest.Config) error {
	v.restConfig = c
	return nil
}
