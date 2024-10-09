package resources

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

func IsManaged(objectMeta *metav1.ObjectMeta) bool {
	kardinalManagedLabelValue, found := objectMeta.Labels[kardinalManagedLabelKey]
	if found && kardinalManagedLabelValue == "true" {
		return true
	}
	return false
}
