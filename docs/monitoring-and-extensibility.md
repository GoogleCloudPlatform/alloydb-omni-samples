# Monitoring and Extensibility

AlloyDB Omni provides deep integration into the Kubernetes ecosystem, enabling comprehensive observability and customized workload behaviors.

## Monitoring and Dashboards

The repository provides pre-built observability configurations for your database clusters.

In the `monitoring-dashboards/grafana/` directory, you will find `alloydbomni_dashboard.yaml`. This is a `GrafanaDashboard` Custom Resource that, once deployed to a cluster running the Grafana Operator, provides a rich dashboard visualizing key PostgreSQL performance indicators.

The dashboard integrates with Prometheus to display:
- Service Up/Down status
- CPU and Memory Utilization per Node
- Database Storage Volume and Usage
- Transaction rates, cache hit ratios, and connections

To use it:
```bash
kubectl apply -f monitoring-dashboards/grafana/alloydbomni_dashboard.yaml
```

### Operator and Database Observability

In addition to Grafana dashboards, you can formally configure how logs, metrics, and traces are handled in the operator and the database instances via the `ObservabilityConfig` resource. The [`v1_observabilityconfig.yaml`](../samples/v1_observabilityconfig.yaml) sample shows how to setup logging formats, enable prometheus metrics scraping endpoints, and configure OpenTelemetry tracing export parameters for your database fleet.

## Extensibility (Webhooks)

The `webhooks/` directory demonstrates how you can dynamically mutate or validate requests for database clusters using Kubernetes mutating webhooks.

Examples included are:
- **alloydb-mutating-wh**: A general-purpose mutating webhook.
- **alloydb-nodeselector-mwh**: A webhook that intercepts cluster creation requests to inject specific `nodeSelectors` and tolerations dynamically. This allows platform teams to forcefully schedule AlloyDB Omni pods onto designated hardware nodes without requiring end-users to specify them in the `DBCluster` YAML.

These webhooks are packaged as Go applications with their own Dockerfiles and Helm charts, ready for customization.
