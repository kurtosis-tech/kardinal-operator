package webhooks

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:webhook:path=/mutate--v1-service,mutating=true,failurePolicy=fail,sideEffects=NoneOnDryRun,groups="",resources=services,verbs=create;update,versions=v1,name=istio-labels-defaulter.core.kardinal.dev,admissionReviewVersions=v1

// IstioLabelsDefaulter injects the `app` and `version` labels on baseline flow services
type IstioLabelsDefaulter struct{}

func (injector *IstioLabelsDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	log := logf.FromContext(ctx)
	service, ok := obj.(*corev1.Service)
	if !ok {
		return fmt.Errorf("expected a Service but got a %T", obj)
	}

	if service.Labels == nil {
		service.Labels = map[string]string{}
	}
	service.Labels["leo-juega"] = "claro-que-si-pa"
	log.Info("Istio labels injection complete")

	return nil
}
