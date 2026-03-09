# Helm Deployments

The repository provides fully configured Helm charts to simplify the deployment of AlloyDB Omni on Kubernetes. You can find them in the `k8s/helm/` directory.

## Available Charts

1. **alloydb-omni-operator**: Installs the custom resource definitions (CRDs), webhooks, and the operator deployment itself required to manage AlloyDB on your cluster.
2. **alloydb-omni-cluster**: A customizable chart to provision instances of your database:
   - Sets admin passwords
   - Configures Primary, Standby, and ReadPool resources
   - Allows configuration of database parameters and sidecars
3. **argocd-applications**: An ArgoCD application specification that manages the lifecycle of the `alloydb-omni-operator` and `alloydb-omni-cluster` charts via GitOps.

## Helm Values (`alloydb-omni-cluster`)

You can modify the deployment by passing your own values configuration. Key tunable values include:

| Name | Description | Default |
| --- | --- | --- |
| `database_version` | Database version | `15.5.2` |
| `cpu_count` / `memory_size` | Resource requests for the cluster | `2` / `16Gi` |
| `data_disk_size` | Size of the main database volume | `100Gi` |
| `num_standbys` | Number of HA standby instances | `1` |
| `num_readpools` | Number of read replicas | `1` |
| `parameters` | Add custom PostgreSQL settings | `n/a` |
| `pgadmin.enabled` | Deploy a `pgadmin4` interface | `true` |

For a complete list of values and their functions, review the `k8s/helm/README.md` or look at the `values.yaml` in the respective chart directories.

## Deployment Example

To automate the chart packaging, you can use the included `cloudbuild.yaml`.

For an ArgoCD GitOps deployment, update the `argocd-applications/values.yaml` and run:

```bash
helm upgrade -i alloydb-omni oci://${_LOCATION}-docker.pkg.dev/${PROJECT_ID}/${_REPO}/alloydb-omni-argocd-applications
```
