Sample yamls to deploy a bare-minimal Observability Stack with Prometheus and Grafana with auto-scraping of AlloyDB Omni metrics.

Deploy:
```shell
kubectl create ns my-prometheus
kubectl create ns my-grafana
kubectl apply -f prometheus/
kubectl apply -f grafana/
```
Note it's expected to see errors about `grafana/grafana-datasource.yaml` because it's just the content of the grafana configmap, not a valid k8s resource spec yaml.

Verify:
```shell
kubectl get deployment -n my-prometheus
### output: prometheus-server                           1/1     1            1           6d23h
kubectl get deployment -n my-grafana
### output: grafana   1/1     1            1           2d23h
```

Port-forward Grafana:
```shell
kubectl port-forward svc/grafana -n my-grafana 3000:3000
```
Access Grafana:
```
username: admin
password: admin

Click "skip" when it asks you to change password

```

Pre-built dashboards are under folder `alloydbomni`.

Deploy Kube-State-Metrics: With the operator installed, run:
```shell
kubectl apply -f ksm/
```

Verify:
```shell
kubectl get deployment -n alloydb-omni-system kube-state-metrics
### output: kube-state-metrics   1/1     1            1           46s
```
Note it's expected to see errors about `ksm/config.yaml` because it's just the content of the ksm metrics configmap.

Undeploy:
```shell
kubectl delete -f ksm/
kubectl delete -f grafana/
kubectl delete -f prometheus/
kubectl delete ns my-grafana
kubectl delete ns my-prometheus
```

For developing new metrics in KSM:
Create configmap from ksm/config.yaml:
```shell
kubectl create configmap kube-state-metrics -n alloydb-omni-system --from-file=config.yaml=ksm/config.yaml
```
Then restart the KSM pod:
```shell
kubectl rollout restart deployment/kube-state-metrics -n alloydb-omni-system
```
