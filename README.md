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
# Create a flow 
kubectl create -f ./ci/flow-test.yaml

# Delete a flow
kubectl delete -f ./ci/flow-test.yaml

# Get all flows in namespace
kubectl get flows -n baseline

# Describe a flow by its name 
kubectl describe flows flow-test -n baseline
```

Deploy the operator inside the cluster
```
make deploy (when you want to test it inside the cluster)
```

## Update the CRDs API

1. Read [this document][api-design-doc] to follow the design rules.
2. The CRDs API files are inside the `./api/core/v1` folder.
3. You can edit the `flow` API for example:
   1. Add, update or remove fields in the `FlowSpec` inside the `flow_types.go` file. Don't forget to add the json tags.
   2. Run `make manifests` to include your changes in the auto generated `./config/crd/bases/core.kardinal.dev_flows.yaml` manifest file.
   3. Update the spec example inside `./config/samples/core_v1_flow.yaml`
4. If you are adding a new CRD make sure its schema has been added in the `init` function in the `./cmd/main.go` file


## Update the RBAC permissions

1. Read [this document][rbac-markers-doc] to understand what are the RBAC markers and how to compose them.
2. Add, update or remove the RBAC markers, for instance the `flow` controller:
   1. Open the flow controller file `./internal/controller/core/flow_controller.go`
   2. Edit the markers inside of it.
   3. Run `make manifests` to include your changes in the auto generated `./config/rbac/role.yaml` manifest file.
   4. NOTE: If you receive an error, please run the specified command in the error and re-run make manifests.

[api-design-doc]: https://book.kubebuilder.io/cronjob-tutorial/api-design
[rbac-markers-doc]: https://book.kubebuilder.io/reference/markers/rbac