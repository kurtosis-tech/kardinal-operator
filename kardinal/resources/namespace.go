package resources

import (
	corev1 "k8s.io/api/core/v1"
)

type Namespace struct {
	Name     string
	Services map[string]*corev1.Service `json:"services"`
}
