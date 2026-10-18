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

1. Create `internal/cmd/<group>_<verb>.go` with `newXxxCommand(opts *GlobalOptions) *cobra.Command`.
2. Keep `RunE` thin; put logic in `runXxx`:

   ```go
   name, err := opts.ClusterName(args)
   c, err := opts.Clients()
   list, err := c.In(k8s.DBClusterGVR).List(ctx, metav1.ListOptions{})
   return output.Print(cmd.OutOrStdout(), opts.Output, table, list.Items)
   ```

3. Register it in `root.AddCommand(...)` in `root.go`.

Use `c.InNamespace(gvr, "")` for `-A` (all namespaces) and `c.Cluster(gvr)` for cluster-scoped types (CRDs, Nodes). Tests inject a fake via `opts.NewClients`.

## Contributors

- https://github.com/hemanthsid
- https://github.com/Eugenehwng
- https://github.com/EvuhLi
- https://github.com/kthallam167
