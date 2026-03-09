# Cluster Management

This guide covers the fundamental operations for deploying and managing AlloyDB Omni clusters on Kubernetes using the available samples.

## Basic Database Cluster

To provision a minimal AlloyDB Omni cluster, use the [`v1_dbcluster.yaml`](../samples/v1_dbcluster.yaml) sample. It defines a single primary node with basic CPU, memory, and disk resources, and references a Kubernetes Secret for the database admin password.

```bash
kubectl apply -f samples/v1_dbcluster.yaml
```

For a more comprehensive setup, review [`v1_dbcluster_full.yaml`](../samples/v1_dbcluster_full.yaml) which includes additional configuration options for parameters, custom labeling, node selectors, and annotations.

## High Availability and Scaling

### Enabling High Availability

High Availability (HA) ensures that your database cluster can survive the failure of a single node. The [`v1_dbcluster_ha.yaml`](../samples/v1_dbcluster_ha.yaml) sample demonstrates how to enable HA by configuring the `availability` section:

```yaml
  availability:
    numberOfStandbys: 1
    enableStandbyAsReadReplica: true
```

This configuration provisions an additional synchronous standby copy of the primary node. If `enableStandbyAsReadReplica` is true, the standby node can also serve read traffic.

### Load Balancing

To expose your database cluster for external connections, you can define external LoadBalancer configurations as shown in [`v1_dbcluster_lb.yaml`](../samples/v1_dbcluster_lb.yaml).

## Read Pools

Read pools allow you to scale your read-bound workloads independently of your primary database cluster. The operator uses `DBInstance` resources to manage read pools linked to a parent `DBCluster`.

Refer to [`v1_dbinstance_readpool.yaml`](../samples/v1_dbinstance_readpool.yaml) to configure a read pool instance. In this sample, you can configure the `nodeCount` and custom `schedulingconfig` to define Kubernetes pod affinity/anti-affinity and tolerations:

```yaml
spec:
  instanceType: ReadPool
  dbcParent:
    name: dbcluster-sample
  nodeCount: 2
```

## Customization

### Database Parameters

You can provide custom PostgreSQL settings to tune your cluster. The [`v1_dbcluster_parameters.yaml`](../samples/v1_dbcluster_parameters.yaml) sample illustrates how to append PostgreSQL parameters (e.g., `temp_buffers`, `max_connections`) directly to the `primarySpec`.

### Sidecars

Sometimes you need additional agents running alongside your database instances. The [`v1_sidecar.yaml`](../samples/v1_sidecar.yaml) and [`v1_dbcluster_sidecar.yaml`](../samples/v1_dbcluster_sidecar.yaml) files demonstrate how to inject sidecar containers into the pods.

## Maintenance and Upgrades

The [`MVU.sh`](../samples/MVU.sh) (Minor Version Upgrade) script is available as a shell script to help you orchestrate cluster upgrades gracefully.
