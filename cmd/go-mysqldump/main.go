package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/pingcap/errors"

	"github.com/gongzhxu/go-mysql/dump"
)

var (
	addr      = flag.String("addr", "127.0.0.1:3306", "MySQL addr")
	user      = flag.String("user", "root", "MySQL user")
	password  = flag.String("password", "", "MySQL password")
	execution = flag.String("exec", "", "mysqldump/mariadb-dump execution path")
	output    = flag.String("o", "", "dump output, empty for stdout")

	dbs           = flag.String("dbs", "", "dump databases, separated by comma")
	tables        = flag.String("tables", "", "dump tables, separated by comma, will overwrite dbs")
	tableDB       = flag.String("table_db", "", "database for dump tables")
	ignoreTables  = flag.String("ignore_tables", "", "ignore tables, must be database.table format, separated by comma")
	skipBinlogPos = flag.Bool("skip-binlog-pos", false, "skip fetching binlog position via --master-data/--source-data")
)

func main() {
	flag.Parse()

	d, err := dump.NewDumper(*execution, *addr, *user, *password)
	if err != nil {
		fmt.Printf("Create Dumper error: %v\n", errors.ErrorStack(err))
		os.Exit(1)
	}

	d.SkipMasterData(*skipBinlogPos)

	if len(*ignoreTables) > 0 {
		subs := strings.Split(*ignoreTables, ",")
		for _, sub := range subs {
			if seps := strings.Split(sub, "."); len(seps) == 2 {
				d.AddIgnoreTables(seps[0], seps[1])
			}
		}
	}

	if len(*tables) > 0 && len(*tableDB) > 0 {
		subs := strings.Split(*tables, ",")
		d.AddTables(*tableDB, subs...)
	} else if len(*dbs) > 0 {
		subs := strings.Split(*dbs, ",")
		d.AddDatabases(subs...)
	}

	f := os.Stdout

	if len(*output) > 0 {
		f, err = os.OpenFile(*output, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Printf("Open file error %v\n", errors.ErrorStack(err))
			os.Exit(1)
		}
	}

	defer f.Close()

	if err = d.Dump(f); err != nil {
		fmt.Printf("Dump MySQL error %v\n", errors.ErrorStack(err))
		os.Exit(1)
	}
}
