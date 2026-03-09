# Replication and Disaster Recovery

AlloyDB Omni supports robust replication and disaster recovery features for your Kubernetes deployments.

## Setting Up Replication

To replicate data between an upstream (primary) cluster and a downstream (replica) cluster, you use the `Replication` custom resource.

### Upstream Configuration
On the source cluster, define an upstream replication resource as shown in [`v1_replication_upstream.yaml`](../samples/v1_replication_upstream.yaml). The operator configures the cluster as a replication source.

### Downstream Configuration
On the target cluster, configure a downstream replication resource to connect to the upstream database. In [`v1_replication_downstream.yaml`](../samples/v1_replication_downstream.yaml), you provide the host, port, credentials, and replication slot information:

```yaml
spec:
  dbcluster:
    name: dbcluster-sample
  downstream:
    host: "10.10.10.10"
    port: 5432
    username: alloydbreplica
    password:
      name: "ha-rep-pw-dbcluster-sample"
    replicationSlotName: "dbcluster_sample_replication_upstream_sample"
    control: setup
```
To promote a downstream replica into an independent primary cluster, change `control` from `setup` to `promote`.

## Managed Failover and Switchover

Once you have a cluster running with standbys (e.g., using `v1_dbcluster_ha.yaml`), you can perform lifecycle operations using standard custom resources.

### Switchover (Graceful)
A graceful switchover deliberately demotes the primary instance and promotes a standby instance. Use the [`v1_switchover.yaml`](../samples/v1_switchover.yaml) sample and provide the name of the standby instance you want to promote:

```yaml
spec:
  dbclusterRef: dbcluster-sample
  newPrimary: aaaa-dbcluster-sample
```

### Failover (Forced)
In the event of an unexpected outage of the primary node where automatic failover does not recover the cluster or you need to intercede manually, use the [`v1_failover.yaml`](../samples/v1_failover.yaml) resource:

```yaml
spec:
  dbclusterRef: dbcluster-sample
```
Depending on data synchronization, failovers may incur some data loss, unlike graceful switchovers.
