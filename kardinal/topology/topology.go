package topology

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	net "k8s.io/api/networking/v1"
)

type ClusterTopology struct {
	FlowID              string              `json:"flowID"`
	Ingress             *Ingress            `json:"ingress"`
	Services            []*Service          `json:"services"`
	ServiceDependencies []ServiceDependency `json:"serviceDependencies"`
	Namespace           string              `json:"namespace"`
}

type Service struct {
	ServiceID               string                 `json:"serviceID"`
	Version                 string                 `json:"version"`
	ServiceSpec             *corev1.ServiceSpec    `json:"serviceSpec"`
	DeploymentSpec          *appsv1.DeploymentSpec `json:"deploymentSpec"`
	IsExternal              bool                   `json:"isExternal"`
	IsStateful              bool                   `json:"isStateful"`
	IsShared                bool                   `json:"isShared"`
	OriginalVersionIfShared string                 `json:"originalVersionIfShared"`
}

type ServiceDependency struct {
	Service          *Service            `json:"service"`
	DependsOnService *Service            `json:"dependsOnService"`
	DependencyPort   *corev1.ServicePort `json:"dependencyPort"`
}

type Ingress struct {
	ActiveFlowIDs []string      `json:"activeFlowIDs"`
	Ingresses     []net.Ingress `json:"ingresses"`
}
