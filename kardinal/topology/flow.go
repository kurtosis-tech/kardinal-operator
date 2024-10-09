package topology

import (
	appsv1 "k8s.io/api/apps/v1"
)

type FlowPatch struct {
	FlowId         string
	ServicePatches []*ServicePatch
}

type ServicePatch struct {
	Namespace      string
	Service        string
	DeploymentSpec *appsv1.DeploymentSpec
}
