#!/usr/bin/env bash
# End-to-end check for adbo against a real AlloyDB Omni operator.
#
# Unit tests run against fake Kubernetes clients, which cannot catch the things
# the operator itself enforces -- `cluster delete` passed every unit test while
# being rejected outright by Omni's validating webhook. Run this against a live
# cluster before shipping a change to create, delete, stop or start.
#
#   ADBO=./adbo NAMESPACE=omni-test VERSION=18.3.0 ./hack/e2e.sh
#
# WAIT_TIMEOUT is the seconds to allow for any one phase change. The default
# suits a warm cluster; raise it where the Omni images still have to be pulled.
#
# Creates a uniquely named cluster, exercises every verb, and removes it again.
# Existing clusters are never touched.
set -euo pipefail

ADBO="${ADBO:-adbo}"
NAMESPACE="${NAMESPACE:-default}"
VERSION="${VERSION:-}"
CLUSTER="${CLUSTER:-adbo-e2e-$$}"
WAIT_TIMEOUT="${WAIT_TIMEOUT:-300}"
SECRET="db-pw-${CLUSTER}"

if [[ -z "$VERSION" ]]; then
  echo "VERSION is required; pick one the operator serves, e.g." >&2
  echo "  kubectl get dbclusters -A -o jsonpath='{.items[*].spec.databaseVersion}'" >&2
  exit 2
fi

step=0
say() { step=$((step + 1)); printf '\n=== %d. %s\n' "$step" "$1"; }
fail() { printf '    FAIL: %s\n' "$1" >&2; exit 1; }
ok() { printf '    ok: %s\n' "$1"; }

# Leave nothing behind, even on failure. The cluster name is unique to this
# run, so this can never remove someone else's cluster.
cleanup() {
  local rc=$?
  if kubectl get dbcluster "$CLUSTER" -n "$NAMESPACE" >/dev/null 2>&1; then
    printf '\n=== cleanup: removing %s\n' "$CLUSTER"
    "$ADBO" cluster delete "$CLUSTER" -y -n "$NAMESPACE" >/dev/null 2>&1 || true
  fi
  kubectl delete secret "$SECRET" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  [[ $rc -eq 0 ]] && printf '\nPASS: every verb behaved against the live operator\n'
  exit $rc
}
trap cleanup EXIT

phase() { kubectl get dbcluster "$CLUSTER" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null; }

# Omni can report a phase and then move on -- a cluster seen DBClusterReady may
# be DBClusterReconciling a second later. Asserting the phase the instant a
# wait returns is therefore racy, so settle on it instead of sampling once.
await_phase() {
  local want="$1" deadline=$((SECONDS + WAIT_TIMEOUT))
  while [[ $SECONDS -lt $deadline ]]; do
    [[ "$(phase)" == "$want" ]] && return 0
    sleep 3
  done
  fail "phase is $(phase) after ${WAIT_TIMEOUT}s, want $want (raise WAIT_TIMEOUT if the cluster is just slow)"
}
is_stopped() { kubectl get dbcluster "$CLUSTER" -n "$NAMESPACE" -o jsonpath='{.spec.primarySpec.isStopped}' 2>/dev/null; }
# These two return empty rather than failing: under `set -e` a non-zero exit
# inside "$(...)" aborts the script at the assignment, before the caller can
# report what was actually missing.
secret_password() {
  kubectl get secret "$SECRET" -n "$NAMESPACE" \
    -o go-template="{{index .data \"$CLUSTER\" | base64decode}}" 2>/dev/null || true
}
db_pod() {
  kubectl get pods -n "$NAMESPACE" -o name 2>/dev/null |
    grep -- "-${CLUSTER}-" | grep -v -- '-nm-' | head -1 | sed 's|pod/||' || true
}
# Proves the password in the secret actually authenticates, which is the one
# thing the operator has to agree with us about.
check_login() {
  local pw pod
  pw="$(secret_password)"
  [[ -n "$pw" ]] || fail "no password under key '$CLUSTER' in secret $SECRET"
  pod="$(db_pod)"
  [[ -n "$pod" ]] || fail "no database pod found for $CLUSTER"
  kubectl exec -n "$NAMESPACE" "$pod" -c database -- \
    env PGPASSWORD="$pw" psql -h 127.0.0.1 -U postgres -tAc 'select 1;' 2>/dev/null | grep -q 1 ||
    fail "the password in $SECRET did not authenticate"
}

printf 'adbo e2e: cluster=%s namespace=%s version=%s\n' "$CLUSTER" "$NAMESPACE" "$VERSION"

# client-go's fake tracker ignores DryRun, so --dry-run can only really be
# checked here. On a fresh name there is no admin secret yet, and Omni's create
# webhook refuses a DBCluster whose passwordRef does not resolve -- so create
# reports a local check rather than sending a doomed request.
say "dry-run create on a fresh name (local check, creates nothing)"
out="$("$ADBO" cluster create "$CLUSTER" --version "$VERSION" -n "$NAMESPACE" --dry-run)"
grep -q 'Checked locally only' <<<"$out" || fail "expected a local-only check, got: $out"
! kubectl get dbcluster "$CLUSTER" -n "$NAMESPACE" >/dev/null 2>&1 || fail "--dry-run created the DBCluster"
! kubectl get secret "$SECRET" -n "$NAMESPACE" >/dev/null 2>&1 || fail "--dry-run created the secret"
ok "nothing persisted, limitation reported"

