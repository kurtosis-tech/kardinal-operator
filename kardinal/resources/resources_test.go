package resources

import (
	"github.com/stretchr/testify/require"
	"testing"
)

type labeledResourcesForTest struct {
	labels map[string]string
	name   string
}

func newLabeledResourcesForTest(labels map[string]string, name string) *labeledResourcesForTest {
	return &labeledResourcesForTest{labels: labels, name: name}
}

func (l *labeledResourcesForTest) GetLabels() map[string]string {
	return l.labels
}

func (l *labeledResourcesForTest) SetLabels(labels map[string]string) {
	l.labels = labels
}

func (l *labeledResourcesForTest) GetName() string {
	return l.name
}

func TestIstioLabelsEnsurer(t *testing.T) {

	withoutAnyIstioLabel := map[string]string{}
	labeledResourceWithoutAnyIstioLabel := newLabeledResourcesForTest(withoutAnyIstioLabel, "my-service")
	expectedLabels := map[string]string{
		"app":     "my-service",
		"version": "baseline",
	}

	expectedBool := ensureIstioLabelsForResource(labeledResourceWithoutAnyIstioLabel)
	require.True(t, expectedBool)
	require.Equal(t, expectedLabels, labeledResourceWithoutAnyIstioLabel.GetLabels())

	withK8sAppLabel := map[string]string{
		"app.kubernetes.io/name": "my-service-using-k8s-app",
	}
	labeledResourceWithK8sAppLabel := newLabeledResourcesForTest(withK8sAppLabel, "my-service")
	expectedLabels = map[string]string{
		"app.kubernetes.io/name": "my-service-using-k8s-app",
		"app":                    "my-service-using-k8s-app",
		"version":                "baseline",
	}
	expectedBool = ensureIstioLabelsForResource(labeledResourceWithK8sAppLabel)
	require.True(t, expectedBool)
	require.Equal(t, expectedLabels, labeledResourceWithK8sAppLabel.GetLabels())

	withVersionLabel := map[string]string{
		"version": "v0.2.1",
	}
	labeledResourceWithVersionLabel := newLabeledResourcesForTest(withVersionLabel, "my-versioned-service")
	expectedLabels = map[string]string{
		"app":     "my-versioned-service",
		"version": "v0.2.1",
	}

	expectedBool = ensureIstioLabelsForResource(labeledResourceWithVersionLabel)
	require.True(t, expectedBool)
	require.Equal(t, expectedLabels, labeledResourceWithVersionLabel.GetLabels())

	withVersionAndAppLabel := map[string]string{
		"app":     "my-app",
		"version": "v0.2.1",
	}
	labeledResourceWithVersionAndAppLabel := newLabeledResourcesForTest(withVersionAndAppLabel, "my-app-service")
	expectedLabels = map[string]string{
		"app":     "my-app",
		"version": "v0.2.1",
	}

	expectedBool = ensureIstioLabelsForResource(labeledResourceWithVersionAndAppLabel)
	require.False(t, expectedBool)
	require.Equal(t, expectedLabels, labeledResourceWithVersionAndAppLabel.GetLabels())
}
