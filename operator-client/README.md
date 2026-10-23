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

Release builds stamp the version that `adbo version` prints:

```bash
go build -ldflags "-X github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/version.Version=v0.1.0" -o bin/adbo ./cmd/adbo
```

## How to add a command

Do **not** register verbs in `root.go`. That file only attaches group commands (`cluster`, `backup`) so parallel PRs do not collide.

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