say "create (waits for ready)"
time "$ADBO" cluster create "$CLUSTER" --version "$VERSION" -n "$NAMESPACE"
await_phase DBClusterReady
ok "phase is DBClusterReady"

# The operator reads the admin password from a key named after the cluster,
# not "password". Getting this wrong leaves a cluster that never starts.
keys="$(kubectl get secret "$SECRET" -n "$NAMESPACE" -o go-template='{{range $k, $v := .data}}{{println $k}}{{end}}')"
[[ "$keys" == "$CLUSTER" ]] || fail "secret keys are '${keys//$'\n'/,}', want just '$CLUSTER'"
ok "secret key is the cluster name"

ref="$(kubectl get dbcluster "$CLUSTER" -n "$NAMESPACE" -o jsonpath='{.spec.primarySpec.adminUser.passwordRef.name}')"
[[ "$ref" == "$SECRET" ]] || fail "passwordRef is '$ref', want '$SECRET'"
ok "passwordRef points at $SECRET"

check_login
ok "the generated password authenticates"

say "status"
"$ADBO" status "$CLUSTER" -n "$NAMESPACE"

say "dry-run stop (must change nothing)"
"$ADBO" cluster stop "$CLUSTER" -y -n "$NAMESPACE" --dry-run
[[ "$(is_stopped)" != "true" ]] || fail "--dry-run actually stopped the cluster"
ok "isStopped untouched"

say "stop (waits for stopped)"
time "$ADBO" cluster stop "$CLUSTER" -y -n "$NAMESPACE"
[[ "$(is_stopped)" == "true" ]] || fail "spec.primarySpec.isStopped is '$(is_stopped)', want true"
await_phase DBClusterStopped
kubectl get secret "$SECRET" -n "$NAMESPACE" >/dev/null || fail "stop removed the admin secret"
ok "stopped, isStopped=true, secret kept"

say "stop again is a no-op"
out="$("$ADBO" cluster stop "$CLUSTER" -y -n "$NAMESPACE")"
grep -q 'already stopped' <<<"$out" || fail "expected a no-op, got: $out"
ok "reports already stopped"

say "start (waits for ready)"
time "$ADBO" cluster start "$CLUSTER" -n "$NAMESPACE"
[[ "$(is_stopped)" == "false" ]] || fail "isStopped is '$(is_stopped)', want false"
await_phase DBClusterReady
ok "started, isStopped=false"

# The whole point of stop/start over delete/create.
check_login
ok "the same password still authenticates after a stop/start round trip"

# spec.isDeleted is still false here, and a dry-run patch is not persisted, so
# the DELETE cannot be validated server-side -- delete says so rather than
# sending a request the webhook would deny for the wrong reason.
say "dry-run delete (must delete nothing)"
out="$("$ADBO" cluster delete "$CLUSTER" -n "$NAMESPACE" --dry-run)"
grep -q 'nothing was changed' <<<"$out" || fail "expected a dry-run report, got: $out"
grep -q 'cannot be validated yet' <<<"$out" || fail "expected the webhook caveat, got: $out"
kubectl get dbcluster "$CLUSTER" -n "$NAMESPACE" >/dev/null 2>&1 || fail "--dry-run deleted the cluster"
kubectl get secret "$SECRET" -n "$NAMESPACE" >/dev/null 2>&1 || fail "--dry-run deleted the admin secret"
[[ "$(kubectl get dbcluster "$CLUSTER" -n "$NAMESPACE" -o jsonpath='{.spec.isDeleted}')" != "true" ]] ||
  fail "--dry-run persisted the isDeleted patch"
ok "cluster, secret and isDeleted all untouched"

say "delete (waits until gone)"
# Omni's webhook denies DELETE unless spec.isDeleted is true; adbo patches it.
time "$ADBO" cluster delete "$CLUSTER" -y -n "$NAMESPACE"
! kubectl get dbcluster "$CLUSTER" -n "$NAMESPACE" >/dev/null 2>&1 || fail "the DBCluster is still present"
! kubectl get secret "$SECRET" -n "$NAMESPACE" >/dev/null 2>&1 || fail "the admin secret is still present"
ok "cluster and secret are gone"

say "errors are reported, not swallowed"
! "$ADBO" cluster delete "no-such-cluster-$$" -n "$NAMESPACE" >/dev/null 2>&1 || fail "deleting a missing cluster should fail"
ok "deleting a missing cluster fails"
! "$ADBO" cluster create "x-$$" -n "$NAMESPACE" >/dev/null 2>&1 || fail "create without --version should fail"
ok "create without --version fails"
