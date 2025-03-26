#!/bin/bash

set -o xtrace
set -o errexit
set -o pipefail

# CPA and database are bundled together
# ./mvu.sh 1.16.3 1.4.0 dbcluster-sample

# DBCluster name and Namespace
if [[ $# -lt 1 || $# -gt 4 ]]; then
  echo "Usage: $0 <newDBVersion> <newCPAVersion> <DBClusterName> <Namespace>"
  exit 1
fi

newDBVersion="$1"
newCPAVersion="$2"
dbcName="$3"
ns="${4:-default}" # Use default if namespace not provided

echo "Pause backup and stop all replication activities before MVU"

# Delete the backupplan
if output=$(kubectl get backupplans.alloydbomni.dbadmin.goog -n "$ns" -l "operator/dbcluster=$dbcName" 2>/dev/null) && [[ -n "$output" ]]; then
  # A backupplan exists, ask for user input
  read -p "Backupplans found for dbcluster '$dbcName'. Do you want to delete it? (y/n): " response

  if [[ "$response" == "y" ]]; then
    # Delete the backupplan
    kubectl delete backupplans.alloydbomni.dbadmin.goog -n "$ns" -l operator/dbcluster="$dbcName"
    if [[ $? -eq 0 ]]; then
      echo "Backupplan is deleted successfully."
    else
      echo "Failed to delete the backupplan."
      exit 1
    fi
  else
    echo "Not delete backup? Exit upgrade."
    exit 1
  fi
else
  echo "No backupplans found for dbcluster '$dbcName'."
fi

# Disable HA
if numOfStandby=$(kubectl get dbclusters.alloydbomni.dbadmin.goog "$dbcName" -n "$ns" -o jsonpath='{.spec.availability.numberOfStandbys}' 2>/dev/null) && [[ "$numOfStandby" -gt 0 ]]; then
 # Standbies exists, ask for user input
  read -p "Standby exists. Do you want to delete it? (y/n): " response

  if [[ "$response" == "y" ]]; then
    # Delete standbys
    kubectl patch dbclusters.alloydbomni.dbadmin.goog "$dbcName" -n "$ns" -p '{"spec": {"availability": {"numberOfStandbys":0}}}' --type=merge
    if [[ $? -eq 0 ]]; then
      echo "Standby is disabled successfully."
    else
      echo "Failed to disable standby."
      exit 1
    fi
  else
    echo "Not disable standby? Exit upgrade."
    exit 1
  fi
else
  echo "Standby is not found for dbcluster '$dbcName'."
fi

# Disable read replica
if output=$(kubectl get dbinstances.alloydbomni.dbadmin.goog -n "$ns" -l alloydbomni.dbadmin.goog/dbcluster="$dbcName" 2>/dev/null) && [[ -n "$output" ]]; then
 # Standbies exists, ask for user input
  read -p "Read replica exists. Do you want to delete it? (y/n): " response

  if [[ "$response" == "y" ]]; then
    # Delete standbys
    kubectl delete dbinstances.alloydbomni.dbadmin.goog -n "$ns" -l alloydbomni.dbadmin.goog/dbcluster="$dbcName"
    if [[ $? -eq 0 ]]; then
      echo "Read replica is disabled successfully."
    else
      echo "Failed to disable read replica."
      exit 1
    fi
  else
    echo "Not disable read replica? Exit upgrade."
    exit 1
  fi
else
  echo "Read replica is not found for dbcluster '$dbcName'."
fi

# DR primary or secondary
if output=$(kubectl get replications.alloydbomni.dbadmin.goog -n "$ns" -l replication-dbc="$dbcName" 2>/dev/null) && [[ -n "$output" ]]; then
 # DR replications exists, ask for user input
  echo "Follow the manual to terminate DR replications. E.g. DR secondary might need to promote. Exit"
  exit 1
fi

# Make sure the DBCluster already exists and Ready
while ready=$(kubectl get dbclusters.alloydbomni.dbadmin.goog "$dbcName" -n "$ns" -o jsonpath='{.status.phase}') && [[ "$ready" != "DBClusterReady" ]]; do
  echo "DBCluster is not ready, wait for it to be ready"
done

# Make sure current version has been set
currVersionInStatus=$(kubectl get dbcluster.alloydbomni.dbadmin.goog "$dbcName" -n "$ns"  -o jsonpath='{.status.primary.currentDatabaseVersion}')
currVersionInSpec=$(kubectl get dbcluster.alloydbomni.dbadmin.goog "$dbcName" -n "$ns"  -o jsonpath='{.spec.databaseVersion}')
if [[ "$currVersionInStatus" != "$currVersionInSpec" ]]; then
  echo "DBCluster db version has not been set in status yet"
  exit 1
fi

# Annotate DBCluster for manual MVU
kubectl annotate dbclusters.alloydbomni.dbadmin.goog "$dbcName" -n "$ns" dbs.dbadmin.goog.com/manualmvu=true
echo "Wait for Pod to restart"
sleep 5

podName=$(kubectl get pods -n "$ns" -l alloydbomni.internal.dbadmin.goog/dbcluster="$dbcName",alloydbomni.internal.dbadmin.goog/task-type="database" -o name | cut -d'/' -f2)
if [ -z "$podName" ]; then
  echo "Error: Pod not found for DBCluster '$dbcName' in namespace '$ns'"
  exit 1
fi

# Wait for pod to be ready
echo "Pod becomes ready"
kubectl wait --for=condition=Ready pod/"$podName" -n "$ns" --timeout=120s

echo "Alias pg15 data directory"
# Perform database upgrade steps (PostgreSQL 15 to 16)
kubectl exec -ti "$podName" -n "$ns" -c database -- /bin/bash -c "
  supervisorctl.par stop postgres;
  mkdir -p /mnt/disks/pgsql/15;
  mv /mnt/disks/pgsql/data /mnt/disks/pgsql/15/data;
  cp -r /usr/lib/postgresql/15/bin /mnt/disks/pgsql/15/.;
  cp -r /usr/lib/postgresql/15/lib /mnt/disks/pgsql/15/.;
  cp -r /usr/share/postgresql/15 /mnt/disks/pgsql/15/share;
  rm /mnt/disks/pgsql/15/share/postgresql.conf.sample;
  cp /usr/share/postgresql/postgresql.conf.sample /mnt/disks/pgsql/15/share/postgresql.conf.sample;
  chmod 2740 /mnt/disks/pgsql/15/data;
"

echo "Update database version to $newDBVersion and recommended CPA version to $newCPAVersion"
# Patch DBCluster to upgrade databaseVersion
kubectl patch dbclusters.alloydbomni.dbadmin.goog "$dbcName" -n "$ns" --type=merge -p '{"spec":{"databaseVersion":"'$newDBVersion'","controlPlaneAgentsVersion": "'$newCPAVersion'"}}'

# Wait for current version getting updated
kubectl wait --for=jsonpath='{.status.primary.currentDatabaseVersion}'="'$newDBVersion'" dbcluster/"$dbcName" -n "$ns" --timeout=240s

# Perform post-upgrade steps
kubectl exec -ti "$podName" -n "$ns" -c database -- /bin/bash -c "
  supervisorctl.par stop postgres;
  rm -fr /mnt/disks/pgsql/data;
  initdb -D /mnt/disks/pgsql/data -U alloydbadmin --data-checksums --encoding=UTF8 --locale=C --locale-provider=icu --icu-locale=und-x-icu --auth-host=trust --auth-local=reject;
  cd ~;
  cp /mnt/disks/pgsql/15/data/pg_hba.conf /mnt/disks/pgsql/15/data/pg_hba.conf.bak;
  echo \"local   all     all             trust\" >> /mnt/disks/pgsql/15/data/pg_hba.conf;
  echo \"host    all     all     127.0.0.1/32      trust\" >> /mnt/disks/pgsql/15/data/pg_hba.conf;
  rm /mnt/disks/pgsql/data/pg_hba.conf;
  echo \"local   all     all             trust\" >> /mnt/disks/pgsql/data/pg_hba.conf;
  echo \"host    all     all     127.0.0.1/32      trust\" >> /mnt/disks/pgsql/data/pg_hba.conf;
  chmod 2740 /mnt/disks/pgsql/15/data;
  pg_upgrade -U alloydbadmin -b /mnt/disks/pgsql/15/bin -B /usr/lib/postgresql/16/bin -d /mnt/disks/pgsql/15/data -D /mnt/disks/pgsql/data --link -v;
  cp /mnt/disks/pgsql/15/data/pg_hba.conf.bak /mnt/disks/pgsql/data/pg_hba.conf;
  cp -r /mnt/disks/pgsql/15/data/postgresql.conf /mnt/disks/pgsql/data/.;
  cp -r /mnt/disks/pgsql/15/data/postgresql.conf.d /mnt/disks/pgsql/data/.;
  cp -r /mnt/disks/pgsql/15/data/parambackup /mnt/disks/pgsql/data/.;
  supervisorctl.par start postgres;
  rm -fr /mnt/disks/pgsql/15/;
"

# Remove annotation
kubectl annotate dbclusters.alloydbomni.dbadmin.goog "$dbcName" -n "$ns" dbs.dbadmin.goog.com/manualmvu-

# Wait for DBCluster to be ready again
while ready=$(kubectl get dbclusters.alloydbomni.dbadmin.goog "$dbcName" -n "$ns" -o jsonpath='{.status.phase}') && [[ "$ready" != "DBClusterReady" ]]; do
  echo "DBCluster is not ready, wait for it to be ready"
done

echo "MVU is done"
