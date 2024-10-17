package resources

import (
	istioclient "istio.io/client-go/pkg/apis/networking/v1alpha3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	net "k8s.io/api/networking/v1"
	kardinalcorev1 "kardinal.dev/kardinal-operator/api/core/v1"
)

type Namespace struct {
	Name             string
	Services         []*corev1.Service              `json:"services"`
	Deployments      []*appsv1.Deployment           `json:"deployments"`
	Ingresses        []*net.Ingress                 `json:"ingresses"`
	VirtualServices  []*istioclient.VirtualService  `json:"virtualServices"`
	DestinationRules []*istioclient.DestinationRule `json:"destinationRules"`
	EnvoyFilters     []*istioclient.EnvoyFilter     `json:"envoy_filters"`
	Flows            []*kardinalcorev1.Flow         `json:"flows"`
}

func (namespace *Namespace) GetService(name string) *corev1.Service {
	for _, service := range namespace.Services {
		if service.Name == name {
			return service
		}
	}

	return nil
}

func (namespace *Namespace) GetDeployment(name string) *appsv1.Deployment {
	for _, deployment := range namespace.Deployments {
		if deployment.Name == name {
			return deployment
		}
	}

	return nil
}

func (namespace *Namespace) GetVirtualService(name string) *istioclient.VirtualService {
	for _, virtualService := range namespace.VirtualServices {
		if virtualService.Name == name {
			return virtualService
		}
	}

	return nil
}

func (namespace *Namespace) GetDestinationRule(name string) *istioclient.DestinationRule {
	for _, destinationRule := range namespace.DestinationRules {
		if destinationRule.Name == name {
			return destinationRule
		}
	}

	return nil
}

func (namespace *Namespace) GetEnvoyFilter(name string) *istioclient.EnvoyFilter {
	for _, filter := range namespace.EnvoyFilters {
		if filter.Name == name {
			return filter
		}
	}

	return nil
}

func (namespace *Namespace) GetIngress(name string) *net.Ingress {
	for _, ingress := range namespace.Ingresses {
		if ingress.Name == name {
			return ingress
		}
	}

	return nil
}
