package cmd

import "strings"

// specCommand is one line of `adbo help`. The list is the finalized adbo
// command spec, including commands that are not registered in cobra yet.
type specCommand struct {
	section string
	path    string
	usage   string
	short   string
}

// specCommands is the adbo command spec. Order within a section is the spec order.
var specCommands = []specCommand{
	{section: "Provisioning", path: "cluster create", usage: "adbo cluster create <name>", short: "Create a DBCluster and wait for DBClusterReady"},
	{section: "Provisioning", path: "cluster delete", usage: "adbo cluster delete <name>", short: "Delete a cluster and its child resources"},
	{section: "Provisioning", path: "cluster stop", usage: "adbo cluster stop [<name>]", short: "Stop a cluster"},
	{section: "Provisioning", path: "cluster start", usage: "adbo cluster start [<name>]", short: "Start a stopped cluster"},
	{section: "Provisioning", path: "cluster restart", usage: "adbo cluster restart [<name>]", short: "Stop a cluster, then start it again"},
	{section: "Provisioning", path: "cluster maintenance", usage: "adbo cluster maintenance [<name>]", short: "Turn maintenance mode on or off"},

	{section: "Scaling", path: "cluster upgrade", usage: "adbo cluster upgrade [<name>]", short: "Upgrade the database and agent versions"},
	{section: "Scaling", path: "cluster downgrade", usage: "adbo cluster downgrade [<name>]", short: "Downgrade the database and agent versions"},
	{section: "Scaling", path: "cluster scale", usage: "adbo cluster scale [<name>]", short: "Change CPU, memory, storage, or standbys"},
	{section: "Scaling", path: "cluster read-pool create", usage: "adbo cluster read-pool create", short: "Create a read-pool DBInstance"},
	{section: "Scaling", path: "cluster read-pool list", usage: "adbo cluster read-pool list", short: "List read pools of a cluster"},
	{section: "Scaling", path: "cluster read-pool scale", usage: "adbo cluster read-pool scale", short: "Scale a read pool"},
	{section: "Scaling", path: "cluster read-pool delete", usage: "adbo cluster read-pool delete", short: "Delete a read pool"},

	{section: "Backup, restore, and replication", path: "backup trigger", usage: "adbo backup trigger [<name>]", short: "Take an on-demand backup"},
	{section: "Backup, restore, and replication", path: "backup list", usage: "adbo backup list [<name>]", short: "List backups of a cluster"},
	{section: "Backup, restore, and replication", path: "restore", usage: "adbo restore [<name>]", short: "Restore in place, or clone to a new cluster"},
	{section: "Backup, restore, and replication", path: "failover", usage: "adbo failover [<name>]", short: "Fail over to a standby"},
	{section: "Backup, restore, and replication", path: "switchover", usage: "adbo switchover [<name>]", short: "Switch over to a standby"},
	{section: "Backup, restore, and replication", path: "replicate create", usage: "adbo replicate create <source> <target>", short: "Create an upstream and downstream replication"},
	{section: "Backup, restore, and replication", path: "replicate status", usage: "adbo replicate status <name>", short: "Show replication status"},
	{section: "Backup, restore, and replication", path: "replicate promote", usage: "adbo replicate promote <target>", short: "Promote a replication target"},
	{section: "Backup, restore, and replication", path: "replicate delete", usage: "adbo replicate delete <name>", short: "Delete replication resources"},

	{section: "Management", path: "cluster credentials", usage: "adbo cluster credentials [<name>]", short: "Show the postgres connection string"},
	{section: "Management", path: "cluster list", usage: "adbo cluster list", short: "List database clusters"},
	{section: "Management", path: "cluster select", usage: "adbo cluster select [<name>]", short: "Set the default cluster for later commands"},
	{section: "Management", path: "status", usage: "adbo status [<name>]", short: "Show cluster, HA, pod, and incident status"},
	{section: "Management", path: "cluster status", usage: "adbo cluster status [<name>]", short: "Show cluster status (alias of adbo status)"},
	{section: "Management", path: "cluster describe", usage: "adbo cluster describe [<name>]", short: "Show the full cluster spec, children, and events"},
	{section: "Management", path: "cluster edit", usage: "adbo cluster edit [<name>]", short: "Edit cluster resources, or open the spec in $EDITOR"},
	{section: "Management", path: "logs", usage: "adbo logs [<name>]", short: "Stream database, diagnostic, or operator logs"},
	{section: "Management", path: "support export", usage: "adbo support export [<name>]", short: "Write a support bundle (secrets excluded)"},
	{section: "Management", path: "version", usage: "adbo version", short: "Show the adbo and operator versions"},

	{section: "Miscellaneous", path: "connect", usage: "adbo connect [<name>]", short: "Open a psql session to a DBCluster"},
	{section: "Miscellaneous", path: "doctor", usage: "adbo doctor", short: "Check the installation, prerequisites, and versions"},
	{section: "Miscellaneous", path: "completion", usage: "adbo completion [shell]", short: "Generate a shell completion script"},
	{section: "Miscellaneous", path: "help", usage: "adbo help [command]", short: "List available commands"},
}

func specSections() []string {
	var sections []string
	seen := map[string]bool{}
	for _, cmd := range specCommands {
		if seen[cmd.section] {
			continue
		}
		seen[cmd.section] = true
		sections = append(sections, cmd.section)
	}
	return sections
}

func findSpec(args []string) (specCommand, bool) {
	path := strings.Join(args, " ")
	for _, cmd := range specCommands {
		if cmd.path == path {
			return cmd, true
		}
	}
	return specCommand{}, false
}
