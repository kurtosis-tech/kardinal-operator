package resources

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kardinalcorev1 "kardinal.dev/kardinal-operator/api/core/v1"
)

type Namespace struct {
	Name        string
	Services    *corev1.ServiceList      `json:"services"`
	Deployments *appsv1.DeploymentList   `json:"deployments"`
	Flows       *kardinalcorev1.FlowList `json:"flows"`
}
