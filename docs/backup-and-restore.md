# Backup and Restore

AlloyDB Omni provides powerful features to protect your data using Backup and Restore mechanisms.

## Configuring Backups

Before you can take backups, you must define a `BackupPlan` that links to your target `DBCluster`.

### Basic Backup Plan
The [`v1_backupplan.yaml`](../samples/v1_backupplan.yaml) sample illustrates a standard Backup Plan that schedules full backups weekly and incremental backups daily. It retains backups for 14 days and targets `dbcluster-sample`.

### External Storage (Google Cloud Storage)
For improved disaster recovery, you can push backups off-cluster to a remote Google Cloud Storage (GCS) bucket. The [`v1_backupplan_gcs.yaml`](../samples/v1_backupplan_gcs.yaml) sample shows how to specify the `gsBucketName` and integrate it with a Google Cloud Service Account (GSA) and Kubernetes Service Account (KSA) using Workload Identity:

```yaml
spec:
  gcsBucketName: "<NAME_OF_THE_BUCKET>"
  gcsAuthType: workload-identity
```

## Taking Backups

While a `BackupPlan` schedules automatic backups, you can also trigger them manually on-demand. Use the [`v1_backup.yaml`](../samples/v1_backup.yaml) sample to create a `Backup` custom resource:

```bash
kubectl apply -f samples/v1_backup.yaml
```

This will trigger a backup immediately using the referenced `dbcluster-sample` and `backupplan1`.

## Restoring from a Backup

To restore your data to a new cluster or the same cluster, use a `Restore` resource. See the [`v1_restore.yaml`](../samples/v1_restore.yaml) sample, which references the source DBCluster and the backup you want to restore from:

```yaml
spec:
  sourceDBCluster: dbcluster-sample
  backup: backup1
```

## Point-in-Time Recovery and Cloning

You can clone a database cluster to a precise point in time (Point-in-Time Recovery, or PITR). The `Restore` API is also used for this operation.

As shown in [`v1_clone.yaml`](../samples/v1_clone.yaml), you provide a timestamp for the `pointInTime` field and a configuration for the new cloned cluster:

```yaml
spec:
  sourceDBCluster: dbcluster-sample
  pointInTime: "2024-02-23T19:59:43Z"
  clonedDBClusterConfig:
    dbclusterName: new-dbcluster-sample
```
