package walker

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/iParadigms/walker/console"
	"github.com/spf13/cobra"
)

// Cmd provides an easy interface for creating walker binaries that use their
// own Handler, Datastore, or Dispatcher. A crawler that uses the default for
// each of these requires simply:
//
//		func main() {
//			walker.Cmd.Execute()
//		}
//
// To create your own binary that uses walker's flags but has its own handler:
//
//		func main() {
//			walker.Cmd.Handler = NewMyHandler()
//			walker.Cmd.Execute()
//		}
//
// Likewise if you want to set your own Datastore and Dispatcher:
//
//		func main() {
//			walker.Cmd.Datastore = NewMyDatastore()
//			walker.Cmd.Dispatcher = NewMyDatastore()
//			walker.Cmd.Execute()
//		}
//
// walker.Cmd.Execute() blocks until the program has completed (usually by
// being shutdown gracefully via SIGINT).
var Cmd struct {
	*cobra.Command
	Handler    Handler
	Datastore  Datastore
	Dispatcher Dispatcher
}

func fatalf(format string, args ...interface{}) {
	fmt.Printf(format, args...)
	fmt.Println()
	os.Exit(1)
}

func init() {
	walkerCommand := &cobra.Command{
		Use: "walker",
	}

	var config string
	walkerCommand.PersistentFlags().StringVarP(&config,
		"config", "c", "", "path to a config file to load")
	readConfig := func() {
		if config != "" {
			if err := ReadConfigFile(config); err != nil {
				panic(err.Error())
			}
		}
	}

	var noConsole bool = false
	crawlCommand := &cobra.Command{
		Use:   "crawl",
		Short: "start an all-in-one crawler",
		Run: func(cmd *cobra.Command, args []string) {
			readConfig()

			if Cmd.Datastore == nil {
				ds, err := NewCassandraDatastore()
				if err != nil {
					fatalf("Failed creating Cassandra datastore: %v", err)
				}
				Cmd.Datastore = ds
				Cmd.Dispatcher = &CassandraDispatcher{}
			}

			if Cmd.Handler == nil {
				Cmd.Handler = &SimpleWriterHandler{}
			}

			manager := &FetchManager{
				Datastore: Cmd.Datastore,
				Handler:   Cmd.Handler,
			}
			go manager.Start()

			if Cmd.Dispatcher != nil {
				go func() {
					err := Cmd.Dispatcher.StartDispatcher()
					if err != nil {
						panic(err.Error())
					}
				}()
			}

			if !noConsole {
				console.Start()
			}

			sig := make(chan os.Signal)
			signal.Notify(sig, syscall.SIGINT)
			<-sig

			if Cmd.Dispatcher != nil {
				Cmd.Dispatcher.StopDispatcher()
			}
			manager.Stop()
		},
	}
	//XXX: I want D. Kinder to tell me what he wants to call the noConsole flag
	crawlCommand.Flags().BoolVarP(&noConsole, "xconsole", "x", false, "Do not start the console")
	walkerCommand.AddCommand(crawlCommand)

	fetchCommand := &cobra.Command{
		Use:   "fetch",
		Short: "start only a walker fetch manager",
		Run: func(cmd *cobra.Command, args []string) {
			readConfig()

			if Cmd.Datastore == nil {
				ds, err := NewCassandraDatastore()
				if err != nil {
					fatalf("Failed creating Cassandra datastore: %v", err)
				}
				Cmd.Datastore = ds
				Cmd.Dispatcher = &CassandraDispatcher{}
			}

			if Cmd.Handler == nil {
				Cmd.Handler = &SimpleWriterHandler{}
			}

			manager := &FetchManager{
				Datastore: Cmd.Datastore,
				Handler:   Cmd.Handler,
			}
			go manager.Start()

			sig := make(chan os.Signal)
			signal.Notify(sig, syscall.SIGINT)
			<-sig

			manager.Stop()
		},
	}
	walkerCommand.AddCommand(fetchCommand)

	dispatchCommand := &cobra.Command{
		Use:   "dispatch",
		Short: "start only a walker dispatcher",
		Run: func(cmd *cobra.Command, args []string) {
			readConfig()

			if Cmd.Dispatcher == nil {
				Cmd.Dispatcher = &CassandraDispatcher{}
			}

			go func() {
				err := Cmd.Dispatcher.StartDispatcher()
				if err != nil {
					panic(err.Error())
				}
			}()

			sig := make(chan os.Signal)
			signal.Notify(sig, syscall.SIGINT)
			<-sig

			Cmd.Dispatcher.StopDispatcher()
		},
	}
	walkerCommand.AddCommand(dispatchCommand)

	var seedURL string
	seedCommand := &cobra.Command{
		Use:   "seed",
		Short: "add a seed URL to the datastore",
		Long: `Seed is useful for:
	- Adding starter links to bootstrap a broad crawl
	- Adding links when add_new_domains is false
	- Adding any other link that needs to be crawled soon

This command will insert the provided link and also add its domain to the
crawl, regardless of the add_new_domains configuration setting.`,
		Run: func(cmd *cobra.Command, args []string) {
			readConfig()

			orig := Config.AddNewDomains
			defer func() { Config.AddNewDomains = orig }()
			Config.AddNewDomains = true

			if seedURL == "" {
				fatalf("Seed URL needed to execute; add on with --url/-u")
			}
			u, err := ParseURL(seedURL)
			if err != nil {
				fatalf("Could not parse %v as a url: %v", seedURL, err)
			}

			if Cmd.Datastore == nil {
				ds, err := NewCassandraDatastore()
				if err != nil {
					fatalf("Failed creating Cassandra datastore: %v", err)
				}
				Cmd.Datastore = ds
			}

			Cmd.Datastore.StoreParsedURL(u, nil)
		},
	}
	seedCommand.Flags().StringVarP(&seedURL, "url", "u", "", "URL to add as a seed")
	walkerCommand.AddCommand(seedCommand)

	var outfile string
	schemaCommand := &cobra.Command{
		Use:   "schema",
		Short: "output the walker schema",
		Long: `Schema prints the walker schema to stdout, substituting
schema-relevant configuration items (ex. keyspace, replication factor).
Useful for something like:
	$ <edit walker.yaml as desired>
	$ walker schema -o schema.cql
	$ <edit schema.cql further as desired>
	$ cqlsh -f schema.cql
`,
		Run: func(cmd *cobra.Command, args []string) {
			readConfig()
			if outfile == "" {
				fatalf("An output file is needed to execute; add with --out/-o")
			}

			out, err := os.Create(outfile)
			if err != nil {
				panic(err.Error())
			}
			defer out.Close()

			schema, err := GetCassandraSchema()
			if err != nil {
				panic(err.Error())
			}
			fmt.Fprint(out, schema)
		},
	}
	schemaCommand.Flags().StringVarP(&outfile, "out", "o", "", "File to write output to")
	walkerCommand.AddCommand(schemaCommand)

	Cmd.Command = walkerCommand
}
