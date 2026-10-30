# AlloyDB Omni K8s Operator CLI

`adbo` is a developer-friendly CLI for the AlloyDB Omni Operator.

Requires Go 1.27.1+.

## Build / test

```bash
cd operator-client
go test ./...
go vet ./...
go build -o bin/adbo ./cmd/adbo
./bin/adbo --help
```

## How to add a command

Register `<group>_<verb>` commands on their **group** command (`cluster.go`, `backup.go`), not in `root.go`, so parallel PRs do not collide. Top-level commands from the spec (`status`, `connect`, `logs`, `restore`, `failover`, `switchover`, `doctor`, `version`) are the only ones added to `root.AddCommand(...)`, one line each. `support export` should get its own `support` group.

1. Create `internal/cmd/<group>_<verb>.go` with `newXxxCommand(opts *GlobalOptions) *cobra.Command`.
2. Keep `RunE` thin; put logic in `runXxx`:

   ```go
   name, err := opts.ClusterName(args)
   c, err := opts.Clients()
   list, err := c.In(k8s.DBClusterGVR).List(ctx, metav1.ListOptions{})
   return output.Print(cmd.OutOrStdout(), opts.Output, table, list.Items)
   ```

   For async ops (`create`, `trigger`, `restore`), wrap the wait with `opts.WithTimeout`. `list` / `status` do not need it.
3. Register the verb on the **group** command:
   - cluster verbs → `c.AddCommand(...)` in `internal/cmd/cluster.go`
   - backup verbs → `c.AddCommand(...)` in `internal/cmd/backup.go`

Use `c.InNamespace(gvr, "")` for `-A` (all namespaces) and `c.Cluster(gvr)` for cluster-scoped types (CRDs, Nodes). Return `c.FormatErr(err)` for Kubernetes API errors so NotFound/Forbidden include namespace and context. Tests inject a fake via `opts.NewClients`.

## Contributors
- https://github.com/hemanthsid
- https://github.com/Eugenehwng
- https://github.com/EvuhLi
- https://github.com/kthallam167
- https://github.com/ojpbarbosa
