package resources

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kardinalcorev1 "kardinal.dev/kardinal-operator/api/core/v1"
)

type Namespace struct {
	Name        string
	Services    []*corev1.Service      `json:"services"`
	Deployments []*appsv1.Deployment   `json:"deployments"`
	Flows       []*kardinalcorev1.Flow `json:"flows"`
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
