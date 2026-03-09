# AlloyDB Omni Samples

This repository provides best-practice technical examples and manifests for deploying and operating the [AlloyDB Omni Operator](https://cloud.google.com/alloydb/omni/kubernetes/current/docs/overview) on Kubernetes.

Here you will find templates and guidance to automate your database life cycle operations, whether you are managing clusters individually or managing fleets using enterprise GitOps tools.

## Documentation Guides

We have organized the examples in this repository into comprehensive functional guides. Please explore the operations relevant to your use case:

- **[Cluster Management](docs/cluster-management.md)**: Deploy basic to full configuration primary databases, setup read pools, and configure PostgreSQL parameters.
- **[Backup and Restore](docs/backup-and-restore.md)**: Schedule automated backup plans, push to Google Cloud Storage (GCS), trigger manual backups, and perform point-in-time recovery (PITR) clones.
- **[Replication and Disaster Recovery](docs/replication-and-disaster-recovery.md)**: Setup Upstream/Downstream data replication across clusters, and orchestrate graceful switchovers or forced failovers.
- **[Helm and GitOps Deployments](docs/helm-deployments.md)**: Discover how to use our Helm charts and ArgoCD configurations to simplify management of the operator and database workloads.
- **[Monitoring and Extensibility](docs/monitoring-and-extensibility.md)**: Leverage our Grafana dashboards for metrics visibility and see examples of using custom mutating webhooks to dynamically enforce topology constraints.

## Repository Structure

- `samples/`: Raw Kubernetes YAML custom resources for all the life cycle capabilities discussed in the guides.
- `k8s/helm/`: Helm Charts for deploying the AlloyDB Omni Operator and configurable Clusters.
- `crd/`: The raw Custom Resource Definitions (CRDs) required by the operator.
- `monitoring-dashboards/`: Extracted dashboard configurations for integration with observability stacks.
- `webhooks/`: Go-based sample mutating webhook servers mapped to database operations.

## Quickstart

If you already have the operator running, you can deploy a sample minimal database in seconds by applying our base configuration:

```bash
kubectl apply -f samples/v1_dbcluster.yaml
```

*Note: You must edit the secret configuration if deploying into production.*
