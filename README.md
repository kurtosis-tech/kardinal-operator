# Kardinal Operator

Implementation of [Kardinal](https://github.com/kurtosis-tech/kardinal) as a K8S Operator.

## Development

Minikube + K8S manifest deployed. K8S context set to your local cluster.
```
make install (to install the CRDs into the cluster)
```

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

Deploy the operator inside the cluster
```
make deploy (when you want to test it inside the cluster)
```

## Update the CRDs API

1. The CRDs API files are inside the `./api/core/v1` folder.
2. You can edit the `flow` API for example:
   1. Add, update or remove fields in the `FlowSpec` inside the `flow_types.go` file. Don't forget to add the json tags.
   2. Run `make manifests` to include your changes in the auto generated `./config/crd/bases/core.kardinal.dev_flows.yaml` manifest file
   3. Update the spec example inside `./config/samples/core_v1_flow.yaml`
   
    