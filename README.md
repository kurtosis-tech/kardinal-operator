# Kardinal Operator

Implementation of [Kardinal](https://github.com/kurtosis-tech/kardinal) as a K8S Operator.

## Development

Minikube + K8S manifest deployed.  K8S context set to your local cluster.

The following three commands are commonly used during development:

```
make lint (Run golangci linter. Can also be configured inside your IDE.)
make test (Run tests against local cluster)
make run (Run operator against your local cluster)
```

Manage custom resources with kubectl:

```yaml
apiVersion: core.kardinal.dev/v1
kind: Flow
metadata:
  labels:
    app.kubernetes.io/name: kardinal
    app.kubernetes.io/managed-by: kustomize
  name: flow-test
  namespace: baseline
spec:
  service: frontend
  image: kurtosistech/frontend:demo-frontend
```

```
kubectl create -f flow.yaml
kubectl delete -f flow.yaml
```
